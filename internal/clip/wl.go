package clip

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Tool is the clipboard as wl-clipboard's two programs expose it.
//
// # Why a subprocess and not the protocol
//
// The Wayland side of a clipboard history is wlr-data-control-unstable-v1 (and
// its successor ext-data-control-v1): bind the manager, take the device for the
// seat, and every selection arrives as an offer that enumerates its mime types
// before anything is read. niri speaks it, which is what makes any of this
// possible at all.
//
// zde does not speak it itself, and the reason is what it would cost. Go has no
// Wayland client in the standard library and this repo vendors its
// dependencies, so speaking it means either a new vendored module with a
// protocol-code generator behind it, or several hundred lines of hand-rolled
// wire format - object ids, opcodes, and file descriptors passed over
// SCM_RIGHTS - in a daemon that already answers every keybind in the session.
// The D-Bus side of zde uses a library for exactly this reason (internal/bus),
// and there is no equivalent here worth pinning.
//
// What that costs, said plainly rather than discovered later:
//
//   - Two processes per copy, and a third when an entry is put back. Copying is
//     a human-rate event, so this is nothing on a clock and is still two
//     processes that did not have to exist.
//   - A gap between finding out what is offered and reading it. In the protocol
//     both come off one offer object; here Types and Read are two connections
//     and therefore two moments, so a selection replaced in the microseconds
//     between them is read as its successor. What that could cost is the
//     sensitive check being made about the previous selection, which is why the
//     check is also cheap enough to be worth repeating (internal/zded, take).
//   - wl-clipboard has to be installed. It is layer 1's to install (nix/
//     home.nix) and a session without it gets one line in the daemon's log and a
//     history that stays empty, rather than a daemon that fails to start.
//
// And why the sensitive check is not simply asked of wl-paste, which is a
// question whose answer moved. `wl-paste --watch` sets CLIPBOARD_STATE for the
// command it spawns, and one of the values it defines is `sensitive`. Up to
// wl-clipboard 2.2.1 that value was never produced - "currently wl-clipboard
// only ever sets CLIPBOARD_STATE to data or nil" - and the first version of this
// file said so as though it were permanent. It is not: 2.3.0, which is what this
// flake pins (nixpkgs 26.05), "only sets it to sensitive when it encounters
// x-kde-passwordManagerHint among the MIME types" (wl-paste(1)). That is the
// same type and the same rule Sensitive applies.
//
// zde still asks the offer, and the reason is no longer that the variable does
// not work:
//
//   - It works on new enough wl-clipboard and silently does not on older, and
//     zde runs on whatever the machine has rather than only on what this flake
//     builds. On 2.2.1 a check written against the variable reads `data` for a
//     password and records it, which is failing open on a difference nothing in
//     the session announces (docs/vision.md, principle 9).
//   - It only arrives in the spawned command's environment, so using it means
//     the watcher's child is a zde program that wl-paste hands the selection to
//     on stdin. It could decline to read that stdin - but the refusal would then
//     be one made about bytes already delivered to something zde wrote, rather
//     than before asking for them.
//   - The types have to be read anyway: which one to ask for, and what to call a
//     non-text offer on a row. The hint is one line of a list this already has,
//     so asking costs nothing that was not already being spent.
//
// On 2.3.0 the two agree by construction, because they are the same check made
// twice. This is the copy zde controls.
type Tool struct{}

// The two programs. Named rather than spelled at each call site, because
// whether they are on the session's PATH is a question asked in two places -
// the watcher at startup, and the palette's row for this action.
const (
	Paste = "wl-paste"
	Copy  = "wl-copy"
)

// guard ties a spawned wl-clipboard process to the daemon that spawned it. For a
// command built with exec.CommandContext, which is the only kind here: it sets
// Cancel, and exec refuses a Cancel with no context to fire it.
//
// Two mechanisms, because zded stops in two ways and only one of them was
// covered. Its own process group, so cancelling reaches what the child started
// rather than only the pid zde knows about - the same reasoning a tier's kill
// has (internal/zded, killGroup). And PR_SET_PDEATHSIG, which is the one this
// was written for: cmd.Cancel only runs while there is a process left to run it,
// so an orderly stop was already fine and SIGKILL, an OOM kill, or the session
// going down underneath the daemon was not. What that left behind was
// `wl-paste --watch` reparented to pid 1, still connected to the compositor and
// still watching the clipboard until logout. One was found alive on a
// developer's machine two hours after the test run that started it.
//
// That is not untidiness, it is the feature contradicting itself: the whole
// argument for keeping a clipboard history in memory is that it dies with the
// daemon, and a watcher nobody can see outliving the daemon is the sharpest
// available way to be wrong about that.
//
// The Go caveat, written down because it decides how this fails rather than
// whether it works: the kernel sends the parent-death signal when the thread
// that forked exits, not when the process does (golang/go#27505). Go does not
// retire ordinary Ms, so in practice that thread lives as long as the daemon -
// and if one ever did go early, the child is killed, the watch ends, and
// WatchClipboard dials another one (internal/zded). A spurious death costs a
// restart, which is the safe direction to be wrong in.
//
// What this does not reach is a grandchild: PR_SET_PDEATHSIG is cleared on fork,
// so the `wl-paste --list-types` children that `wl-paste --watch` spawns do not
// inherit it. They print and exit in milliseconds, and their stdout is the pipe
// zded held, so a dead daemon closes it under them either way. The group kill is
// what covers them while zded is alive to do it.
// spawn builds a guarded wl-clipboard command, and is the only place in this
// package that builds one at all. One construction site rather than two, so that
// "every process this package starts dies with the daemon" is a single fact
// somebody can check, and not a line to remember at each new call.
//
// The one deliberate exception is Write, which does not come through here and
// says why where it is.
func spawn(ctx context.Context, argv ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	guard(cmd)
	return cmd
}

func guard(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			// The group and not the pid, so that stopping a wl-paste stops what
			// the wl-paste started. ESRCH means there was nothing left, which is
			// the outcome this exists for.
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck // ESRCH is the good case
		}
		return nil
	}
}

// within bounds one wl-clipboard run.
//
// A read is a request to whichever application owns the selection, and an
// application that has stopped answering leaves the pipe open rather than
// closing it - so with no bound the watcher goroutine would stop for as long as
// that program lives. Three seconds is far past a clipboard read on a working
// machine and short enough that the next copy is not queued behind a dead one.
const within = 3 * time.Second

// Watch rings the channel when the clipboard changes, until ctx ends or
// wl-paste stops.
//
// The child it spawns is `wl-paste --list-types`, which is doing two things at
// once and neither of them is reading the clipboard's content. It is a bell,
// because a spawned command that prints something is how wl-paste says a change
// happened; and it is the only program this needs to exist beyond the one
// already required, so a watcher that works is a watcher whose one dependency
// is installed.
//
// Deliberately not `--watch <something that reads stdin>`, which is how every
// other clipboard manager is wired - but the difference is narrower than it
// looks, and being exact about it is the point of this paragraph. wl-paste's
// selection callback pipes the selection to itself before it forks: the parent
// calls pipe() and then offer_receive() unconditionally, and only then spawns
// the command with the read end as its stdin (wl-clipboard 2.3.0,
// src/wl-paste.c, selection_callback). A password manager has therefore written
// the secret into a pipe held by a child of zded whatever that command is, and
// choosing `--list-types` does not change it.
//
// What it changes is who reads that pipe. `--list-types` never touches its
// stdin, so those bytes go nowhere and die with the pipe when the child exits;
// and nothing zde wrote asks for the content until the offer has been asked what
// it is and answered something other than the hint (internal/zded, take). So
// "never recorded" means what internal/clip's own header says it means and no
// more: zded's heap never holds it.
func (Tool) Watch(ctx context.Context) (<-chan struct{}, error) {
	if _, err := exec.LookPath(Paste); err != nil {
		return nil, fmt.Errorf("%s is not installed, so nothing can watch the clipboard: %w", Paste, err)
	}
	// The long-lived one, and the reason spawn exists: this process is meant to
	// last the session, and must not last longer than the daemon does.
	cmd := spawn(ctx, Paste, "--watch", Paste, "--list-types")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// wl-paste says why it cannot watch - a compositor with no data-control, no
	// Wayland display - and that sentence is the whole diagnosis, so it goes
	// where the daemon's log goes.
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// One deep, and a send that never blocks. The child prints one line per
	// offered mime type, so a single copy rings this several times and the
	// daemon must not do the work several times: a bell that arrives while the
	// last one is being answered leaves the flag set, and ten of them are still
	// one thing to do, because what the reader catches up with is the clipboard
	// as it is now. A change that does slip through as two reads ends in the
	// same entry either way - a repeat of the newest entry is not a new one
	// (see History.Add).
	changes := make(chan struct{}, 1)
	go func() {
		defer close(changes)
		defer cmd.Wait() //nolint:errcheck // the exit status is the log's, and it is already there
		lines := bufio.NewScanner(out)
		// A mime type is a short line, and an application offering a long one
		// must not be able to grow this buffer.
		lines.Buffer(make([]byte, 0, 4<<10), 64<<10)
		for lines.Scan() {
			select {
			case changes <- struct{}{}:
			default:
			}
		}
	}()
	return changes, nil
}

// typesMax bounds the list of offered types.
//
// The list is an application's to write, so it is bounded like everything else
// an application decides the size of. Eight kilobytes is hundreds of mime type
// names and nothing has ever offered ten.
const typesMax = 8 << 10

// Types is what the current selection is offered as, and it is the question
// that decides whether the content is ever asked for.
//
// A list too long to read is refused whole rather than used in part, which is
// the fail-closed half of it (docs/vision.md, principle 9): the hint that says
// "this is a secret" is one line of this list, and a list cut off before the end
// is one that might have had it. Nothing is recorded from an offer this cannot
// read all of.
func (Tool) Types() ([]string, error) {
	ctx, stop := context.WithTimeout(context.Background(), within)
	defer stop()
	out, more, err := read(ctx, typesMax, Paste, "--list-types")
	if err != nil {
		// An empty clipboard answers this way too, which is not a fault: it is
		// a session where nothing has been copied yet, or one where somebody
		// cleared it.
		return nil, fmt.Errorf("%s --list-types: %w", Paste, err)
	}
	if more {
		return nil, fmt.Errorf("%s --list-types: more than %d KiB of type names, "+
			"and a list this cannot read to the end of is one the hint could be past",
			Paste, typesMax>>10)
	}
	var types []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			types = append(types, s)
		}
	}
	return types, nil
}

// Read is the selection in one type, up to limit bytes, and whether there was
// more of it.
func (Tool) Read(mime string, limit int) ([]byte, bool, error) {
	ctx, stop := context.WithTimeout(context.Background(), within)
	defer stop()
	// --no-newline, because wl-paste adds one to text that did not have one,
	// and an entry put back on the clipboard has to be what was copied.
	data, more, err := read(ctx, limit, Paste, "--no-newline", "--type", mime)
	if err != nil {
		return nil, false, fmt.Errorf("%s --type %s: %w", Paste, mime, err)
	}
	return data, more, nil
}

// read runs one wl-clipboard program and takes up to limit bytes of what it
// says, answering whether there was more.
//
// Through a pipe with a limit on it rather than with Output(), which is the
// difference between a bound and a wish: Output reads until the writer stops,
// so a copied film would be in this daemon's heap before anything could decide
// it was too big. Past the limit the read stops and the program is killed,
// which is the source application being told through a closed pipe that nobody
// wants the rest.
func read(ctx context.Context, limit int, argv ...string) ([]byte, bool, error) {
	// Bounded by `within` while zded is alive, and by the guard when it is not. A
	// read is a request to whichever application owns the selection, so one that
	// has stopped answering leaves this blocked in a read with no timeout left to
	// enforce it - an orphan holding a clipboard read open.
	cmd := spawn(ctx, argv...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(io.LimitReader(pipe, int64(limit)+1))
	more := len(data) > limit
	if more {
		// Nothing to wait for: what is left is the part that will not be kept.
		cmd.Process.Kill() //nolint:errcheck // it is on its way out either way
	}
	waitErr := cmd.Wait()
	if err != nil {
		clear(data)
		return nil, false, err
	}
	if waitErr != nil && !more {
		// Killed on purpose is not a failure; anything else is the selection
		// having gone away between two questions, which is ordinary.
		clear(data)
		return nil, false, waitErr
	}
	return data, more, nil
}

// Write puts text on the clipboard.
//
// As text/plain and not as whatever it was read as: wl-copy offers the other
// text names (UTF8_STRING and the rest) alongside it by itself, and an entry
// read out of a browser as text/html is words by the time it gets here. What
// this cannot do is put back an offer with several types in it, which is
// wl-clipboard's own documented limit and is the second half of why anything
// that is not text is a note rather than an entry.
func (Tool) Write(text []byte) error {
	if len(text) == 0 {
		return errors.New("nothing to put on the clipboard")
	}
	ctx, stop := context.WithTimeout(context.Background(), within)
	defer stop()
	cmd := exec.CommandContext(ctx, Copy, "--type", "text/plain")
	// Deliberately not guarded, which is the one exception here and is worth the
	// sentence. wl-copy forks a process that serves the selection and then
	// returns, so what is left to orphan exits by itself in milliseconds - and
	// the process that does outlive it has to, or the entry the person just chose
	// disappears off their clipboard the moment zded stops. A group kill would
	// take that server with it, and a parent-death signal on this one would risk
	// killing the write in the window before it has forked.
	cmd.Stdin = bytes.NewReader(text)
	// wl-copy forks and serves the selection from the background, so this
	// returns as soon as the clipboard is taken rather than holding a goroutine
	// for as long as the entry is on it.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", Copy, err)
	}
	return nil
}
