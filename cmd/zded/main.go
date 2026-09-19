// zded owns the journal, reads the compositor and serves the desktop socket.
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
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/zded"
)

// Set by nix/zde.nix with -X main.version; local builds report dev.
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Startup can block before reading ctx (for example on a FIFO). Keep the
	// signal wait independent so even that daemon has a bounded stop.
	done := make(chan error, 1)
	go func() { done <- run(ctx, *socket, *jrnPath, *desksDir, *histPath, attn.Serve, clip.Tool{}) }()

	err, stopped := awaitStop(ctx, done, stopGrace)
	if !stopped {
		// Listen probes and removes stale sockets on the next start; waiting
		// indefinitely for uncancellable startup would prevent stopping at all.
		fmt.Fprintf(errOut, "zded: told to stop and still starting %v later, so it is leaving "+
			"unfinished. Something at one of its paths is not a plain file it can read: the journal, "+
			"the notification history, a desk manifest, or niri's dynamic.kdl\n", stopGrace)
		os.Exit(1)
	}
	if err != nil {
		fatal(err)
	}
}

// Allow the bounded two-second subprocess stop plus the final history write,
// but do not wait indefinitely for startup code that cannot observe cancellation.
const stopGrace = 5 * time.Second

// awaitStop has no deadline until cancellation, then allows a graceful exit.
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
