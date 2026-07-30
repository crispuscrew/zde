package zded

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// sendWait is how long a listener has to take a line before it stops being a
// listener. A healthy client on a unix socket takes it immediately; one that has
// stopped reading - a frozen shell that has not closed its socket - would
// otherwise block the write for ever, and with it every later broadcast, so
// pressing Mod+Tab would cost five seconds and a leaked goroutine each time
// until the session ended. Measured before this existed: 276 events filled the
// buffer and the 277th never returned.
const sendWait = 200 * time.Millisecond

// Events are how zded stops being a thing that only answers questions.
//
// Everything until now was request and reply, which is right for a key that
// wants something done and wrong for a surface that has to appear when a key is
// pressed. A shell cannot poll for that: the delay would be the delay between
// pressing Mod+Tab and seeing anything.
//
// So a client may ask to listen, and keep the connection. Events arrive on it
// as they happen, alongside the replies to anything else it asks on the same
// connection - told apart by the key, since an event line carries "event" where
// a reply carries "ok" or "error".
type Event struct {
	Kind string `json:"kind"`
	// The picker's contents, sent with the event rather than fetched after it:
	// zded already has them, and a surface that has to ask before it can draw
	// is a surface that appears in two steps.
	Desks []string `json:"desks,omitempty"`
	On    string   `json:"on,omitempty"`
	// Output is the monitor to appear on: the one being looked at. A picker on
	// every screen is not a picker.
	Output string `json:"output,omitempty"`
}

// EventPicker asks the shell to show the desk switcher.
const EventPicker = "picker"

// MethodEvents is the request that turns a connection into a listener. One
// constant because two places name it: the connection loop, which is where it is
// really handled, and the dispatcher, which knows the name so that anything
// checking the protocol from outside does not read it as a method zded lacks.
const MethodEvents = "events"

// sink is one connection, with the lock that keeps a reply and an event from
// interleaving halfway through a line.
type sink struct {
	mu sync.Mutex
	w  io.Writer
}

func (k *sink) reply(resp Response) {
	k.mu.Lock()
	defer k.mu.Unlock()
	writeResponse(k.w, resp)
}

func (k *sink) send(ev Event) error {
	line, err := json.Marshal(struct {
		Event Event `json:"event"`
	}{ev})
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	// Bounded, and cleared afterwards so the reply path is not left with a
	// deadline it never asked for.
	if d, ok := k.w.(interface{ SetWriteDeadline(time.Time) error }); ok {
		d.SetWriteDeadline(time.Now().Add(sendWait))
		defer d.SetWriteDeadline(time.Time{})
	}
	_, err = k.w.Write(append(line, '\n'))
	return err
}

// listen adds a connection to the listeners. Idempotent, so a client that asks
// twice is listening once.
func (s *Server) listen(k *sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = map[*sink]struct{}{}
	}
	s.subs[k] = struct{}{}
}

func (s *Server) unlisten(k *sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, k)
}

// broadcast sends an event to every listener and answers how many took it. The
// count is what lets a verb behave differently when nothing is listening: with
// no shell running, `zde desk switcher` has to fall back to printing a list
// rather than asking a surface that does not exist to appear.
//
// A listener whose write fails or stalls is dropped: a connection that keeps its
// place in the list is a slow leak and a broadcast that lies about its reach.
// Dropping a stalled one is also what keeps the answer useful - a shell that has
// stopped reading is a shell that will not draw, and the caller needs to hear
// that nothing was shown so it can print the list itself.
func (s *Server) broadcast(ev Event) int {
	s.mu.Lock()
	subs := make([]*sink, 0, len(s.subs))
	for k := range s.subs {
		subs = append(subs, k)
	}
	s.mu.Unlock()

	sent := 0
	for _, k := range subs {
		if err := k.send(ev); err != nil {
			s.unlisten(k)
			continue
		}
		sent++
	}
	return sent
}

// listeners is how many connections are listening. For tests, and for anything
// later that wants to know whether the shell is there.
func (s *Server) listeners() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Switcher is what `desk.switcher` answers: the desks, and whether a surface
// took the job of showing them.
type Switcher struct {
	// Shown is true when at least one listener took the line. That is a
	// listener accepting bytes, not a surface appearing: a shell that reads and
	// then fails to draw still counts here, and only an acknowledgement from the
	// shell could tell the difference. What it does catch is the shell that has
	// stopped reading at all, because that write is dropped (sendWait).
	//
	// False means nothing took it, and the caller prints the list itself - which
	// is what the key does on a machine with no shell running.
	Shown bool     `json:"shown"`
	Desks []string `json:"desks"`
	On    string   `json:"on,omitempty"`
}

// switcher opens the desk picker, or says that nothing could open it.
func (s *Server) switcher() Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	names := m.DeskNames()
	if len(names) == 0 {
		// A fresh login, before anything is named. Advice rather than a bare
		// refusal, because this is the one time somebody sees it.
		return Response{Error: "no desks yet: write a manifest, or open something and it will be adopted into one"}
	}
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		// Which screen is a detail; not knowing it is not worth refusing over,
		// and the shell falls back to the screen it can see.
		output = ""
	}
	on := s.activeDesk(m)
	sent := s.broadcast(Event{
		Kind:   EventPicker,
		Desks:  names,
		On:     on,
		Output: output,
	})
	return ok(Switcher{Shown: sent > 0, Desks: names, On: on})
}
