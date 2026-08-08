package zded

import (
	"log"
	"strconv"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
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
	// And the desk's loan ends here, if one was open. A mode chosen by hand is
	// the session's again: it goes with you when you leave the desk that lent
	// one, and that desk takes the mode back the next time you enter it, which
	// is a moment somebody can see happening.
	//
	// Giving it back on the way out instead - keeping the loan and restoring it
	// when you leave - would make Mod+q mean two different things depending on
	// which desk you were standing on when you pressed it: silence that follows
	// you off an ordinary desk, and silence that ends the moment you switch
	// away from a declaring one. The second is the one that ends a call badly.
	if s.jrn.Borrowed().Desk != "" {
		if err := s.jrn.SetBorrowed(journal.Borrowed{}); err != nil {
			return Response{Error: err.Error()}
		}
	}
	return ok(Attn{Mode: string(m)})
}

// enterDesk puts the session in the mode the desk being entered declares, and
// gives back the mode the desk being left had borrowed (docs/model.md, section
// 5: policies.attn).
//
// Borrowed, not taken. Entering writes down the mode that was in force and
// entering anywhere else puts it back, because the alternative - a desk that
// simply sets the mode - is a session sitting in whatever the last declaring
// desk asked for hours after leaving it, with nothing anywhere saying why. A
// desk's policy is a display policy for that desk (docs/vision.md, section 2).
//
// Two things are deliberately not entries here, and each is a decision:
//
//   - A switch that failed partway is not a desk you are on, so this is called
//     only after the journal has recorded the switch (see switchFrom). The mode
//     then still belongs to the desk you are still looking at, rather than to
//     the one whose workspaces are half up. Retrying the switch is what applies
//     it, which is also what fixes the screens.
//   - The first desk after login is only an entry if it is a different desk
//     from the one you logged out on, because re-entering the desk you are
//     standing on is not entering (that guard is switchFrom's, and it is what
//     stops half the nav keys from undoing a mode set by hand a keypress after
//     it was set). Coming back to where you left off is therefore the mode you
//     left off in - which is what the journal keeps a mode for - and the loan
//     comes back with it, so leaving that desk still gives the mode back. There
//     is nothing to re-apply and no second answer about what was in force
//     before: the record already holds it.
func (s *Server) enterDesk(target string) {
	if s.jrn == nil {
		return // nothing to write a mode into, so nothing can be given back
	}
	was := s.mode()
	now := was
	held := s.jrn.Borrowed()
	if held.Desk != "" && held.Desk != target {
		// Leaving the borrower. What it displaced comes back before the desk
		// being entered has its say, so that a switch between two declaring
		// desks writes down the session's own mode rather than the previous
		// desk's - which would make the mode of one desk outlive two more.
		//
		// A name this zde does not know replays as work, the way the mode
		// itself does (see mode): an unreadable record is not a reason to leave
		// a desk's mode behind on the desk you walked to.
		back, _ := attn.ParseMode(held.Mode)
		now = back
		held = journal.Borrowed{}
	}
	if declared, declares := s.declaredMode(target); declares {
		if held.Desk != target {
			// What this desk displaces is the mode in force now. Re-entering
			// the desk that already holds the loan keeps the record it has:
			// overwriting it with the mode that desk itself set would make
			// giving it back a no-op, and you get there for real by leaving a
			// desk through a switch that failed and then coming back.
			held = journal.Borrowed{Desk: target, Mode: string(now)}
		}
		now = declared
	}
	// Written only where something changed. Every line here is an fsync and a
	// thousand of them is a compaction, and a nav key that re-enters a desk
	// declaring nothing has nothing to say.
	if held != s.jrn.Borrowed() {
		if err := s.jrn.SetBorrowed(held); err != nil {
			log.Printf("zded: remembering which desk lent the mode: %v", err)
		}
	}
	if now != was {
		if err := s.jrn.SetMode(string(now)); err != nil {
			log.Printf("zded: entering %s in %s: %v", target, now, err)
		}
	}
}

// declaredMode is the mode a desk's manifest declares, and whether it declares
// one at all.
//
// The second answer is the point. Empty is not work: a desk that says nothing
// about attn leaves the session in whatever mode it is in, and reading empty as
// work would make every undeclared desk an instruction to turn the
// notifications back on - in the middle of a call, on the way to a terminal.
func (s *Server) declaredMode(target string) (attn.Mode, bool) {
	d := s.manifestFor(target)
	if d == nil || d.Policies.Attn == "" {
		return "", false
	}
	m, err := attn.ParseMode(d.Policies.Attn)
	if err != nil {
		// The manifest layer refuses this at parse, so a desk only reaches here
		// with an unreadable mode if it came from a newer zde. Logged and left
		// alone: this is the daemon, and the mode it would otherwise guess at
		// decides whether the person hears anything for the rest of the day.
		log.Printf("zded: desk %s declares attn %q, which this zde cannot read: %v", target, d.Policies.Attn, err)
		return "", false
	}
	return m, true
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
	if rec.Restored {
		// Its own refusal, and the reason is not the one below. A restored
		// record has no actions because the snapshot does not keep them
		// (internal/attn, Snapshot), so saying its sender declared none would
		// be blaming an app for what a restart did - and even where it did
		// declare some, the connection that offered them ended with the last
		// session.
		return Response{Error: strconv.Quote(rec.Text) + " arrived before this session started, so whatever it offered went with the app that sent it"}
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
