package zded

import (
	"encoding/json"
	"io"
	"sync"
)

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
// A listener whose write fails is dropped: a dead connection that keeps its
// place in the list is a slow leak and a broadcast that lies about its reach.
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

// Switcher is what `desk.switcher` answers: the desks, and whether a surface
// took the job of showing them.
type Switcher struct {
	// Shown is true when at least one listener was told to open the picker. The
	// caller prints the list itself when it is false, which is what the key
	// does on a machine with no shell running.
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
		return Response{Error: "no desks exist yet"}
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
