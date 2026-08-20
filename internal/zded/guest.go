package zded

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

// guest: one desk unlocked, everything else needs your password
// (docs/glossary.md; docs/vision.md, section 3 and A9).
//
// What it is honestly for, said first because the rest only makes sense after
// it. zde is not a login manager and this is not an authentication boundary.
// The one thing on this machine that checks a password is the screen locker,
// which is PAM's business and not zde's, so guest mode does not check one, does
// not hold one and does not hash one. What it does is refuse: while it holds,
// no key and no socket call this daemon answers will leave the desk you handed
// over, show what is in your clipboard history or your notification centre, or
// put the popups back on. And the way out is to run the machine's locker and
// wait for it to exit, which is the closest thing to "your password" that
// anything in zde can honestly say (see endGuest).
//
// What that does not stop, and it belongs here rather than only in the docs. A
// guest with a terminal is a guest with a shell: the desk restriction is
// zded's, and niri's own overview, a `niri msg` from that terminal, a
// screenshot or a tty are all outside it. Nothing here stops somebody reading
// the files of the account they are sitting at. It is the answer to "hold on, I
// will look it up on your laptop", which is what A9 asks for, and it is not the
// answer to somebody who means you harm - that one is `zde system lock` and a
// second account.

// guestFile is layer 1's answer to which desk gets handed over, beside its
// answers to what a terminal is, which desk an unlock shows and which desk
// panic hides behind. One directory for zde's config, one file per reader
// (config.go).
//
// A file rather than an argument after the verb, which is what docs/model.md
// registered this as for as long as it was unwritten. The guest desk is a desk
// prepared in advance for being handed over - a browser and nothing else - and
// deciding which one that is while somebody stands over your shoulder is how a
// desk with your work on it gets handed over instead. It is the same reasoning
// the decoy and the lock preset are configured by (panic.go, lock.go), and it
// is what lets the action be one row in the palette rather than a name no
// surface can offer.
const guestFile = "guest.json"

// guestConfig is that file.
type guestConfig struct {
	// Desk is the desk handed over. Empty is a machine that has not set one,
	// and that is a refusal for panic's reason rather than a note for
	// lock-preset's: guest with no desk has nothing to hand over, and a session
	// that silenced itself and restricted itself to the desk your work is on is
	// worse than a key that says it did nothing.
	Desk string `json:"desk"`
}

// guest is the session that is open, if one is. The journal is where it lives
// and there is no copy of it in this process, which is the one place this
// differs from panic: panic's hold is memory-only so that nothing on disk
// records that you pressed it, and a guest session forgotten by a zded restart
// is a machine that hands every desk back to whoever is sitting at it. A record
// saying a guest was here is a record worth having.
func (s *Server) guest() journal.Guest {
	if s.jrn == nil {
		return journal.Guest{}
	}
	return s.jrn.Guest()
}

// guestDesk is the desk a guest session is standing on, empty when none is
// open. It is the whole of "is guest mode holding", asked by everything that
// has to behave differently while it is.
func (s *Server) guestDesk() string { return s.guest().Desk }

// guestKeeps is the refusal every desk change goes through, and empty when the
// change is one a guest session allows.
//
// One gate, in switchFrom, rather than a check per verb. Ten actions reach a
// desk switch - a name, next, prev, last, the regulars, queue-jump, a window
// jump that lands on another desk, panic's decoy, lock-preset's preset, and the
// tail of carrying a window - and a rule written ten times is a rule with a
// hole in it the next time an eleventh is added.
//
// The two verbs that carry something to a desk are barred a step earlier, at
// the door, because they move the thing before they switch (server.go, carryTo)
// - refused here, they would leave a window on a desk this session cannot
// reach.
func (s *Server) guestKeeps(target string) string {
	on := s.guestDesk()
	if on == "" || target == on {
		return ""
	}
	return "guest mode is on, so this session stays on " + attn.Line(on) +
		": `zde desk guest` locks the screen, and the rest of your desks come back when you unlock"
}

// guestOnly cuts a list of desks down to the one this session will name.
//
// The refusal is the enforcement (see guestKeeps); this is the half a person
// sees. A picker that went on listing every desk would be offering rows that
// answer with a refusal, and reading your desk names out to whoever is
// borrowing the machine while it did - and a desk name here is a project name.
func (s *Server) guestOnly(names []string) []string {
	if on := s.guestDesk(); on != "" {
		return []string{on}
	}
	return names
}

// guestPins cuts the placement rules down to the guest desk's, and is the whole
// of "guest mode restricts launching to the guest desk" (docs/model.md, section
// 6).
//
// Launching is not the daemon's business: `zde app launch` resolves a name
// against apps.json and execs it without ever talking to zded (cmd/zde, launch).
// So the restriction cannot be a refusal, and it does not need to be one -
// adoption puts whatever you open on the desk you are standing on (invariant 3),
// and a guest session is standing on one desk. The one thing that would override
// that is a manifest pin, which is a niri window rule naming another desk's
// workspace by name (rules.go): niri would open the window there and take the
// session with it, past a gate that never sees the launch at all. So the rules
// that could do it are not in the file while a guest is at the keyboard, and
// adoption places those apps on the guest desk like everything else.
//
// A nil for a guest desk no manifest declares, which is most of them: the desk
// pins nothing, so there is nothing to keep.
func (s *Server) guestPins(all map[string]*manifest.Desk) map[string]*manifest.Desk {
	on := s.guestDesk()
	if on == "" {
		return all
	}
	if d := all[on]; d != nil {
		return map[string]*manifest.Desk{on: d}
	}
	return nil
}

// syncPins puts the placement rules back in step with the session and asks niri
// to read them now, which is what zen does with the same file and for the same
// reason: a rule that arrives at niri's next poll is a rule that was not there
// for the launch it was written for.
//
// Logged rather than returned. It is the belt beside the desk gate's braces, so
// a niri that will not reload costs a pinned app landing where its manifest
// says rather than the whole action.
func (s *Server) syncPins() {
	s.SyncRules()
	if err := s.niri.ReloadConfig(); err != nil {
		log.Printf("zded: niri would not reload for a guest session, so its placement rules arrive when niri next reads the file: %v", err)
	}
}

// guestBarred is what a guest session refuses outright: the surfaces that draw
// your records, and the verbs that spend them.
//
// Everything not in it is allowed on purpose, and the list is short because the
// desk restriction does most of the work: a guest may browse, launch, move
// windows, change the volume and join a network, which is what handing somebody
// your machine means. What they may not do is read your correspondence or
// finish it for you.
//
// The clipboard is here twice over. `clip.history` with no argument draws what
// you copied before they arrived, and with one it puts an entry back on the
// clipboard, which is the same leak with an extra step - so the method is
// barred rather than the surface, and the connection loop asks this too because
// that arity never reaches Dispatch (server.go, handle).
//
// The queue is not barred but emptied, which is display policy rather than data
// policy (docs/vision.md, principle 3): `queue.list` is what the bar asks for
// every two seconds, and a refusal there would be an error message on the strip
// for as long as the guest sits there. The items are untouched and come back
// whole. Finishing one or clearing the lot is barred, because that is the queue
// being spent rather than hidden.
var guestBarred = map[string]bool{
	"desk.move-window-to":    true,
	"desk.move-workspace-to": true,
	"desk.snapshot":          true,
	"clip.history":           true,
	"clip.clear":             true,
	"attn.center":            true,
	"attn.reach":             true,
	"attn.invoke":            true,
	"attn.quiet":             true,
	"queue.done":             true,
	"queue.clear":            true,
}

// guestRefuses is that list applied to one request, and empty when the request
// is one a guest session allows.
func (s *Server) guestRefuses(req Request) string {
	if s.guestDesk() == "" {
		return ""
	}
	barred := guestBarred[req.Method]
	if req.Method == "attn.mode" {
		// Reading the mode is the bar's question, asked on a clock. Setting one
		// is what would put the popups back on, so the two arities of one verb
		// are answered differently - which is the only place in this list that
		// has to look at an argument at all.
		barred = len(req.Args) > 0
	}
	if !barred {
		return ""
	}
	return req.Method + " is not something a guest session does: the machine is on " +
		attn.Line(s.guestDesk()) + " until `zde desk guest` locks the screen and you unlock it"
}

// deskGuest is the verb, and it is one verb both ways for the reason panic is
// one key both ways: a session you cannot get out of from the keyboard you are
// sitting at is a session lost. Which way it goes is read off whether one is
// open, and not off where anybody is standing - guest cannot borrow panic's
// rule there, because panic's way back is a place and this one is a password.
func (s *Server) deskGuest() Response {
	if s.guestDesk() != "" {
		return s.endGuest()
	}
	return s.startGuest()
}

// startGuest hands the machine over.
//
// Everything that can refuse is asked before anything moves, the way panic
// asks, and one of the questions is panic's opposite. panic never needs a way
// back that anybody else could refuse; this does, so whether this machine can
// lock its screen at all is checked on the way in and not only on the way out.
// A guest session entered on a machine with no locker is a session with no exit
// from the same keyboard, which is the one shape none of this set is allowed to
// have.
//
// Then the order, and it is panic's for panic's reason:
//
//   - The desk first. It is the half the action exists for and the only one
//     that can still fail once the checks have passed, so a switch that failed
//     leaves the session exactly as it was rather than silenced and restricted
//     over your own work.
//   - The record next, because from the moment it is written this is a guest
//     session: the desk gate, the barred verbs and the suspended clipboard are
//     all read off it, and nothing is true of this session until it is.
//   - Then the silence, and the sweep after it. Quiet stops the next popup; the
//     sweep takes away the ones already drawn, and in the other order an
//     arrival between the two would pop straight back onto the guest's screen
//     (events.go, EventAttnHide).
func (s *Server) startGuest() Response {
	if s.jrn == nil {
		return Response{Error: "nothing was handed over: no journal, so a guest session could not be written down - " +
			"and one that a zded restart forgets is a machine that gives every desk back to whoever is sitting at it"}
	}
	var cfg guestConfig
	if err := readConfig(guestFile, &cfg); err != nil {
		return Response{Error: "nothing was handed over: " + err.Error()}
	}
	if cfg.Desk == "" {
		// The option by name, which is a line somebody can paste into a config
		// rather than three documents to go and read.
		return Response{Error: fmt.Sprintf("nothing was handed over: no guest desk is set, so set zde.guest.desk "+
			"in your home-manager config, which is what writes %s", apps.Path(guestFile))}
	}
	// Whether this is a desk to put in front of somebody else, which is the
	// question lock-preset and panic ask of their own desks and fails closed the
	// same way (config.go, canShow). A private desk is the one desk on the
	// machine that must never be the one handed over.
	if err := s.canShow(cfg.Desk); err != nil {
		return Response{Error: "nothing was handed over: " + err.Error()}
	}
	if _, err := s.lockArgv(); err != nil {
		return Response{Error: "nothing was handed over: " + attn.Line(err.Error()) +
			", and the way out of a guest session is this machine asking for your password"}
	}
	// Read before anything moves, or the mode given back at the end is the
	// silence guest is about to impose.
	was := string(s.mode())
	if resp := s.switchDesk(cfg.Desk); resp.Error != "" {
		return Response{Error: "nothing was handed over: " + attn.Line(resp.Error)}
	}
	if err := s.jrn.SetGuest(journal.Guest{Desk: cfg.Desk, Mode: was}); err != nil {
		// The switch has happened and nothing else has. Said plainly, because
		// the machine is now showing the guest desk with none of the
		// restrictions on it, and somebody is standing there waiting for it.
		return Response{Error: "you are on " + attn.Line(cfg.Desk) + " and nothing is restricted, because the guest session " +
			"could not be written down: " + attn.Line(err.Error())}
	}
	note := ""
	if resp := s.setMode(string(attn.Quiet)); resp.Error != "" {
		note = "the notifications were not silenced: " + resp.Error
	}
	s.broadcast(Event{Kind: EventAttnHide})
	// And the pins that would open a window on another desk go out of niri's
	// config, so that a launch lands where the session is (see guestPins).
	s.syncPins()
	resp := ok("guest: " + attn.Line(cfg.Desk) + " is the only desk this session will show, the clipboard history is " +
		"suspended, and nothing will interrupt - `zde desk guest` locks the screen to end it")
	resp.Note = note
	return resp
}

// endGuest asks for the password, which means running this machine's locker and
// waiting for it to exit.
//
// This is the whole of "everything else needs your password" and it is worth
// being exact about what it is and is not. zde never sees a password: it starts
// the program `zde.apps.lock` names, which is the same program `zde system
// lock` and the power menu start, and that program is the one talking to PAM. A
// locker exits when it has let somebody back in, so its exit is the only signal
// on this machine that a password was accepted - and a locker that exits
// without one is a locker that would have been a broken lock screen anyway.
//
// The lock goes up before anything is given back, and the giving back happens
// on the far side of it. Ending the restriction first and locking second would
// be the fail-open version of this: a locker that would not start, and the
// desks are back with the guest still at the keyboard.
//
// The answer comes back now rather than when the locker exits. A guest session
// lasts as long as somebody borrows the machine, and a socket call that did not
// return until then would be a keypress parked against a reply nobody is
// reading (events.go, replyWait).
func (s *Server) endGuest() Response {
	argv, err := s.lockArgv()
	if err != nil {
		// The way out needs a locker, and the way in checked for one - so this
		// is a machine whose apps.json changed under a guest session. Nothing
		// is given back, because there is nothing to ask for a password with.
		return Response{Error: "guest mode is still on: " + attn.Line(err.Error()) +
			", and giving the desks back without asking for a password is not something this can do"}
	}
	if !s.lockingOut(true) {
		return Response{Error: "the screen is already locked and guest mode ends when you unlock it: " +
			"type your password, and your desks come back"}
	}
	if err := s.locker()(argv); err != nil {
		s.lockingOut(false)
		return Response{Error: "guest mode is still on: the screen would not lock, so nothing was given back: " +
			attn.Line(err.Error())}
	}
	return ok("locking: guest mode ends when you unlock, and your desks come back behind the lock screen")
}

// releaseGuest is what the locker exiting means: the guest session is over.
//
// It runs on the goroutine that waited for the locker and answers to nobody, so
// everything it cannot do is logged rather than returned. The order is the
// record first, for the reason setZen writes the journal first: the record is
// what every other part of this reads, and a mode put back before it would be a
// session with the popups on and the desks still refused.
//
// A journal that will not take the line leaves the session in guest mode, which
// is the safe half of that failure: the desks stay refused and the next press
// locks and tries again.
func (s *Server) releaseGuest() {
	g := s.guest()
	if g.Desk == "" {
		return
	}
	if err := s.jrn.SetGuest(journal.Guest{}); err != nil {
		log.Printf("zded: the guest session could not be closed, so this session is still on %s: %v", g.Desk, err)
		return
	}
	// The mode as it was before the guest arrived, set rather than toggled and
	// for panic's reason: somebody who was already in quiet when they handed the
	// machine over must not have the notifications turned on for them by the
	// unlock that gives their desks back.
	if g.Mode != "" {
		if resp := s.setMode(g.Mode); resp.Error != "" {
			log.Printf("zded: the guest session is over and the mode was not put back: %s", resp.Error)
		}
	}
	// And every desk's pins go back into niri's config (see guestPins).
	s.syncPins()
	log.Printf("zded: the guest session on %s is over", g.Desk)
}

// lockingOut claims the one lock a guest session may have out at a time, and
// says whether the claim was taken. Without it a second press would start a
// second locker over the first, and two lock screens on one session is a way to
// end up looking at the desktop with one of them still holding a grab.
func (s *Server) lockingOut(on bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on && s.guestLocking {
		return false
	}
	s.guestLocking = on
	return true
}

// locker is how the screen locker is run and waited for. A field for the reason
// launch and spawn are fields: what this starts is a program that holds the
// screen for as long as somebody is away from it, and a test must be able to
// drive both ends of that without locking the machine it is running on.
func (s *Server) locker() func(argv []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockAndWait == nil {
		return s.lockUntilUnlocked
	}
	return s.lockAndWait
}

// lockUntilUnlocked starts the locker and gives the desks back when it exits.
//
// It returns as soon as the locker has started, which is the only part the
// caller can be told about: a lock screen lasts as long as somebody is away
// from the machine.
//
// Not in s.runs and not under runCtx, which is the opposite of everything else
// this daemon starts and is deliberate twice over. A run under runCtx is killed
// by process group when zded stops, and killing this one takes the lock screen
// off a session somebody walked away from. And Close waits for what is in
// s.runs, so a locker in it would make every logout wait runStopWait and then
// complain about a program that is doing exactly what it should.
//
// What that costs is a zded restarted while the screen is locked: the goroutine
// below goes with it, so nothing releases the session and the guest mode
// written in the journal is still there at the next login. That is the failure
// in the safe direction - the desks stay refused, and the next press locks and
// asks again.
func (s *Server) lockUntilUnlocked(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		defer s.lockingOut(false)
		if err := cmd.Wait(); err != nil {
			// A locker that exited badly is one that never asked anybody for a
			// password - it could not open the screen, or it crashed - so the
			// desks stay where they are and the next press tries again.
			log.Printf("zded: %s ended without letting anybody in, so guest mode is still on: %v",
				strings.Join(argv, " "), err)
			return
		}
		s.releaseGuest()
	}()
	return nil
}
