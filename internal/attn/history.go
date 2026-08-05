package attn

import (
	"sync"
	"time"
)

// HistoryMax is how many arrivals zded keeps in memory.
//
// Bounded because notifications arrive at machine speed and not at human speed:
// one chat is a hundred a day and a build bot can send a hundred in a minute, so
// an unbounded list is a leak in a daemon that is meant to run for weeks. Two
// hundred is a day or two of ordinary use, which is the span "what did I miss"
// actually asks about (docs/vision.md, ask A5) - and with the body bounded at
// bodyMax it is about 3 MB if every record is at its limit, a few hundred
// kilobytes in a real session.
//
// All two hundred are this session's. What outlives the daemon is two smaller
// things: the queue, because it is what you still owe, and a snapshot of the
// newest snapshotMax records with their bodies cut to snapshotBodyMax
// (snapshot.go). So a restart costs the tail of the ring and most of the long
// bodies, and what it keeps is enough to answer "what did I miss" across a
// reboot instead of starting every session blank.
//
// The journal is still not where that lives, and the argument is about the
// shape of the two files rather than about what is in them today. Every arrival
// writes a journal line either way, a queued one to record the item and a
// silenced one to spend its id (internal/journal, ClaimID). A journal line is
// appended and then replayed, and it is rewritten only when a daemon starts and
// finds a long one - `Compact` has no other caller in the tree - so everything
// written per arrival is carried until the next login, fsynced, whether or not
// anybody will ever read it. The snapshot is the other shape: bounded at
// snapshotMax records with their bodies cut to snapshotBodyMax, written whole
// and renamed over the old one, so it costs the same whether it is written once
// or every two minutes, and a hundred arrivals a minute do not make it larger.
//
// What this comment used to claim, and what is not true, is that the journal
// does not carry bodies. It carries the whole body of everything the mode
// queues, fsynced, and has since long before this branch (internal/journal,
// Queue) - and the queue it replays into is not bounded the way this ring is.
// So the shape argument above is the reason the history goes in a file of its
// own; it is not a reason to believe a notification body is only ever in one
// place on the disk. It is not, the queue's copy is a separate change, and
// stating it the other way here was the comment flattering the code.
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
	// Text is the summary, on one line: it is what the queue and the row show.
	Text string `json:"text"`
	// Body is the rest of the message with its line breaks, up to bodyMax
	// characters. It is the part the center shows under the row you are on, and
	// the reason a notification is worth keeping rather than counting.
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
	// Restored says this came back from the snapshot rather than arriving in
	// this session (snapshot.go). On the wire because a surface must not draw
	// it as though it were live: its actions were not kept, and the connection
	// that sent it is not on the bus any more, so there is nothing on the other
	// end of pressing one.
	Restored bool `json:"restored,omitempty"`
	// Clipped says the body here is the front of what arrived and not the whole
	// of it, cut to snapshotBodyMax on its way to the file. A row that was cut
	// and did not say so would be the one place zde shows less than the app sent
	// without admitting it (docs/vision.md, principle 3).
	Clipped bool `json:"bodyClipped,omitempty"`
	// Private says this arrived on a desk whose manifest declares private, and
	// it is what every path that would put a notification anywhere but this
	// session's own memory has to check first (docs/vision.md, section 3 -
	// private desks are history only).
	//
	// Decided once, where the desk it arrived on is known (internal/zded,
	// Arrived), and carried rather than asked again. The manifests are a
	// directory of files: asking them again on every path that writes or shows
	// a record is a directory read and a YAML parse per notification, on the
	// far end of a D-Bus call the sending app is blocked on - and two paths
	// that asked separately could get two answers about one arrival.
	//
	// Never serialised, in either direction. A surface has no use for it, and a
	// file that could carry it would be a file that could clear it.
	Private bool `json:"-"`
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
	// rev counts the times what is in here changed. It is what lets the writer
	// tell an idle session from a busy one without comparing records: the
	// snapshot is rewritten on a clock, and a laptop where nothing has arrived
	// since the last write should not touch the disk every two minutes for the
	// rest of the afternoon (internal/zded, SaveHistory).
	rev uint64
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
	h.rev++
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
			h.rev++
			return true
		}
	}
	return false
}

// Rev is how many times this history has changed, and it is only ever compared
// with itself. The writer keeps the one it last wrote down, so a snapshot that
// would be the same file as the one already there is not written at all.
func (h *History) Rev() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rev
}

// Restore puts a snapshot's records back, oldest first, in front of whatever
// this session has already received (snapshot.go, ReadSnapshot).
//
// In front, because the ring is in the order things happened and these happened
// before the daemon started. Trimmed from the front for the reason Add trims
// there: the bound keeps the newest, so a restore that arrives after a busy
// minute costs the restored records rather than the live ones.
func (h *History) Restore(records []Record) {
	if len(records) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// A slice of its own rather than appending onto what the caller handed us:
	// that slice is the file's, and growing it in place would write through to
	// whatever else is still holding it.
	all := make([]Record, 0, len(records)+len(h.records))
	all = append(all, records...)
	all = append(all, h.records...)
	if len(all) > HistoryMax {
		all = all[len(all)-HistoryMax:]
	}
	h.records = all
	h.rev++
}
