package zded

import (
	"context"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/desk"
)

func init() {
	// The waits are the point of the design, not of the tests.
	settle = 5 * time.Millisecond
	retry = 5 * time.Millisecond
}

// An event is a wake-up: zded re-reads the world and makes the names true,
// without anyone running a command.
func TestWatchReconcilesOnEvents(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.DP-1.code", Output: "HDMI-A-1"}, // moved
		}, []string{"DP-1", "HDMI-A-1"}),
		focused: "vshop.DP-1.code",
	}
	s := New("test", nil, niri, nil)

	events := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Watch(ctx, func() (<-chan string, error) { return events, nil })

	events <- "WorkspacesChanged"
	waitFor(t, func() bool { return len(niri.renameCalls()) == 1 })
	if got := niri.renameCalls()[0]; got != "vshop.DP-1.code -> vshop.HDMI-A-1.code" {
		t.Errorf("renamed %q", got)
	}
}

// Several events arrive for one thing a person did. Acting once at the end of
// the burst is cheaper and less surprising than acting three times during it.
func TestWatchSettlesABurst(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}
	s := New("test", nil, niri, nil)

	events := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Watch(ctx, func() (<-chan string, error) { return events, nil })

	for i := 0; i < 5; i++ {
		events <- "WindowOpenedOrChanged"
	}
	waitFor(t, func() bool { return niri.mapReads() > 0 })
	time.Sleep(20 * settle)
	if got := niri.mapReads(); got > 2 {
		t.Errorf("read the world %d times for one burst of five events", got)
	}
}

// A compositor that is not running is not an error: zded is what gets asked
// why the session is broken, so it waits and tries again.
func TestWatchRetriesWhenTheCompositorIsAway(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}
	s := New("test", nil, niri, nil)

	tries := make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Watch(ctx, func() (<-chan string, error) {
		select {
		case tries <- struct{}{}:
		default:
		}
		return nil, errNotThere{}
	})

	for i := 0; i < 3; i++ {
		select {
		case <-tries:
		case <-time.After(2 * time.Second):
			t.Fatal("gave up on a compositor that was not there")
		}
	}
}

// A stream that ends is niri exiting or restarting. Neither is ours to fix,
// and the watcher has to survive both.
func TestWatchResubscribesWhenTheStreamEnds(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}
	s := New("test", nil, niri, nil)

	subs := make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Watch(ctx, func() (<-chan string, error) {
		select {
		case subs <- struct{}{}:
		default:
		}
		ch := make(chan string)
		close(ch) // the compositor hung up immediately
		return ch, nil
	})

	for i := 0; i < 3; i++ {
		select {
		case <-subs:
		case <-time.After(2 * time.Second):
			t.Fatal("did not resubscribe after the stream ended")
		}
	}
}

// Cancelling stops it, so a zded that is shutting down does not keep dialling.
func TestWatchStopsOnContext(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Watch(ctx, func() (<-chan string, error) { return nil, errNotThere{} }); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch ignored a cancelled context")
	}
}

type errNotThere struct{}

func (errNotThere) Error() string { return "no compositor" }

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}
