package zded

import (
	"fmt"
	"strings"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/attn"
)

// panic: the decoy desk, mute, silence, one action (docs/glossary.md;
// docs/vision.md, section 3 and W8).
//
// The three are one act and they reach three different things. The decoy is
// what the screen shows - a desk declared like any other, and the only half
// that cannot be done without configuring one. Mute is the machine's output,
// which is the default sink and nothing finer (audio.go). Silence is quiet
// mode: nothing pops, nothing reaches the queue (internal/attn, Mode).
//
// What panic does not do is erase. Everything that arrived while it held is in
// the notification history afterwards, and the queue is as long as it was -
// that is principle 3, display policy and never data policy (docs/vision.md).
// panic takes a screen away from somebody standing behind you; it is not a way
// to unsay what happened, and the machine it hands to an adversary with your
// keyboard is a machine that is already lost. `lock` and `guest` are what that
// moment is for.
//
// One key both ways, like the kill switch, and for a sharper version of the
// same reason: a person who needs a second key to get the sound and the desk
// back has a key that costs them a session. What that must not mean is a key
// that reveals - see deskPanic.

// panicFile is layer 1's answer to which desk is the harmless one, beside its
// answers to what a terminal is and which desk an unlock shows (nix/home.nix).
// One directory for zde's config, one file per reader (config.go).
//
// Its own file rather than a line in lock.json, which is lock-scoped on purpose,
// and rather than a flag in a manifest. A manifest is the wrong place twice
// over: the decoy would be spread over as many files as there are desks, with
// nothing stopping two of them claiming it, and the manifests are the one thing
// that may be unreadable at the moment this is asked - which is the failure
// canShow already has to fail closed on (config.go).
const panicFile = "panic.json"

// panicConfig is that file.
type panicConfig struct {
	// Decoy is the desk panic switches to. Empty is a machine that has not set
	// one, and unlike the lock preset that is a refusal rather than a note:
	// lock-preset with no preset still locks the screen, which is what it is
	// for, and panic with no decoy has nothing to put in front of the person
	// who just walked in.
	Decoy string `json:"decoy"`
}

// panicHold is what panic took, so that the same key can give it back. In
// memory and never in the journal, deliberately: the journal outlives the
// session and a line saying you panicked at 14:32 is the one record that could
// be read afterwards by whoever you were hiding from. What does reach the
// journal is a desk switch and a mode, which is what an ordinary keypress
// writes there anyway.
type panicHold struct {
	// decoy is the desk panic switched to, kept here rather than re-read, so
	// that coming back needs nothing on disk to still be readable.
	decoy string
	// from is the desk to come back to, empty when nothing could say which one
	// it was.
	from string
	// mode is what was in force before the silence.
	mode string
	// took is whether the sound was actually muted, muted whether it was
	// already off when panic found it, and knewMuted whether that could be read
	// at all. Leaving gives back only what panic took: a sound it never took
	// stays as it is, somebody who muted for a meeting and then panicked must
	// not have it put back on for them, and a machine that could not say stays
	// quiet rather than being made to make a noise.
	took, muted, knewMuted bool
}

// UseSound says how to reach the machine's sound. Nothing does until this is
// called, which is what keeps a Server built by a test from muting the speakers
// of the machine it is running on (cmd/zded is where the real one is wired,
// beside the clipboard and the compositor).
func (s *Server) UseSound(a Sound) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sound = a
}

// sounder is who to ask, or nil when nothing can reach the sound. Read under
// the lock and used outside it, the way the clipboard and the notifier are:
// every call on it is a program, and holding s.mu across one would put every
// keybind behind whatever PipeWire is doing.
func (s *Server) sounder() Sound {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sound
}

func (s *Server) panicHeld() *panicHold {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.panicking
}

func (s *Server) holdPanic(h *panicHold) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panicking = h
}

// deskPanic is the key: hide, or come back.
//
// Which of the two it is comes from where you are standing and not only from
// what zded remembers, and that is the whole rule. A key that gave the work
// back whenever panic happened to be holding would put the hidden desk on the
// screen for somebody who had switched off the decoy by hand and pressed panic
// because somebody walked in - the one thing this key must never do. So it
// comes back only from the decoy itself, and from anywhere else it hides again.
//
// The cost of that rule is one confusing case and it is the safe one: panic
// pressed on the decoy when nothing is held mutes and silences a desk that was
// already harmless, and the same key undoes it.
func (s *Server) deskPanic() Response {
	if held := s.panicHeld(); held != nil && s.whereWeAre() == held.decoy {
		return s.leavePanic(*held)
	}
	return s.enterPanic()
}

// enterPanic hides.
//
// Everything that can refuse is asked before anything moves, which is what
// keeps a failure from leaving the machine in a state that is neither the work
// nor the decoy: no decoy set, a decoy that is private, manifests that cannot
// be read to say whether it is. A panic that had muted the sound and silenced
// the notifications and then found it had nowhere to switch to would be the
// worst answer here - the sound going off is what a person reads as "it
// worked", over a screen that still has their work on it.
//
// Then the order, and each step is where it is for a reason:
//
//   - The desk first. It is the half the action exists for, the only one that
//     can still fail once the checks have passed - niri may be unreachable -
//     and the only one nothing else can stand in for. Nothing after it is
//     started until it has happened, so a switch that fails leaves the sound on
//     and the notifications on: unchanged, and said so, rather than a machine
//     that looks panicked and is not.
//   - Silence before the sweep. Quiet stops the next popup; the sweep takes
//     away the ones already on the screen. In the other order, a notification
//     arriving between the two would pop straight back onto the decoy.
//   - The sound last, because it is the only one that has to run a program, and
//     the only one whose failure is a note rather than a stop. A machine with no
//     sound card - every VM - panics fine and says the sound was not touched.
func (s *Server) enterPanic() Response {
	var cfg panicConfig
	if err := readConfig(panicFile, &cfg); err != nil {
		return Response{Error: "nothing was hidden: " + err.Error()}
	}
	if cfg.Decoy == "" {
		// The option by name. "No decoy" leaves somebody reading three
		// documents; this is a line they can paste into a config.
		return Response{Error: fmt.Sprintf("nothing was hidden: no decoy desk is set, so set zde.panic.decoy "+
			"in your home-manager config, which is what writes %s", apps.Path(panicFile))}
	}
	if err := s.canShow(cfg.Decoy); err != nil {
		return Response{Error: "nothing was hidden: " + err.Error()}
	}
	// Read before anything moves, or the desk to come back to is the decoy.
	held := panicHold{decoy: cfg.Decoy, from: s.whereWeAre(), mode: string(s.mode())}
	if a := s.sounder(); a != nil {
		muted, err := a.Muted()
		held.muted, held.knewMuted = muted, err == nil
	}
	if resp := s.switchDesk(cfg.Decoy); resp.Error != "" {
		return Response{Error: "the decoy is not up, and nothing was muted or silenced either: " + attn.Line(resp.Error)}
	}
	note := ""
	if resp := s.setMode(string(attn.Quiet)); resp.Error != "" {
		note = "the notifications were not silenced: " + resp.Error
	}
	// And what is already on the screen goes with the desk it belongs to. A card
	// that is up is a popup that has already happened: the sender's name and the
	// summary of something that arrived on the desk this has just hidden, drawn
	// over the decoy for the rest of its five seconds. Nothing is forgotten -
	// the record is in the history, and the sweep is the display half only
	// (events.go, EventAttnHide).
	s.broadcast(Event{Kind: EventAttnHide})
	if err := s.setMuted(true); err != nil {
		note = joinNotes(note, "the sound was not muted: "+err.Error())
	} else {
		held.took = true
	}
	s.holdPanic(&held)
	resp := ok("panic: " + attn.Line(cfg.Decoy) + " is up, nothing will interrupt, and the same key comes back")
	resp.Note = note
	return resp
}

// leavePanic gives back what panic took: the sound, the mode, and the desk.
//
// The desk first, for the reason the way in switches first: it is the half that
// can fail, and the one a person is waiting to see. The sound and the mode
// follow it whether it worked or not - they are this session's and not that
// desk's, and a key that left them because a workspace had gone would be a
// machine that stays quiet with no reason on any surface.
//
// The mode last, and set rather than toggled. Mod+q coming out of quiet always
// goes to work, on the reasoning that a toggle remembering what came before
// answers one key with two modes (attn.go, toggleQuiet); this is the opposite
// case and wants the opposite rule. Panic is one act being undone rather than a
// mode being chosen, and somebody who was already in quiet when they pressed it
// - in a call, in a screencast - must not have the notifications turned on for
// them by the key that gives their desk back.
func (s *Server) leavePanic(held panicHold) Response {
	note := ""
	back := true
	if held.from != "" && held.from != held.decoy {
		if resp := s.switchDesk(held.from); resp.Error != "" {
			// Kept holding, so the key still means "get me back" and a second
			// press retries it. Dropping it here would make the next press panic
			// again, on a machine that is already on the decoy: the sound would
			// go off a second time and nothing would explain it.
			back = false
			note = "the desk you were on could not be brought back: " + attn.Line(resp.Error)
		}
	} else if held.from == "" {
		note = "nothing could say which desk you were on when panic went on, so you are still on the decoy"
	}
	switch {
	case !held.took:
		// Nothing was muted on the way in - a machine with no sound card, or a
		// wpctl that refused - and it was said then. Nothing to give back.
	case !held.knewMuted:
		note = joinNotes(note, "the sound was left off: nothing could say whether it was already muted when panic took it")
	case held.muted:
		// It was off before panic and it is somebody else's decision. Silent
		// about it: this is the key doing exactly what it says.
	default:
		if err := s.setMuted(false); err != nil {
			note = joinNotes(note, "the sound is still muted: "+err.Error())
		}
	}
	if resp := s.setMode(held.mode); resp.Error != "" {
		note = joinNotes(note, "the notifications were not turned back on: "+resp.Error)
	}
	if back {
		s.holdPanic(nil)
	}
	resp := ok("back")
	resp.Note = note
	return resp
}

// setMuted is the mute half, with the daemon that cannot reach any sound
// answered in words rather than by doing nothing.
func (s *Server) setMuted(on bool) error {
	a := s.sounder()
	if a == nil {
		return fmt.Errorf("nothing here can reach this machine's sound")
	}
	return a.Mute(on)
}

// joinNotes puts two of them in one line. A note is one line by construction
// (see Response.Note), and panic is the one action with two halves that can each
// half-fail.
func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return strings.Join([]string{a, b}, "; ")
}
