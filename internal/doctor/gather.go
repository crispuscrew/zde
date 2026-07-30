package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/zded"
)

// Session is everything doctor could find out, gathered before anything is
// judged. A struct and not a set of calls, so that Judge is pure and a session
// nobody has ever had can be written down in a test.
type Session struct {
	// Socket is where zded's socket belongs, said even when nothing is
	// answering on it: half the reports that start "zde says nothing" are a
	// daemon listening in another session's runtime directory.
	Socket string
	// Status is what the daemon said, and nil when it said nothing. Everything
	// zded alone can answer hangs off this.
	Status  *zded.Status
	DialErr error

	// Notify is who holds the notification name when zded does not, and empty
	// when nobody does. Asked of the bus only when the answer can matter,
	// because a session that is well has nothing to learn from it.
	Notify    string
	NotifyErr error

	Units  []Unit
	Podman Podman
	Lock   Locker
}

// Unit is one systemd user unit as systemctl reports it.
type Unit struct {
	Name  string
	State string // what `is-active` printed: active, inactive, failed, ...
	Err   error  // systemctl could not be asked at all
}

// Podman is whether rootless podman works for this user, which is what layer 2
// will run every app inside.
type Podman struct {
	Rootless bool
	Err      error
}

// Locker is the screen lock: what this machine runs for it, and whether that
// program could authenticate anybody if it ran.
type Locker struct {
	Configured bool
	Argv0      string
	BinErr     error // argv0 is not something this machine can run
	// Service is the PAM service the locker would authenticate against.
	Service string
	PAM     PAMState
	Err     error // the apps file itself could not be read
}

// ServicePath is the file a person would go and look for.
func (l Locker) ServicePath() string { return filepath.Join(pamDir, l.Service) }

// PAMState is whether the locker's PAM service is there. Three values, because
// "this machine has no /etc/pam.d at all" is not the same as "the file is
// missing" - the first is a machine doctor cannot answer for, and answering
// anyway would be a failure invented out of nothing.
type PAMState int

const (
	PAMUnknown PAMState = iota
	PAMPresent
	PAMAbsent
)

// pamDir is where every PAM service on a Linux machine lives.
const pamDir = "/etc/pam.d"

// probeTimeout bounds every subprocess doctor starts. This command is run when
// something is already wrong, so a probe that hangs would take away the one
// thing that could have explained it - and each of these is a program that can
// hang for reasons of its own: a wedged user manager, a container store on a
// filesystem that has gone away.
const probeTimeout = 5 * time.Second

// Gather asks everything, and refuses nothing: every probe records what it
// found or why it could not, and none of them decides what that means.
func Gather() Session {
	s := Session{}
	path, err := zded.DefaultSocket()
	s.Socket = path
	if err != nil {
		s.DialErr = err
	} else {
		s.Status, s.DialErr = ask(path)
	}
	// Only when it can matter. With zded holding the name there is nothing to
	// find out, and connecting to the bus to confirm what the daemon just said
	// would be a second answer that could disagree with the first.
	if s.Status == nil || !s.Status.Notifications {
		s.Notify, s.NotifyErr = attn.Owner()
	}
	// The two units a login starts (nix/home.nix). zde-bar as well as zded,
	// because a bar that is not running is the difference between the picker
	// and a list printed at a key.
	for _, name := range []string{"zded", "zde-bar"} {
		s.Units = append(s.Units, unitState(name))
	}
	s.Podman = probePodman()
	s.Lock = probeLocker()
	return s
}

// ask is the one question doctor puts to the daemon: `zde status`'s own
// method, rather than a second one that could drift from it.
func ask(path string) (*zded.Status, error) {
	c, err := zded.DialPath(path)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var st zded.Status
	if err := c.Call("status", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// unitState reads one unit. is-active exits non-zero for everything but
// active and prints the state either way, so the word on stdout is the answer
// and the exit status is not.
func unitState(name string) Unit {
	out, err := run(probeTimeout, "systemctl", "--user", "is-active", name)
	if state := strings.TrimSpace(out); state != "" {
		return Unit{Name: name, State: state}
	}
	return Unit{Name: name, Err: err}
}

// probePodman asks podman one field. `podman info` is what surfaces the way
// rootless actually breaks - a user with no subuid range - and asking for one
// field rather than the whole report keeps the answer to a word instead of a
// screen of YAML to parse. It is not a cheap call, which is what the deadline
// is for.
func probePodman() Podman {
	out, err := run(probeTimeout, "podman", "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil {
		return Podman{Err: err}
	}
	return Podman{Rootless: strings.TrimSpace(out) == "true"}
}

// probeLocker resolves the lock the same way `zde system lock` does, because a
// check that resolved it any other way would be checking something else.
func probeLocker() Locker {
	all, err := apps.Load(apps.DefaultPath())
	if err != nil {
		return Locker{Err: err}
	}
	argv, err := all.Argv("lock")
	if err != nil {
		// Not this error: apps.Argv answers about a name that was asked for,
		// and doctor is asking about a machine. What is missing is said in the
		// report instead.
		return Locker{}
	}
	l := Locker{Configured: true, Argv0: argv[0]}
	if _, err := exec.LookPath(argv[0]); err != nil {
		l.BinErr = err
	}
	// A locker's PAM service is named after the program: swaylock reads
	// /etc/pam.d/swaylock, hyprlock /etc/pam.d/hyprlock. Nothing declares the
	// pairing anywhere a machine can be asked, so the binary's own name is the
	// only handle there is - and it is the one every locker that authenticates
	// through PAM uses.
	l.Service = filepath.Base(argv[0])
	l.PAM = pamState(pamDir, l.Service)
	return l
}

// pamState reports whether a service file is there, and separately whether the
// question means anything on this machine.
func pamState(dir, service string) PAMState {
	if _, err := os.Stat(filepath.Join(dir, service)); err == nil {
		return PAMPresent
	}
	if _, err := os.Stat(dir); err != nil {
		return PAMUnknown
	}
	return PAMAbsent
}

// run is every subprocess doctor starts. Bounded, and with the program's own
// first line of complaint as the error rather than an exit status nobody sees:
// "cannot find UID/GID for user zde" is the answer, and "exit status 125" is
// the start of another search.
func run(d time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return stdout.String(), fmt.Errorf("%s did not answer in %s", name, d)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if line := firstLine(stderr.String()); line != "" {
			return stdout.String(), errors.New(line)
		}
	}
	return stdout.String(), err
}

// firstLine is as much of a program's complaint as belongs on one line of a
// report that is one line per check.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
