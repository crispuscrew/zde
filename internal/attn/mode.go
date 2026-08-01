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
// The whole policy, in one function, so that the daemon, the CLI and the bar
// cannot each grow their own idea of what a mode does - the bug that shape
// produces is a bar saying "quiet" over a queue that is still filling.
//
// Anything that is not a mode this file knows queues, which is deliberate: the
// failure this could have is a hand-edited journal or an entry from a newer zde,
// and being interrupted by something you would rather not see beats never
// hearing about the thing you were waiting for.
func (m Mode) Queues(urgent bool) bool {
	switch m {
	case Quiet:
		return false
	case Focus:
		return urgent
	default:
		return true
	}
}
