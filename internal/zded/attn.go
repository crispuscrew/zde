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

// Reach is what `attn.reach` answers: whether a surface took the keyboard onto
// the newest popup. The same bargain as Switcher.Shown and Center.Shown - false
// means nothing drew it, or nothing was up to reach, and the caller says so
// rather than leaving a key looking broken.
type Reach struct {
	Reached bool `json:"reached"`
}

// popupBacklog is how many arrivals may be waiting to be drawn before the popup
// path starts leaving them out.
//
// The bound exists because notifications arrive at machine speed: a build bot
// can send a hundred in a minute, and the shell draws at human speed. It is the
// arrival path that must never wait, so the hand-off is a buffered channel and a
// full one drops the popup - never the record, which is already in the history
// and on the queue by the time this is reached (see Arrived). A popup nobody saw
// is a glance missed; a notification nobody kept is the thing zded exists to
// prevent (docs/vision.md, principle 3).
//
// Sixteen because an ordinary burst - a build finishing and three things
// reacting to it - must never be the thing that gets dropped, and because
// sixteen stale lines is all a wedged shell can make the daemon hold. What
// bounds the screen is a different number and lives where the screen is
// (shell/AttnPopup.qml, maxUp).
const popupBacklog = 16

// maybePop puts an arrival in front of the person, if two different silences
// both allow it. Called at the end of Arrived, after the record is kept and the
// queue has it, and never instead of either.
//
// Two guards and deliberately not one condition, because they are two different
// reasons for the same quiet and somebody reading this later must not fold them
// together. The mode is the session's choice, made with a keypress and changed
// with another one. A private desk is the desk's, declared in a manifest and
// true whatever mode you are in, which is why quiet mode is not a way to get a
// private desk and leaving quiet mode is not a way to lose one.
//
// Both are display and nothing else. The record is in the history and, where the
// mode let it, on the queue, whichever way these two answer: that is principle 3
// (docs/vision.md), and it is the same argument for both gates.
//
// The mode is passed in rather than read again: Arrived reads it once so that
// what was queued and what was shown are one session's answer, and a second
// reading here could land the other side of `zde attn quiet`.
func (s *Server) maybePop(rec attn.Record, mode attn.Mode) {
	// The session's answer: quiet shows nothing, focus shows what the sender
	// called urgent, work shows everything (internal/attn, Pops).
	if !mode.Pops(rec.Urgent) {
		return
	}
	// The desk's. A private desk is popups off, history only (docs/vision.md,
	// section 3), and until now this path read the mode and nothing else - so a
	// notification arriving while somebody stood on a desk they had declared
	// private put its sender, its summary and its body on the screen for five
	// seconds, which is the one thing that desk exists to prevent.
	if s.privateArrival(rec.Desk) {
		return
	}
	// Which screen, asked here rather than in the pump. The pump runs on its own
	// goroutine and the compositor client is one request at a time, so asking
	// there would put it beside every other question the daemon is answering.
	// Here it is one more round trip on a path that already makes one
	// (whereWeAre), and the answer is the screen that was being looked at when
	// the thing arrived, which is the honest one.
	//
	// Last of the three, so the two silences cost nothing: a machine in quiet
	// mode, and a private desk, ask niri nothing at all.
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	s.pop(Event{
		Kind:          EventAttnPopup,
		Notifications: []attn.Record{rec},
		Output:        output,
	})
}

// privateArrival says whether something arriving on this desk must stay in
// memory (docs/vision.md, section 3: private desks are popups off, history
// only, capture-blocked).
//
// A notification on a screen is a different thing from one in a daemon's
// memory: it is readable by whoever is in the room, and by whatever is
// recording it, which is the case a private desk is for - a screencast, or
// guest mode. A desk marked private is somebody saying that what arrives there
// is not to be left lying around, and here that means no card is drawn.
//
// The same helper answers the same question for the history snapshot on
// feat/attn-persist (#78), where "lying around" means a file that outlives the
// session. It is deliberately one function and not two, because two ways of
// asking whether a desk is private is two places for the answer to be wrong;
// whichever of the two branches lands second should keep one copy of this.
//
// Fail closed, which is principle 9, and it decides the three cases that are
// not a plain yes or no:
//
//   - the manifests cannot be read at all: we cannot tell which desk is
//     private, so nothing leaves the daemon.
//   - the desk is not declared, or nothing could say which desk it was (niri
//     unreadable, or a session where nothing is named yet): then it could have
//     been the private one, so it is refused whenever this machine declares a
//     private desk at all. On the ordinary machine, which declares none, there
//     is nothing to protect and it goes ahead.
//   - one of the manifests would not parse: the broken file could be the
//     private desk's, and a typo must not be how a private desk stops being
//     one. `zde status` names the file (see rememberProblems).
func (s *Server) privateArrival(deskName string) bool {
	all, problems, err := s.desks.All()
	if err != nil {
		return true
	}
	s.rememberProblems(problems)
	if d, declared := all[deskName]; declared && deskName != "" {
		return d.Private
	}
	for _, d := range all {
		if d.Private {
			return true
		}
	}
	return len(problems) > 0
}

// pop offers one arrival to whatever is drawing popups, and never waits for it.
//
// A channel and a goroutine rather than a broadcast from here, because this is
// called from the bus: an app calls Notify and blocks until it gets an id back,
// and broadcast waits up to sendWait on a listener that has stopped reading.
// Two hundred milliseconds of a shell's bad afternoon must not become two
// hundred milliseconds of every notify-send on the machine.
//
// The pump is started on the first popup and not in New, so a daemon that never
// receives a notification never starts one, and Close is what ends it.
func (s *Server) pop(ev Event) {
	s.startPump.Do(func() {
		s.popups = make(chan Event, popupBacklog)
		go s.pumpPopups(s.popups)
	})
	select {
	case s.popups <- ev:
	default:
		// Full: the shell is not keeping up, or is not reading at all. Not
		// logged - a flood that fills this is a flood that would fill the log
		// with one line each - and not waited on, which is the whole point.
	}
}

// pumpPopups writes what pop handed over, one at a time and in the order it
// arrived. One goroutine, so a hundred arrivals are a hundred lines on the
// socket in the order the person's day happened, rather than a hundred
// goroutines racing to write into the same connection.
func (s *Server) pumpPopups(in <-chan Event) {
	for {
		select {
		case <-s.popupStop:
			return
		case ev := <-in:
			s.broadcast(ev)
		}
	}
}

// reach puts the keyboard on the newest popup: the deliberate key, and the only
// way a popup ever holds it (events.go, EventAttnReach).
//
// The same shape as the switcher and the center, deliberately: a key asks, zded
// tells whoever is listening, and the answer says whether anything came of it.
// Nothing was up to reach and no shell is running are the same answer here -
// both mean the keys stayed where they were - and the caller has one sentence
// for both, because from a person's side they are one fact.
func (s *Server) reach() Response {
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		// Which screen is a detail, and not knowing it is not worth refusing
		// over: the shell falls back to the screen it can see.
		output = ""
	}
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	sent := s.broadcast(Event{Kind: EventAttnReach, Output: output, Token: token})
	if sent == 0 {
		return ok(Reach{Reached: false})
	}
	select {
	case <-acked:
		return ok(Reach{Reached: true})
	case <-time.After(ackWait):
		return ok(Reach{Reached: false})
	}
}

// mode is the mode the session is in. Never an error: an unreadable or unknown
// mode is a session in the default, and refusing to answer would take the queue
// down with the answer (see Arrived).
func (s *Server) mode() attn.Mode {
	if s.jrn == nil {
		return attn.Work
	}
	// The mode alone (internal/journal, Mode). The bar asks for this on a clock,
	// and a copy of every desk position the session has accumulated is a strange
	// price for one word.
	m, err := attn.ParseMode(s.jrn.Mode())
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
	w := s.watcher()
	if w == nil {
		return Response{Error: "zded is not the notification server on this session, so there is nobody to tell"}
	}
	if err := w.Invoke(n, key); err != nil {
		return Response{Error: err.Error()}
	}
	return ok([]string{})
}
