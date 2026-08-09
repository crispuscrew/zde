// zded is the zde daemon (docs/vision.md, section 2): it owns the journal,
// reads the compositor, and answers the one socket everything else talks to.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/clip"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/niri"
	"github.com/crispuscrew/zde/internal/zded"
)

// Set at link time by the derivation that builds this (nix/zde.nix), so the
// version a running daemon reports and the version in the store path are the
// same string. A `go build` with no ldflags says so rather than claiming a
// release it is not.
var version = "dev"

func main() {
	socket := flag.String("socket", "", "listen here instead of $XDG_RUNTIME_DIR/zde/zded.sock")
	jrnPath := flag.String("journal", "", "journal path (default $XDG_STATE_HOME/zde/journal.jsonl)")
	desksDir := flag.String("desks", "", "desk manifests (default $XDG_CONFIG_HOME/zde/desks)")
	histPath := flag.String("history", "", "notification snapshot (default $XDG_STATE_HOME/zde/history.json)")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(fmt.Errorf("unexpected argument %q", flag.Arg(0)))
	}

	if *socket == "" {
		s, err := zded.DefaultSocket()
		if err != nil {
			fatal(err)
		}
		*socket = s
	}
	if *jrnPath == "" {
		*jrnPath = journal.DefaultPath()
	}
	if *desksDir == "" {
		*desksDir = manifest.DefaultDir()
	}
	if *histPath == "" {
		*histPath = attn.DefaultSnapshotPath()
	}

	// The signal has to reach the listener, or a stale socket outlives the
	// daemon and the next zded refuses to start.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// run on its own goroutine so that a signal is answered by this one.
	//
	// It used to be called here, and a cancelled context is only a stop if
	// somebody is watching it: run does its whole startup - the journal, the
	// notification history, the desk manifests, niri's placement rules - before
	// it reaches the first line that looks at ctx, so a SIGTERM arriving during
	// any of that was turned into a cancel nobody read. The process stayed.
	// Measured with a FIFO at the journal path: SIGTERM and SIGINT both
	// swallowed, and only SIGKILL ended it - which is also what
	// `systemctl --user stop zded` ran into.
	done := make(chan error, 1)
	go func() { done <- run(ctx, *socket, *jrnPath, *desksDir, *histPath, attn.Serve, clip.Tool{}) }()

	err, stopped := awaitStop(ctx, done, stopGrace)
	if !stopped {
		// Left rather than waited on. Whatever it is stuck in is something no
		// cancel reaches, so the alternatives are this or a daemon that cannot
		// be stopped, and the second one is how an account loses its desktop
		// until somebody finds a shell and a SIGKILL.
		//
		// The socket file is not removed on the way out, and that is not a leak
		// worth staying for: Listen dials a socket it finds before it removes
		// one, so a stale file left by this exit costs the next zded a connect
		// that nobody answers (internal/zded, Listen).
		fmt.Fprintf(os.Stderr, "zded: told to stop and still starting %v later, so it is leaving "+
			"unfinished. Something at one of its paths is not a plain file it can read: the journal, "+
			"the notification history, a desk manifest, or niri's dynamic.kdl\n", stopGrace)
		os.Exit(1)
	}
	if err != nil {
		fatal(err)
	}
}

// stopGrace is how long the daemon has to leave after it has been asked to.
//
// Long enough for the tidy exit, which is the whole point of having a grace
// rather than exiting on the signal: run stops the tiers it started and waits
// up to two seconds for their process groups to go (internal/zded,
// askStopWait), and then writes the last notification snapshot, which is a file
// write on whatever disk the person has. Five seconds covers both with room,
// and anything longer than that is not a slow stop - it is a stop that is not
// happening.
const stopGrace = 5 * time.Second

// awaitStop waits for the daemon to finish, and says whether it did.
//
// Two waits and not one. Before the stop is asked for there is no deadline at
// all: a daemon that has been up for six hours is not late. After it, there is,
// because the promise a stop makes is that the process goes - and every way of
// keeping that promise cooperatively runs through code that has to be looking
// at the context, which is exactly what the thing being guarded against is not
// doing.
//
// Its own function because the decision is testable and main is not.
func awaitStop(ctx context.Context, done <-chan error, grace time.Duration) (err error, stopped bool) {
	select {
	case err := <-done:
		return err, true
	case <-ctx.Done():
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case err := <-done:
		return err, true
	case <-t.C:
		return nil, false
	}
}

// notifier is how the daemon takes the session's notification name. A parameter
// rather than a call to attn.Serve, because what this file is really about is
// the order things are brought up in, and the way to test an order is to hand it
// something that will not come up.
type notifier func(sink attn.Sink, version string) (*attn.Server, error)

// The clipboard is a parameter for a sharper version of the same reason. It
// spawns wl-paste against whatever Wayland session the machine has, so a test
// of this file that took the real one would sit there recording the developer's
// own clipboard - which it did, once, before this was a parameter.

// run is the daemon from a bound socket to the last connection answered.
//
// The order below is the point of this function, and it changed. attn.Serve
// used to come before srv.Serve, and a session bus that accepted the connection
// and then would not authenticate stopped zded there - with the listener
// already bound, so every `zde` call landed in the backlog and got nothing back
// until the client gave up five seconds later. Every keybind in the session,
// failing, because of the notification server.
//
// So the socket is answering first, and everything that talks to a bus happens
// behind it. Nothing zded does for a keypress needs the notification server:
// notifications arrive at it, and a session with none is a session that still
// switches desks. Taking the name is bounded too (internal/attn, Serve), which
// is what makes it safe for this to be the thing the main goroutine sits in
// while the socket is served from another.
func run(ctx context.Context, socket, jrnPath, desksDir, histPath string, notify notifier, clipboard zded.Clipboard) error {
	// Cancelled by the caller's signal, and by anything below that ends the
	// daemon on its own: the goroutine writing the notification history has to
	// be told either way, or a zded that stopped because its listener failed
	// would sit here waiting for a snapshot nobody asked for (see the wait at
	// the end).
	ctx, stopSaving := context.WithCancel(ctx)
	defer stopSaving()
	jrn, err := journal.Open(jrnPath)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	defer jrn.Close()
	if n := jrn.Skipped(); n > 0 {
		fmt.Fprintf(os.Stderr, "zded: journal: %d entries could not be read\n", n)
	}

	srv := zded.New(version, jrn, compositor{}, manifest.Dir(desksDir))
	// What the last session was interrupted by, put back before anything can
	// arrive on top of it (internal/attn, snapshot.go). Never fatal: the
	// notification center starting empty is where a first login starts, and it
	// is not worth a session over (internal/zded, LoadHistory).
	if err := srv.LoadHistory(histPath); err != nil {
		fmt.Fprintf(os.Stderr, "zded: notification history: %v\n", err)
	}
	srv.UseClipboard(clipboard)
	if err := srv.Listen(socket); err != nil {
		return err
	}
	// On every way out, not only the tidy one: a socket file left behind is what
	// makes the next zded refuse to start.
	defer os.Remove(socket)
	fmt.Fprintf(os.Stderr, "zded %s listening on %s\n", version, socket)

	// What the desks say about where their windows open, handed to niri before
	// anything can be launched (internal/zded/rules.go). At startup because the
	// manifests are files: they change when somebody edits one, not when a desk
	// is entered.
	srv.SyncRules()

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	// Answering keybinds, from here on and before anything touches a bus.
	serving := make(chan error, 1)
	go func() { serving <- srv.Serve() }()

	// And the history written down while the session runs, so a zded that is
	// killed rather than asked to stop costs the last couple of minutes and not
	// the whole day (internal/zded, KeepHistory).
	saved := make(chan struct{})
	go func() { srv.KeepHistory(ctx, histPath); close(saved) }()

	// Keep the names true while the session runs, rather than only when
	// someone asks. This is also what makes zded worth having running: a
	// workspace is adopted the moment something is in it.
	go srv.Watch(ctx, subscribe)

	// And the clipboard, on a goroutine of its own for the same reason: it
	// spawns wl-paste and waits on a session that may have neither the program
	// nor a compositor that supports it, and none of that is allowed to be
	// between a keypress and the socket that answers it (internal/zded/clip.go).
	go srv.WatchClipboard(ctx)

	// The session's notification server, if this session has a bus and nobody
	// else has taken the name. Not fatal either way: zded runs the desks
	// whether or not anything can send it a notification, and a daemon that
	// refused to start because of a bus would take the desks down with it.
	if n, err := notify(srv, version); err != nil {
		fmt.Fprintf(os.Stderr, "zded: notifications: %v\n", err)
	} else {
		defer n.Close()
		// So that finishing something with `zde queue done` tells whoever sent
		// it. A client blocked on its closure has no other way to find out.
		srv.Watching(n)
		fmt.Fprintln(os.Stderr, "zded: notifications: listening")
	}

	err = <-serving
	// And the tiers, before this process goes.
	//
	// Close is already called from the goroutine above, but that one races the
	// exit: closing the listener is what makes Serve return, so run could be
	// back in main with the tiers still being stopped. Called again here, on the
	// goroutine that is actually leaving, so the wait inside it is a wait this
	// process does. Close is idempotent and bounded (internal/zded, stopRuns).
	//
	// On every way out and not only the signal: a Serve that returned an error
	// ends the daemon just as thoroughly, and a tier is a subprocess in a
	// process group the session's own signal cannot reach - so nothing else
	// would ever stop it.
	srv.Close()
	// Then the last snapshot, waited for rather than left to a goroutine the
	// process is about to exit out from under. A logout is the ordinary way a
	// session ends, so it must not be the ordinary way the last arrivals are
	// lost. After the tiers rather than before, so that the writing is the last
	// thing this process does and takes in whatever arrived while they died.
	stopSaving()
	<-saved
	return err
}

// subscribe opens an event stream. The connection is not shared with the
// request path: the stream owns its socket for as long as it lasts.
func subscribe() (<-chan string, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	events, err := c.Events()
	if err != nil {
		c.Close()
		return nil, err
	}
	return events, nil
}

// compositor dials niri per call. The connection is deliberately not held
// open: niri restarts, and a daemon holding a dead socket would answer with
// stale truth instead of saying it cannot see the compositor.
type compositor struct{}

func (compositor) DeskMap() (*desk.Map, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.DeskMap()
}

func (compositor) FocusedName() (string, error) {
	c, err := niri.Dial()
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.FocusedName()
}

func (compositor) FocusedOutput() (string, error) {
	c, err := niri.Dial()
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.FocusedOutput()
}

func (compositor) FocusedPlace() (string, string, error) {
	c, err := niri.Dial()
	if err != nil {
		return "", "", err
	}
	defer c.Close()
	return c.FocusedPlace()
}

func (compositor) MoveWindowToWorkspace(name string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.MoveWindowToWorkspace(name)
}

func (compositor) FocusedWindow() (uint64, error) {
	c, err := niri.Dial()
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return c.FocusedWindow()
}

// Windows is niri's answer in the shape the socket speaks. The two structs
// stay apart on purpose: one is what the compositor said, the other is what
// goes out over the zde socket and is read by a shell, and tying the wire to
// niri's field names would make a niri rename somebody else's problem. Same
// seam, and same reason, as AsDeskWorkspaces.
func (compositor) Windows() ([]zded.Window, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	open, err := c.OpenWindows()
	if err != nil {
		return nil, err
	}
	out := make([]zded.Window, 0, len(open))
	for _, w := range open {
		out = append(out, zded.Window{
			ID:        w.ID,
			Title:     w.Title,
			AppID:     w.AppID,
			Workspace: w.Workspace,
		})
	}
	return out, nil
}

func (compositor) FocusWindow(id uint64) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.FocusWindow(id)
}

func (compositor) FocusWindowVertically(down bool) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.FocusWindowVertically(down)
}

func (compositor) Perform(action string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Perform(action)
}

func (compositor) FocusWorkspace(name string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.FocusWorkspace(name)
}

func (compositor) RenameWorkspace(from, to string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.RenameWorkspace(from, to)
}

func (compositor) EmptyByOutput() (map[string][]uint64, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.EmptyByOutput()
}

func (compositor) FirstApps() (map[uint64]string, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.FirstApps()
}

func (compositor) SetWorkspaceNameByID(id uint64, name string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.SetWorkspaceNameByID(id, name)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "zded:", err)
	os.Exit(1)
}
