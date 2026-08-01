package zded

import (
	"encoding/json"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/link"
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
	// Windows is the other picker's rows. One event type with two possible
	// payloads rather than two, because everything else about them is the same
	// - the screen, the token, the surface that draws it - and a shell that
	// learned to listen for one has already learned the other.
	Windows []Window `json:"windows,omitempty"`
	// Notifications is the notification center's rows, newest first. A third
	// payload on the same event for the same reason the second one is here: the
	// screen, the token and the acknowledgement are the same for every surface
	// zded asks for, and a shell that learned one has learned this one.
	Notifications []attn.Record `json:"notifications,omitempty"`
	// The connections surface's two: the wifi networks it lists, and the link
	// as it stands, so it can say what you are on without asking a second
	// question (net.go).
	Networks []link.Network `json:"networks,omitempty"`
	Link     *link.Status   `json:"link,omitempty"`
	On       string         `json:"on,omitempty"`
	// Output is the monitor to appear on: the one being looked at. A picker on
	// every screen is not a picker.
	Output string `json:"output,omitempty"`
	// Token is what the shell sends back once the surface is up. It is how the
	// asker learns that something was actually shown rather than merely
	// written to (see Switcher.Shown).
	Token string `json:"token,omitempty"`
}

// EventPicker asks the shell to show the desk switcher.
const EventPicker = "picker"

// EventWindows asks it to show the open windows instead. A kind of its own and
// not a flag on the picker event: a shell that has never heard of it ignores
// the line rather than drawing a desk picker with no desks in it.
const EventWindows = "windows"

// EventCenter asks it to show the notification center: what arrived, whether
// the mode let it through, and what became of it.
const EventCenter = "notif-center"

// MethodShown is how a listener says it did the thing: the token from the
// event it acted on. Unsolicited ones are ignored, so this cannot be used to
// make a key report success that never happened.
const MethodShown = "shown"

// ackWait is how long the asker waits for that. A shell on the same machine
// answers in about a millisecond, so this is invisible when everything works
// and is the whole cost when the shell is wedged - at which point the key falls
// back to printing, which is the point.
const ackWait = 200 * time.Millisecond

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

// await registers a token and returns the channel that closes when somebody
// acknowledges it.
func (s *Server) await(token string) chan struct{} {
	ch := make(chan struct{})
	s.mu.Lock()
	if s.waiting == nil {
		s.waiting = map[string]chan struct{}{}
	}
	s.waiting[token] = ch
	s.mu.Unlock()
	return ch
}

func (s *Server) stopAwaiting(token string) {
	s.mu.Lock()
	delete(s.waiting, token)
	s.mu.Unlock()
}

// acknowledge resolves a token. Unknown ones are ignored rather than refused:
// an event whose asker has already given up is not an error, it is late.
func (s *Server) acknowledge(token string) Response {
	s.mu.Lock()
	ch, known := s.waiting[token]
	if known {
		delete(s.waiting, token)
	}
	s.mu.Unlock()
	if known {
		close(ch)
	}
	return ok("thanks")
}

// nextToken is a counter and not a random string: it never leaves this machine,
// and one that reads 7 is one a person can follow in a log.
func (s *Server) nextToken() string {
	s.mu.Lock()
	s.tokens++
	n := s.tokens
	s.mu.Unlock()
	return strconv.FormatUint(n, 10)
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
	// Shown is true when a listener said it put the surface up, within ackWait.
	// Not "the bytes were accepted": a shell can read a socket while failing to
	// draw anything - frozen, or stuck on a frame - and that used to count,
	// which made the key do nothing at all and suppress the printed list too.
	//
	// False means nothing showed it in time, and the caller prints the list
	// itself. That is what the key does with no shell running, with a wedged
	// one, and with one that is merely slower than ackWait - the last of which
	// prints a list and then shows a picker, which is untidy and better than
	// silence.
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
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	sent := s.broadcast(Event{
		Kind:   EventPicker,
		Desks:  names,
		On:     on,
		Output: output,
		Token:  token,
	})
	if sent == 0 {
		return ok(Switcher{Shown: false, Desks: names, On: on})
	}
	// Somebody took the bytes; now find out whether anything came of them.
	select {
	case <-acked:
		return ok(Switcher{Shown: true, Desks: names, On: on})
	case <-time.After(ackWait):
		return ok(Switcher{Shown: false, Desks: names, On: on})
	}
}
