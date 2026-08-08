package attn

import "fmt"

// Mode is what the session is currently doing about the things that arrive
// (docs/roadmap.md, 0.1: work/focus/quiet modes).
//
// The three differ in exactly one way: whether an arrival reaches the queue.
// None of them decides whether it is recorded, because that is principle 3 -
// display policy, never data policy (docs/vision.md). A mode that dropped a
// notification would be a mode that decides what happened, and the whole
// reason zde is the notification server rather than a popup daemon is that
// what arrived while you were busy is worth as much afterwards.
type Mode string

const (
	// Work is the default and the one nobody has to choose: everything that
	// arrives waits for you on the desk it arrived on.
	Work Mode = "work"
	// Focus lets the emergencies through and keeps the rest out of the way.
	// What counts as an emergency is the sender's own urgency hint, which is a
	// claim like the app name is (see Notification.From): this is a mode that
	// works because most apps are honest about urgency, not because anything
	// checks. An app that lies about it is an app to fix, and until channel
	// attribution lands (vision.md, ask 4) there is nothing else to go on.
	Focus Mode = "focus"
	// Quiet is nothing reaching the queue at all, the urgent included. It is
	// the mode for a screencast, a call, and a night's sleep, and the reason it
	// is not merely a stricter focus is that somebody in it has decided being
	// interrupted is worse than being late.
	Quiet Mode = "quiet"
)

// ParseMode reads a mode name. Empty is work, so a journal written before modes
// existed - or one that has never been told - replays into the default rather
// than into an error.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "":
		return Work, nil
	case Work, Focus, Quiet:
		return Mode(s), nil
	}
	return Work, fmt.Errorf("no such mode %q: it is work, focus or quiet", s)
}

// Queues reports whether an arrival of this urgency reaches the queue.
//
// The whole policy is one function below, so that the daemon, the CLI and the
// bar cannot each grow their own idea of what a mode does - the bug that shape
// produces is a bar saying "quiet" over a queue that is still filling.
func (m Mode) Queues(urgent bool) bool { return m.lets(urgent) }

// Pops reports whether an arrival of this urgency is put in front of a person
// the moment it lands, rather than waiting to be looked for.
//
// A second name for the same predicate rather than a second switch beside it.
// They are two decisions - what is still owed, and what interrupts - and today
// they have one answer each: quiet shows nothing, focus shows what the sender
// called urgent, work shows everything. The day a desk carries its own attn
// policy (docs/roadmap.md, 0.1) is the day they can differ, and a copied switch
// would have drifted before then, which is the bug this file's header is about.
//
// Neither of them decides what is recorded. A popup is display and nothing else:
// what arrived is in the history and, where the queue took it, on the queue,
// whatever this answers. That is principle 3 (docs/vision.md) - a mode that
// changed what is kept would be a mode that decides what happened.
func (m Mode) Pops(urgent bool) bool { return m.lets(urgent) }

// lets is the one rule both of them read.
//
// Anything that is not a mode this file knows lets it through, which is
// deliberate: the failure this could have is a hand-edited journal or an entry
// from a newer zde, and being interrupted by something you would rather not see
// beats never hearing about the thing you were waiting for.
func (m Mode) lets(urgent bool) bool {
	switch m {
	case Quiet:
		return false
	case Focus:
		return urgent
	default:
		return true
	}
}
