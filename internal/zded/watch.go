package zded

import (
	"context"
	"log"
	"time"
)

// Events is a subscription to the compositor: a channel of event names that
// closes when the compositor goes away.
type Events func() (<-chan string, error)

// settle is how long to wait after an event before acting. Several events
// arrive for one thing a person did - opening a window changes the window
// list, the workspace, and sometimes the strip - and reconciling once at the
// end of that is both cheaper and less surprising than three times during it.
var settle = 150 * time.Millisecond

// retry is how long to wait before dialling a compositor that was not there.
var retry = 2 * time.Second

// Watch keeps the naming model true while the session runs, instead of only
// when someone asks. It reconciles after each burst of compositor events: a
// new workspace gets adopted as soon as something is in it, and a workspace
// dragged between monitors gets its name corrected at once.
//
// This terminates rather than thrashes because reconcile converges - adoption
// names what is unnamed, the second pass finds nothing, and the events that
// its own renaming produced settle into one more no-op pass.
//
// A compositor that is not running is not an error. zded is what gets asked
// why the session is broken, so it waits and tries again rather than exiting.
func (s *Server) Watch(ctx context.Context, subscribe Events) {
	for ctx.Err() == nil {
		events, err := subscribe()
		if err != nil {
			if !sleep(ctx, retry) {
				return
			}
			continue
		}
		s.drain(ctx, events)
		// The stream ended: niri exited, or restarted. Neither is ours to
		// fix, and both are worth waiting out.
		if !sleep(ctx, retry) {
			return
		}
	}
}

// drain reconciles after each burst of events until the stream ends.
func (s *Server) drain(ctx context.Context, events <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-events:
			if !open {
				return
			}
		}
		// Let the rest of the burst arrive before acting on any of it.
		for settling := true; settling; {
			select {
			case <-ctx.Done():
				return
			case _, open := <-events:
				if !open {
					return
				}
			case <-time.After(settle):
				settling = false
			}
		}
		if resp := s.reconcile(); resp.Error != "" {
			// A compositor that went away mid-reconcile is the ordinary case
			// here, and the loop above is already going to wait it out.
			//
			// Through the log rather than straight at stderr, because the log is
			// the daemon's one filtered door (cmd/zded, errOut) and what this
			// prints is niri's sentence with a workspace name in it. Written
			// with fmt.Fprintln it was the terminal that started zded reading
			// niri's bytes as instructions, while the same error was filtered
			// for `zde status` (server.go, status).
			log.Printf("zded: reconcile: %s", resp.Error)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
