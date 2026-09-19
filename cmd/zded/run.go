package main

import (
	"context"
	"fmt"
	"os"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/zded"
)

// notifier lets startup-order tests block the session bus independently.
type notifier func(sink attn.Sink, version string) (*attn.Server, error)

// run serves the socket before contacting the bus so notification startup cannot
// block keybinds. Clipboard injection keeps tests from reading the real session.
func run(ctx context.Context, socket, jrnPath, desksDir, histPath string, notify notifier, clipboard zded.Clipboard) error {
	// A listener failure must stop history saving just as a signal does.
	ctx, stopSaving := context.WithCancel(ctx)
	defer stopSaving()
	jrn, err := journal.Open(jrnPath)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	defer jrn.Close()
	if n := jrn.Skipped(); n > 0 {
		fmt.Fprintf(errOut, "zded: journal: %d entries could not be read\n", n)
	}

	srv := zded.New(version, jrn, compositor{}, manifest.Dir(desksDir))
	// Restore before new arrivals; missing history must not prevent startup.
	if err := srv.LoadHistory(histPath); err != nil {
		complain(fmt.Errorf("notification history: %w", err))
	}
	srv.UseClipboard(clipboard)
	srv.UseSound(zded.Wpctl{})
	if err := srv.Listen(socket); err != nil {
		return err
	}
	defer os.Remove(socket)
	fmt.Fprintf(errOut, "zded %s listening on %s\n", version, socket)

	// Install placement rules before any desk app can be launched.
	srv.SyncRules()

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	serving := make(chan error, 1)
	go func() { serving <- srv.Serve() }()

	saved := make(chan struct{})
	go func() { srv.KeepHistory(ctx, histPath); close(saved) }()

	go srv.Watch(ctx, subscribe)
	go srv.WatchClipboard(ctx)

	// Notifications are optional; losing the bus must not take desks down.
	if n, err := notify(srv, version); err != nil {
		complain(fmt.Errorf("notifications: %w", err))
	} else {
		defer n.Close()
		srv.Watching(n)
		fmt.Fprintln(errOut, "zded: notifications: listening")
	}

	err = <-serving
	// Close is idempotent and bounded. Wait here too: Serve can return before
	// the signal goroutine finishes stopping the subprocess groups.
	srv.Close()
	// Save last, including arrivals during shutdown, and wait before exiting.
	stopSaving()
	<-saved
	return err
}
