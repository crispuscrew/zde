package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/bus"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/zded"
	"github.com/crispuscrew/zde/internal/zinc"
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
	Desks  Desks
	Power  Logind
}

// Desks is what the manifests name, judged against what this machine can
// actually start. It is the ahead-of-time half of a desk switch: entering a
// desk launches what it declares, and until now the only record of a name
// nothing could run was a line in zded's log.
type Desks struct {
	// Dir is where the manifests were read from, said on the healthy line
	// because it is the one reading that can be about the wrong directory: zded
	// takes -desks and this is the default both of them use (cmd/zded).
	Dir string
	// Err is why this check could not be made at all - the apps file, the
	// directory itself, or a zcr that stopped answering. Said rather than left
	// out, because an empty list of problems reads as an all-clear.
	Err error
	// Resolver is which of the two was asked, and it is printed on every line
	// of this check. The two do not answer the same question: a manifest's
	// `app:` is a zinc app name, the thing `zcr run <app>@<instance>` takes
	// (internal/manifest, App), and zde.apps is the keymap's own
	// logical-name-to-argv map that layer 1 writes (internal/apps). The docs
	// pair them by convention and no machine enforces it, so which one answered
	// is the difference between a warning worth acting on and one to ignore.
	Resolver string
	// Configured is whether this machine has any app in zde.apps at all. One
	// fact and one edit, so the report says it once instead of once per name a
	// desk happens to mention - and it is about zde.apps alone, which is why
	// only that resolver's lines use it (see deskApps).
	Configured bool
	// Unrunnable is one entry per app a desk names that nothing here can start.
	Unrunnable []DeskApp
}

// The two things on a machine that can answer "is there anything to start under
// that name". zcr where there is one, because it is the resolver a launch
// actually uses (internal/zinc, Run); zde.apps for the machine layer 2 has not
// reached.
const (
	byZcr  = zinc.Runner
	byApps = "zde.apps"
)

// Logind is systemd-logind on the system bus, and whether this session may use
// it. It is the machine half of the power menu: lock, log out, suspend, reboot
// and power off, of which the last four are logind's - TerminateSession,
// Suspend, Reboot and PowerOff on org.freedesktop.login1.Manager.
//
// It is checked whether or not there is a menu to press, and it landed before
// there was one on purpose: "the power menu will refuse everything on this
// machine" is a sentence about polkit, an inhibitor or a bus that is not there,
// and every one of those is true before any menu is written - and invisible
// until somebody presses the key.
//
// Nothing here imports whatever ends up calling these. This asks logind what it
// would allow; that will ask logind to do it. Two questions, and the second one
// is not a thing doctor may put to a machine somebody is working on.
type Logind struct {
	// Err is why nothing could be asked: no system bus, or a bus with nobody on
	// logind's name. It stands for all four verbs at once, because none of them
	// has anywhere to go.
	Err error
	// Session is the logind session a log out would end, and empty when logind
	// can name none - which is a log out that has to refuse rather than guess at
	// somebody else's.
	Session string
	// SessionErr is why logind could not be asked which session this is.
	SessionErr error
	// Can is one entry per verb logind has a question for, in the order the menu
	// would list them.
	Can []Can
}

// Can is one power verb and logind's own word about it: yes, no, na (the
// machine cannot do it at all) or challenge (polkit would want an
// authentication first).
type Can struct {
	// What is the verb as a person would say it, since that is what the line
	// prints. The method behind it is CanSuspend, CanReboot, CanPowerOff.
	What   string
	Answer string
	Err    error
}

// DeskApp is one manifest's reference to an app, and what resolving it said.
//
// The desk names the manifest rather than a path: manifests are keyed by the
// desk they declare and not by their filename (internal/manifest, LoadDir), so
// this is the handle that is true whatever the file is called.
type DeskApp struct {
	Desk string
	App  string
	Err  error
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
	// Off the disk rather than out of the daemon, for the reason the locker
	// check reads the apps file itself: the session this is run on is often one
	// where zded is the thing that is wrong, and a check that could only be made
	// through it would go blank exactly when it is wanted.
	s.Desks = probeDesks(manifest.DefaultDir())
	s.Power = probeLogind()
	return s
}

// probeDesks asks, of every app every desk declares, the question a launch
// asks: is there anything on this machine to run under that name.
//
// Which resolver is asked is the whole of this check. zde.apps answers about
// the keymap's logical names - "terminal", "editor" - and a manifest's `app:`
// is a zinc app name, which is a different namespace that happens to be written
// the same way in the docs. Judged against zde.apps alone, a machine that keeps
// its apps in zinc and never sets that option is warned about every desk it
// has, and every one of those desks launches perfectly: a warning that is wrong
// on the ordinary machine is one people stop reading, which costs the warnings
// that are right.
//
// So zcr answers where there is a zcr, which is the same thing `zde desk apps`
// asks (cmd/zde) and the same thing a switch ends up in (internal/zded,
// launch). zde.apps stays the answer for a machine without one, because until
// layer 2 reaches a machine that map is all there is (docs/delivery.md).
func probeDesks(dir string) Desks {
	d := Desks{Dir: dir, Resolver: byApps}
	// PATH here rather than zded's answer about its own PATH (Status.Zinc), for
	// the reason the rest of this check is made off the disk: the session this
	// is run on is often one where zded is what is wrong, and an answer that
	// needed it would go blank exactly when it is wanted.
	if _, err := exec.LookPath(byZcr); err == nil {
		d.Resolver = byZcr
	}
	var all apps.Apps
	if d.Resolver == byApps {
		var err error
		all, err = apps.Load(apps.DefaultPath())
		if err != nil {
			d.Err = err
			return d
		}
		d.Configured = len(all.Names()) > 0
	}
	desks, _, err := manifest.LoadDir(dir)
	if err != nil {
		d.Err = err
		return d
	}
	// The problems LoadDir also returns are deliberately dropped here: a
	// manifest that will not parse is already a line of its own in this report
	// (see manifests), and one file being wrong is not a thing to say twice.
	//
	// Sorted, because this is printed: a report whose lines change places between
	// two runs reads as a machine that changed.
	names := make([]string, 0, len(desks))
	for name := range desks {
		names = append(names, name)
	}
	sort.Strings(names)
	// One answer per name for the whole directory. Asking zcr is a process, so
	// the same browser on four desks would be four of them - and, worse, a name
	// answered twice is a report that can contradict itself halfway down.
	answered := map[string]error{}
	for _, name := range names {
		seen := map[string]bool{}
		for _, app := range desks[name].Apps {
			// Once per name, not once per instance. Two desks' worth of the same
			// browser is one app to define, and a line each would be the same
			// edit listed twice.
			if seen[app.App] {
				continue
			}
			seen[app.App] = true
			refused, asked := answered[app.App]
			if !asked {
				var broken error
				refused, broken = resolves(d.Resolver, all, app.App)
				if broken != nil {
					// The resolver stopped answering, so what it has said so far
					// is not a list of bad manifests: it is the beginning of one,
					// about a machine whose resolver went quiet partway through.
					// Dropped rather than printed, because a partial list of
					// faults reads exactly like a complete one.
					return Desks{Dir: dir, Resolver: d.Resolver, Err: broken}
				}
				answered[app.App] = refused
			}
			if refused != nil {
				d.Unrunnable = append(d.Unrunnable, DeskApp{Desk: name, App: app.App, Err: refused})
			}
		}
	}
	return d
}

// resolves puts one app name to whichever resolver is answering, and keeps the
// two things that can come back apart.
//
// refused is the resolver's own answer about that name, and it belongs on a
// line beside the desk that named it. broken is the resolver not answering at
// all, which is a fact about the machine: written down per app it would blame
// every desk on it for one wedged program.
func resolves(resolver string, all apps.Apps, app string) (refused, broken error) {
	if resolver != byZcr {
		// apps.Argv's own error, alternatives and all: it knows what this
		// machine does have, which is the other half of the fix.
		_, err := all.Argv(app)
		return err, nil
	}
	// `zcr where` is the question because zcr answers it only for an app it
	// has: the state directory and the bus socket it prints come out of that
	// app's own config, so it refuses a name it cannot load rather than
	// guessing - `no app "x" defined (try: zc list)` (zinc 0.9.1, cmdWhere).
	//
	// The bare name and not app@instance. Whether an app is defined is a fact
	// about the app, so the instance would only add ways for this to fail on
	// something that is not what is being asked - and one answer then serves
	// every desk that names it.
	//
	// What it printed is dropped. Where an instance keeps its state is zinc's
	// to say and the launcher's to use (internal/zinc, Where); doctor is only
	// asking whether there is anything there to launch.
	if _, err := run(probeTimeout, byZcr, "where", app); err != nil {
		var silent *exec.ExitError
		var late noAnswer
		if errors.Is(err, exec.ErrNotFound) || errors.As(err, &silent) || errors.As(err, &late) {
			// zcr was on PATH a moment ago and is not answering questions about
			// apps: it went away, it timed out, or it failed with nothing to
			// say. None of those is something a manifest did.
			return nil, fmt.Errorf("%s is on PATH and could not be asked about %q: %s (try `%s where %s`)",
				byZcr, app, err, byZcr, app)
		}
		// Anything else is zcr refusing that name in its own words - undefined,
		// a VM app zvr owns, a config that will not validate - and every one of
		// them is a desk that will not start it. zcr says why better than a
		// paraphrase of zcr would.
		return err, nil
	}
	return nil, nil
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

// logind's own names, on the system bus. Every one of them is API: they are
// what loginctl calls, and what a power menu would call.
const (
	logindName = "org.freedesktop.login1"
	logindMgr  = logindName + ".Manager"
	logindUser = logindName + ".User"
	logindPath = dbus.ObjectPath("/org/freedesktop/login1")
	propsGet   = "org.freedesktop.DBus.Properties.Get"
)

// askFor bounds the whole logind probe, not each question in it. Five calls on
// a local system bus is microseconds, so anything that needs longer is a
// machine that is already unwell - and doctor is not the command to spend ten
// seconds discovering that, since it is the one somebody is running because
// something else has already gone wrong. Two seconds is what every other bus
// question in zde gets (internal/bt, askFor; internal/link, askFor); the
// connect that comes first is bounded separately (internal/bus).
const askFor = 2 * time.Second

// probeLogind asks logind the four questions a power menu's four verbs turn
// into. Three of them logind answers directly - CanSuspend, CanReboot,
// CanPowerOff, which is polkit's verdict for this session rather than a guess
// at one - and the fourth, a log out, is whether logind can name a session to
// end at all.
//
// A machine with no logind is answered rather than skipped: containers and
// build sandboxes have none, and so does a session where the system bus is
// what broke, which is precisely when somebody wants to know why the power
// menu refuses everything.
func probeLogind() Logind {
	conn, err := bus.System()
	if err != nil {
		return Logind{Err: err}
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), askFor)
	defer cancel()

	// Whether anybody holds the name, and not a call to find out the hard way:
	// a call to a name nobody owns starts the service on some machines and
	// times out on others, and neither is a reading about this session.
	var owned bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, logindName).Store(&owned); err != nil {
		return Logind{Err: err}
	}
	if !owned {
		return Logind{Err: errors.New("nothing owns " + logindName)}
	}

	l := Logind{}
	mgr := conn.Object(logindName, logindPath)
	// In the order a menu would list them, because this is printed. The lock is
	// not here: it is whatever zde.apps names and it has a check of its own
	// (locker), which is the one place on this machine that knows what locks
	// this screen.
	for _, verb := range []struct{ what, method string }{
		{"suspend", "CanSuspend"},
		{"reboot", "CanReboot"},
		{"power off", "CanPowerOff"},
	} {
		c := Can{What: verb.what}
		if err := mgr.CallWithContext(ctx, logindMgr+"."+verb.method, 0).Store(&c.Answer); err != nil {
			c.Err = err
		}
		l.Can = append(l.Can, c)
	}
	l.Session, l.SessionErr = displaySession(ctx, conn)
	return l
}

// displaySession asks logind for this user's graphical session, which is the
// session a log out would end.
//
// Deliberately not GetSessionByPID, which is the exact answer and the wrong
// one to check with: doctor is typed into a terminal inside the session, where
// that call works, and a log out is run from the daemon, whose unit lives in
// user@.service outside every session's cgroup - so it never works there. An
// all-clear from a call the daemon cannot make is worse than no check.
//
// An empty id is not an error. It means logind can name no session for this
// user, which is a log out that has to refuse rather than end somebody else's.
func displaySession(ctx context.Context, conn *dbus.Conn) (string, error) {
	var user dbus.ObjectPath
	if err := conn.Object(logindName, logindPath).
		CallWithContext(ctx, logindMgr+".GetUser", 0, uint32(os.Getuid())).Store(&user); err != nil {
		return "", err
	}
	var v dbus.Variant
	if err := conn.Object(logindName, user).
		CallWithContext(ctx, propsGet, 0, logindUser, "Display").Store(&v); err != nil {
		return "", err
	}
	// logind's Display is a (session id, object path) pair, and it is the pair
	// or nothing: a machine with no graphical session gives back an empty id.
	var display struct {
		ID   string
		Path dbus.ObjectPath
	}
	if err := v.Store(&display); err != nil {
		return "", err
	}
	return display.ID, nil
}

// noAnswer is a probe that ran out of time instead of answering. A type and
// not a message, because one caller has to tell a program's answer from its
// silence: zcr refusing an app name is a manifest to correct, and zcr saying
// nothing at all is not something to write down beside somebody's desk as
// though the desk were wrong (resolves).
type noAnswer struct{ said string }

func (n noAnswer) Error() string { return n.said }

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
		return stdout.String(), noAnswer{fmt.Sprintf("%s did not answer in %s", name, d)}
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
