package zded

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/desk"
)

// A writer that a sink can hold, standing in for a connection.
type recorder struct {
	mu    sync.Mutex
	lines bytes.Buffer
	fail  bool
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return 0, errClosed
	}
	return r.lines.Write(p)
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lines.String()
}

var errClosed = &closedErr{}

type closedErr struct{}

func (*closedErr) Error() string { return "connection closed" }

// The picker is a surface somebody else draws, so the verb's job is to tell
// them, and the event has to carry everything the surface needs to draw without
// asking anything else.
//
// Nothing acknowledges here, so Shown is false: a listener that takes the bytes
// and does nothing is exactly the case that used to report success.
func TestSwitcherTellsAListener(t *testing.T) {
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)

	rec := &recorder{}
	k := &sink{w: rec}
	s.listen(k)

	resp := s.Dispatch(Request{Method: "desk.switcher"})
	if resp.Error != "" {
		t.Fatalf("desk.switcher: %s", resp.Error)
	}
	var sw Switcher
	if err := json.Unmarshal(resp.Ok, &sw); err != nil {
		t.Fatal(err)
	}
	if sw.Shown {
		t.Error("nobody acknowledged the event and the answer claims a picker was shown")
	}
	if len(sw.Desks) != 2 {
		t.Errorf("desks = %v", sw.Desks)
	}

	// And the event carries what the surface needs to draw without asking
	// anything else.
	var got struct{ Event Event }
	line := strings.TrimSpace(rec.String())
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the event is not one line of json: %q", line)
	}
	if got.Event.Kind != EventPicker {
		t.Errorf("kind = %q", got.Event.Kind)
	}
	if len(got.Event.Desks) != 2 || got.Event.On != "vshop" {
		t.Errorf("event = %+v, want the desks and where we are", got.Event)
	}
	if got.Event.Output != "DP-1" {
		t.Errorf("output = %q, want the screen being looked at", got.Event.Output)
	}
	if got.Event.Token == "" {
		t.Error("the event carries no token, so nothing can say it drew the surface")
	}
}

// And with an acknowledgement, Shown is true. This is the whole difference
// between the key opening a picker and the key doing nothing: a shell that reads
// its socket while failing to draw counts as neither, so the caller falls back
// to printing the list.
func TestSwitcherReportsShownOnlyWhenAcknowledged(t *testing.T) {
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)

	rec := &recorder{}
	s.listen(&sink{w: rec})

	// The shell's side, in the background: read the token out of the event and
	// send it back, which is what Picker.qml does when the surface exists.
	go func() {
		for i := 0; i < 200; i++ {
			var got struct{ Event Event }
			line := strings.TrimSpace(rec.String())
			if line != "" && json.Unmarshal([]byte(line), &got) == nil && got.Event.Token != "" {
				s.acknowledge(got.Event.Token)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	start := time.Now()
	resp := s.Dispatch(Request{Method: "desk.switcher"})
	took := time.Since(start)
	if resp.Error != "" {
		t.Fatalf("desk.switcher: %s", resp.Error)
	}
	var sw Switcher
	if err := json.Unmarshal(resp.Ok, &sw); err != nil {
		t.Fatal(err)
	}
	if !sw.Shown {
		t.Error("the picker was acknowledged and the answer says nothing was shown")
	}
	// And it did not sit out the timeout to hear it: a key that waits 200ms
	// every time is a key that feels broken.
	if took > 100*time.Millisecond {
		t.Errorf("waited %v for an acknowledgement that arrived at once", took)
	}
}

// An acknowledgement nobody asked for is ignored rather than refused, and
// cannot make some later event report success it never had.
func TestAcknowledgingAnUnknownTokenIsHarmless(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	if resp := s.Dispatch(Request{Method: "shown", Args: []string{"1234"}}); resp.Error != "" {
		t.Errorf("a late acknowledgement was refused: %s", resp.Error)
	}
	rec := &recorder{}
	s.listen(&sink{w: rec})
	var sw Switcher
	json.Unmarshal(s.Dispatch(Request{Method: "desk.switcher"}).Ok, &sw)
	if sw.Shown {
		t.Error("a stale acknowledgement made a later event report shown")
	}
}

// With nothing listening the verb must not pretend: the caller prints the list
// itself, and only knows to because of this field.
func TestSwitcherSaysWhenNothingIsListening(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)

	resp := s.Dispatch(Request{Method: "desk.switcher"})
	if resp.Error != "" {
		t.Fatalf("desk.switcher: %s", resp.Error)
	}
	var sw Switcher
	json.Unmarshal(resp.Ok, &sw)
	if sw.Shown {
		t.Error("nothing was listening and the answer claims the picker was shown")
	}
	if len(sw.Desks) == 0 {
		t.Error("nothing to show and no list to print either")
	}
}

// A listener that has gone away is dropped rather than counted. Counted, it
// would make Shown true for ever and the key would stop printing anything on a
// machine whose shell had died.
func TestBroadcastDropsADeadListener(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	dead := &sink{w: &recorder{fail: true}}
	alive := &recorder{}
	s.listen(dead)
	s.listen(&sink{w: alive})

	if sent := s.broadcast(Event{Kind: EventPicker}); sent != 1 {
		t.Errorf("broadcast reached %d listeners, want 1", sent)
	}
	if sent := s.broadcast(Event{Kind: EventPicker}); sent != 1 {
		t.Errorf("the dead listener is still in the list: reached %d", sent)
	}
	if !strings.Contains(alive.String(), EventPicker) {
		t.Error("the live listener got nothing")
	}
}

// Asking twice is listening once, or every event arrives as many times as the
// shell reconnected.
func TestListenIsIdempotent(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	rec := &recorder{}
	k := &sink{w: rec}
	s.listen(k)
	s.listen(k)

	if sent := s.broadcast(Event{Kind: EventPicker}); sent != 1 {
		t.Errorf("broadcast reached %d listeners after subscribing twice", sent)
	}
}

// The switcher lists what exists, including the regulars: the band is reachable
// from every desk, so a picker that hides it is a picker that cannot take you
// to comms.
func TestSwitcherListsTheRegulars(t *testing.T) {
	m := desk.Rebuild([]desk.Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "regulars.DP-1.comms", Output: "DP-1"},
	}, []string{"DP-1"})
	s := New("test", nil, &fakeCompositor{m: m, focused: "vshop.DP-1.code"}, nil)

	resp := s.Dispatch(Request{Method: "desk.switcher"})
	var sw Switcher
	json.Unmarshal(resp.Ok, &sw)
	found := false
	for _, d := range sw.Desks {
		if d == desk.Regulars {
			found = true
		}
	}
	if !found {
		t.Errorf("desks = %v, and the regulars are not among them", sw.Desks)
	}
}

// The subscription over a real socket, because handle is where it lives: a
// connection that has asked for events keeps answering questions, and events
// arrive on it in between. Both halves matter - a stream that took the
// connection over would break the client that opened it.
func TestEventsArriveOnALiveConnection(t *testing.T) {
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)
	path := serve(t, s)

	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var ack string
	if err := c.Call("events", &ack); err != nil {
		t.Fatalf("subscribing: %v", err)
	}

	// Something else asked on the same connection still answers.
	var st Status
	if err := c.Call("status", &st); err != nil {
		t.Fatalf("the connection stopped answering after subscribing: %v", err)
	}

	// And an event pushed from elsewhere lands on it. Asked for by another
	// connection, which is how it happens: the key is a different process.
	other, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// Shown is not asserted here: this client is a test, not a shell, so it
	// acknowledges nothing. What matters for this test is that the line arrives.
	var sw Switcher
	if err := other.Call("desk.switcher", &sw); err != nil {
		t.Fatal(err)
	}

	ev, err := c.NextEventBefore(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatalf("waiting for the event: %v", err)
	}
	if ev.Kind != EventPicker {
		t.Errorf("event kind = %q, want %q", ev.Kind, EventPicker)
	}
	if len(ev.Desks) != 2 {
		t.Errorf("event desks = %v", ev.Desks)
	}
}

// blocker is a listener that never reads: a write to it blocks until its
// deadline, which is what a frozen shell that has not closed its socket looks
// like from here.
type blocker struct {
	mu       sync.Mutex
	deadline time.Time
}

func (b *blocker) SetWriteDeadline(t time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deadline = t
	return nil
}

func (b *blocker) Write(p []byte) (int, error) {
	b.mu.Lock()
	d := b.deadline
	b.mu.Unlock()
	if d.IsZero() {
		// No deadline set: this is the bug the deadline exists to prevent, and
		// a test must not hang on it. Wait long enough to fail the timing
		// assertion instead.
		time.Sleep(3 * time.Second)
		return len(p), nil
	}
	time.Sleep(time.Until(d))
	return 0, errClosed
}

// A listener that has stopped reading must cost one send and then stop being a
// listener. Before the deadline it blocked for ever: measured, 276 events filled
// the socket and the next write never returned, so every later Mod+Tab waited
// five seconds, printed a timeout to a stderr nobody reads, and left a goroutine
// parked in Write.
func TestBroadcastDropsAListenerThatStoppedReading(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	deaf := &sink{w: &blocker{}}
	s.listen(deaf)

	start := time.Now()
	sent := s.broadcast(Event{Kind: EventPicker})
	took := time.Since(start)

	if sent != 0 {
		t.Errorf("a listener that never read took %d events", sent)
	}
	// Generous, and still far below the three seconds the unbounded write takes.
	if took > 2*time.Second {
		t.Errorf("broadcast waited %v on a listener that never reads", took)
	}
	if n := s.listeners(); n != 0 {
		t.Errorf("the stalled listener is still on the list: %d", n)
	}

	// And the next one is not slowed by it at all, which is the part that made
	// the key unusable rather than merely slow once.
	start = time.Now()
	s.broadcast(Event{Kind: EventPicker})
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Errorf("the second broadcast still waited %v", took)
	}
}

// `zde status` is what somebody runs when a key did nothing and there is no
// second machine to look anything up on. Whether a shell is listening is the
// whole diagnosis for a session where Mod+Tab prints a list instead of drawing
// a picker, so it has to be in there - and it has to be false when there is
// none, which is the answer that gets tested least and matters most.
func TestStatusSaysWhetherAShellIsListening(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)

	if st := s.status(); st.Shell {
		t.Error("nothing is listening and status says a shell is")
	}
	s.listen(&sink{w: &recorder{}})
	if st := s.status(); !st.Shell {
		t.Error("a shell is listening and status says none is")
	}
}

// The notification server is the other half of a quiet session: without the bus
// name, everything an app sends goes to whoever has it, or nowhere.
func TestStatusSaysWhetherNotificationsAreOurs(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	if st := s.status(); st.Notifications {
		t.Error("no notifier and status claims notifications are handled")
	}
	s.Watching(stubNotifier{})
	if st := s.status(); !st.Notifications {
		t.Error("a notifier is watching and status says notifications are not handled")
	}
}

type stubNotifier struct{}

func (stubNotifier) Dismissed(uint64)            {}
func (stubNotifier) Invoke(uint64, string) error { return nil }
