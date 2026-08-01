package zded

import (
	"strconv"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
)

// The attn half of the daemon: the verbs that read and change the mode, and the
// surface that shows what arrived (docs/vision.md, section 2 - zded holds the
// notification history, and the shell is a thin adapter over it).
//
// What a mode means and what a history is are internal/attn's: that package is
// the policy and the record. What is here is the session's use of them - which
// mode this session is in, which is journal state, and which desk was in front
// of you when something arrived, which only the daemon knows.

// Attn is what the mode verbs answer. An object rather than a bare string,
// because the bar reads its replies off a connection that also carries events
// and the replies to desk switches - and a shape it can recognise is what stops
// it from putting a workspace name where the mode goes.
type Attn struct {
	Mode string `json:"mode"`
}

// Center is what `attn.center` answers: whether a surface took the job of
// showing the history, and the history itself for the caller that has to print
// it because nothing did.
type Center struct {
	// Shown means a listener said the surface is up, within ackWait. The same
	// bargain as the picker's (see Switcher.Shown), and for the same reason:
	// a shell that took the bytes and drew nothing must not silence the print.
	Shown bool `json:"shown"`
	// Notifications is newest first, which is the order the question is asked
	// in. Sent even when a surface has it, because a caller that timed out
	// waiting still has something to print.
	Notifications []attn.Record `json:"notifications"`
}

// mode is the mode the session is in. Never an error: an unreadable or unknown
// mode is a session in the default, and refusing to answer would take the queue
// down with the answer (see Arrived).
func (s *Server) mode() attn.Mode {
	if s.jrn == nil {
		return attn.Work
	}
	m, err := attn.ParseMode(s.jrn.State().Mode)
	if err != nil {
		// A journal edited by hand, or one written by a newer zde with a mode
		// this one has never heard of. Not logged: the bar asks this every two
		// seconds, so a line here would be a log filling up at the speed of a
		// clock. The mode is the default and `zde attn work` writes over
		// whatever the file says.
		return attn.Work
	}
	return m
}

// setMode changes what arrivals are allowed to do, and answers with the mode
// that is now in force - the same answer as reading it, so a keybind and a
// person get the same line back.
func (s *Server) setMode(name string) Response {
	// Empty is a mode to replay, not one to ask for. ParseMode reads it as work
	// so that a journal which has never been told comes back in the default,
	// and that same function is what checks what a person typed - so
	// `zde attn "$MODE"` with MODE unset turned the notifications back on and
	// said "work" as though it had been asked to. Every other way to get this
	// wrong is refused; this was the one direction where the accident is
	// expensive.
	if name == "" {
		return Response{Error: "no mode given: it is work, focus or quiet"}
	}
	m, err := attn.ParseMode(name)
	if err != nil {
		return Response{Error: err.Error()}
	}
	if s.jrn == nil {
		return Response{Error: "no journal, so a mode could not be remembered - and a mode that is forgotten is worse than none"}
	}
	if err := s.jrn.SetMode(string(m)); err != nil {
		return Response{Error: err.Error()}
	}
	return ok(Attn{Mode: string(m)})
}

// quiet is Mod+q: into quiet, or back out of it.
//
// Out of it is always into work, including from focus. A toggle that remembered
// what you were in before would answer the same key with two different modes
// depending on a history nobody can see - and the key exists for the moment
// somebody needs silence now and wants it gone afterwards.
func (s *Server) toggleQuiet() Response {
	if s.mode() == attn.Quiet {
		return s.setMode(string(attn.Work))
	}
	return s.setMode(string(attn.Quiet))
}

// center opens the notification center, or says that nothing could open it.
//
// The same shape as the desk switcher (see switcher), deliberately: a key asks,
// zded tells whoever is listening, and the caller prints the list itself when
// nothing did. A surface that is not running has to degrade to text, or Mod+n
// on a session whose shell has died is a key that does nothing at all.
func (s *Server) center() Response {
	seen := s.history.Recent()
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		// Which screen is a detail, and not knowing it is not worth refusing
		// over: the shell falls back to the screen it can see.
		output = ""
	}
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	sent := s.broadcast(Event{
		Kind:          EventCenter,
		Notifications: seen,
		Output:        output,
		Token:         token,
	})
	if sent == 0 {
		return ok(Center{Shown: false, Notifications: seen})
	}
	select {
	case <-acked:
		return ok(Center{Shown: true, Notifications: seen})
	case <-time.After(ackWait):
		return ok(Center{Shown: false, Notifications: seen})
	}
}

// invoke presses one of a notification's actions: the id from the history, and
// the key its sender declared for that action.
//
// The key is checked against that notification's own list rather than passed
// through. A surface is on the other end of this socket, and a key nobody
// declared would arrive at the app as an action it never offered - which is not
// something zde should be able to do on anybody's behalf, however it got asked.
//
// Every way this cannot work is a sentence rather than a silence, because the
// surface shows what comes back and somebody who pressed a key is owed an
// answer about it.
func (s *Server) invoke(id, key string) Response {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return Response{Error: "attn.invoke wants the id from the history, not " + strconv.Quote(id)}
	}
	rec, found := s.history.Find(n)
	if !found {
		return Response{Error: "nothing in the history has id " + id + ", so there is nothing to act on"}
	}
	if len(rec.Actions) == 0 {
		return Response{Error: strconv.Quote(rec.Text) + " came with no actions: its app sent a notification, not a button"}
	}
	if !rec.Allows(key) {
		return Response{Error: strconv.Quote(rec.Text) + " never offered " + strconv.Quote(key)}
	}
	if s.notifier == nil {
		return Response{Error: "zded is not the notification server on this session, so there is nobody to tell"}
	}
	if err := s.notifier.Invoke(n, key); err != nil {
		return Response{Error: err.Error()}
	}
	return ok([]string{})
}
