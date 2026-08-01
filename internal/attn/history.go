package attn

import (
	"sync"
	"time"
)

// HistoryMax is how many arrivals zded keeps.
//
// Bounded because notifications arrive at machine speed and not at human speed:
// one chat is a hundred a day and a build bot can send a hundred in a minute, so
// an unbounded list is a leak in a daemon that is meant to run for weeks. Two
// hundred is a day or two of ordinary use, which is the span "what did I miss"
// actually asks about (docs/vision.md, ask A5) - and at the length one record
// can reach it is about a quarter of a megabyte at worst.
//
// The queue is the part that outlives a restart, because it is what you still
// owe. This is what already happened, and it is in memory only: writing every
// arrival to the journal would fsync a line per notification (internal/journal,
// recordLocked) for the sake of history that is stale by the next login. So a
// zded restart forgets what arrived and remembers what is waiting, which is the
// half worth keeping.
const HistoryMax = 200

// Record is one arrival, as the notification center reads it back. It is what
// the queue item cannot say on its own: when it happened, and whether the mode
// let it through (docs/vision.md, principle 3 - everything lands in history,
// whatever the mode did about showing it).
type Record struct {
	// ID is the number everything addresses this by: the app on the bus, the
	// center dismissing it, the queue if it reached the queue. It comes from
	// the journal even when nothing was queued, so an id is never reused
	// (internal/journal, ClaimID).
	ID uint64 `json:"id"`
	// From is the sender's claim about itself, unverified - see Notification.
	From string `json:"from,omitempty"`
	Text string `json:"text"`
	Body string `json:"body,omitempty"`
	// Urgent is the sender's claim that this should interrupt. It is also what
	// focus mode reads to decide.
	Urgent bool `json:"urgent,omitempty"`
	// At is when it arrived, which the queue does not keep and the center
	// cannot do without: "what did I miss" is a question about time.
	At time.Time `json:"at"`
	// Desk is where the session was when it arrived.
	Desk string `json:"desk,omitempty"`
	// Queued says whether it reached the queue. False means a mode kept it out,
	// and that is the difference a person has to be able to see: a history that
	// did not say so would make quiet mode look like an app that stopped
	// sending.
	Queued bool `json:"queued,omitempty"`
	// Dismissed says it is not waiting any more - finished by a person, or
	// taken back by the app that sent it.
	Dismissed bool `json:"dismissed,omitempty"`
	// Actions is what its sender says can be done about it, in the order it
	// declared them. The center offers every one of them, which is what makes
	// the "actions" capability an honest claim (see GetCapabilities).
	Actions []Action `json:"actions,omitempty"`
	// Extra is how many more were declared than are kept here, so the surface
	// can say some are out of its reach rather than showing a list that quietly
	// stops (see actionsMax).
	Extra int `json:"moreActions,omitempty"`
}

// Allows reports whether this notification declared that action.
//
// The center sends a key back over the socket, and a key nobody declared is one
// an app would be told was pressed without ever having offered it. Checked here
// rather than on the bus side, because the list is the record's.
func (r Record) Allows(key string) bool {
	for _, a := range r.Actions {
		if a.Key == key {
			return true
		}
	}
	return false
}

// History is what arrived, bounded at HistoryMax and oldest first. Safe for
// concurrent use: arrivals come off the bus and the center asks over the
// socket, on different goroutines.
type History struct {
	mu      sync.Mutex
	records []Record
}

// Add records an arrival and answers with the id of the record it pushed out,
// or zero when it pushed out nothing.
//
// The id is answered rather than dropped on the floor because a record leaving
// here is the moment nothing can address that notification any more: it cannot
// be dismissed, invoked or listed, so whatever else is holding state about it
// should let go too (internal/attn, Forget).
func (h *History) Add(r Record) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	if len(h.records) <= HistoryMax {
		return 0
	}
	gone := h.records[0].ID
	// Copied down rather than resliced from the front: a reslice leaves the
	// backing array growing to the right for ever, which is the leak this bound
	// exists to stop, only slower.
	copy(h.records, h.records[1:])
	h.records = h.records[:HistoryMax]
	return gone
}

// Recent is what arrived, newest first, as a copy. Newest first because that is
// the order the center reads in and the order the question is asked in: what
// happened while I was away, most recent thing first.
func (h *History) Recent() []Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Record, 0, len(h.records))
	for i := len(h.records) - 1; i >= 0; i-- {
		out = append(out, h.records[i])
	}
	return out
}

// Find is one record by id, for a caller that has to know what it is acting on
// before it acts.
func (h *History) Find(id uint64) (Record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.ID == id {
			return r, true
		}
	}
	return Record{}, false
}

// Dismiss marks one as no longer waiting, and says whether it found it. An id
// that has fallen off the end of the history is not an error: the thing is not
// waiting either way, which is what was asked for.
func (h *History) Dismiss(id uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.records {
		if h.records[i].ID == id {
			h.records[i].Dismissed = true
			return true
		}
	}
	return false
}
