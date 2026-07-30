package zded

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

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
// them - and to say whether anybody was told, because the key has to do
// something on a machine with no shell running.
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
	if !sw.Shown {
		t.Error("something was listening and the answer says nothing was shown")
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
	var sw Switcher
	if err := other.Call("desk.switcher", &sw); err != nil {
		t.Fatal(err)
	}
	if !sw.Shown {
		t.Fatal("a connection was listening and desk.switcher says nothing was shown")
	}

	ev, err := c.NextEvent()
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
