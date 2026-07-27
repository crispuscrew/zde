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

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/niri"
	"github.com/crispuscrew/zde/internal/zded"
)

const version = "0.1.0"

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

	jrn, err := journal.Open(*jrnPath)
	if err != nil {
		fatal(fmt.Errorf("journal: %w", err))
	}
	defer jrn.Close()
	if n := jrn.Skipped(); n > 0 {
		fmt.Fprintf(os.Stderr, "zded: journal: %d entries could not be read\n", n)
	}

	srv := zded.New(version, jrn, compositor{}, func() (map[string]*manifest.Desk, error) {
		return manifest.LoadDir(*desksDir)
	})
	if err := srv.Listen(*socket); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "zded %s listening on %s\n", version, *socket)

	// The signal has to reach the listener, or a stale socket outlives the
	// daemon and the next zded refuses to start.
	ctx, stopWatching := context.WithCancel(context.Background())
	defer stopWatching()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		stopWatching()
		srv.Close()
	}()

	// Keep the names true while the session runs, rather than only when
	// someone asks. This is also what makes zded worth having running: a
	// workspace is adopted the moment something is in it.
	go srv.Watch(ctx, subscribe)

	if err := srv.Serve(); err != nil {
		fatal(err)
	}
	os.Remove(*socket)
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
