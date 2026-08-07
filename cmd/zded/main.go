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

	"github.com/crispuscrew/zde/internal/attn"
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

	// The signal has to reach the listener, or a stale socket outlives the
	// daemon and the next zded refuses to start.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *socket, *jrnPath, *desksDir, attn.Serve); err != nil {
		fatal(err)
	}
}

// notifier is how the daemon takes the session's notification name. A parameter
// rather than a call to attn.Serve, because what this file is really about is
// the order things are brought up in, and the way to test an order is to hand it
// something that will not come up.
type notifier func(sink attn.Sink, version string) (*attn.Server, error)

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
func run(ctx context.Context, socket, jrnPath, desksDir string, notify notifier) error {
	jrn, err := journal.Open(jrnPath)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	defer jrn.Close()
	if n := jrn.Skipped(); n > 0 {
		fmt.Fprintf(os.Stderr, "zded: journal: %d entries could not be read\n", n)
	}

	srv := zded.New(version, jrn, compositor{}, manifest.Dir(desksDir))
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

	// Keep the names true while the session runs, rather than only when
	// someone asks. This is also what makes zded worth having running: a
	// workspace is adopted the moment something is in it.
	go srv.Watch(ctx, subscribe)

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
