package attn

import (
	"sort"
	"sync"
	"time"
)

// PerSenderMax is how many arrivals zded keeps from any one sender.
//
// A ring each, rather than one ring for the whole session, because one ring
// let a single app answer the question for everybody: a download posting a
// hundred progress updates left a hundred records, and "what did I miss" came
// back as one download and nothing else (docs/vision.md, ask A5). With a ring
// per sender a loud app evicts nothing but itself.
//
// Thirty, and they are thirty distinct notifications rather than thirty
// renderings of one, because a notification carrying replaces_id now lands on
// top of the record it supersedes instead of beside it (see Replace). That is
// a day or two of one ordinary app, which is the span the question asks about.
//
// A session of five apps therefore holds fewer records than the flat two
// hundred this used to be, and answers better: thirty from each of five is a
// worse number and a truer history than a hundred and ninety from one app and
// ten from the rest.
//
// All of them are this session's. What outlives the daemon is two smaller
// things: the queue, because it is what you still owe, and a snapshot of the
// newest snapshotMax records across every sender, with their bodies cut to
// snapshotBodyMax (snapshot.go). So a restart costs the tail of every ring and
// most of the long bodies, and what it keeps is enough to answer "what did I
// miss" across a reboot instead of starting every session blank.
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
// The journal does not carry bodies, and that is worth saying beside the shape
// argument rather than instead of it. A queue item is the id, the desk, the
// sender, the urgency and the one-line summary, and nothing else: the body was
// dropped from it because a file that is fsynced per arrival and rewritten only
// at Open is where a message would stay for the session and past it
// (internal/journal, Item). So the two files divide by what they are for and not
// only by how they are written, and the disk holds a notification body in one
// place - the snapshot, bounded, 0600, and never for an arrival on a desk that
// declared private.
const PerSenderMax = 30

// SendersMax is how many senders hold a ring of their own at once. When a
// thirteenth name arrives, the ring that is cheapest to lose goes whole (see
// dropExtraSenders, which is where the choice is argued).
//
// Bounded because From is the sender's own claim and nothing verifies it
// (notify.go, Notification.From). An app that varies its name mints a ring per
// variation, so a map keyed on it is unbounded in senders even while every
// ring inside it is bounded - the same leak, one level up.
//
// Twelve is more names than a session that is not being played with has: chat,
// mail, the browser, a download, a build, the calendar, battery, bluetooth and
// updates is nine.
//
// The arithmetic, done the way the ring's above is, and it is the whole of it:
// every other count in this package (bodyMax, snapshotBodyMax) points here.
// Twelve rings plus the two nothing on the bus can reach - the nameless one
// (see nobody) and the desktop's own (see SelfFrom) - is fourteen, at thirty
// records each: 420 records. A record at its limit is a summary of summaryMax
// (300), a body of bodyMax (4000), a sender name bounded at summaryMax too
// (300), and actionsMax action pairs of actionTextMax each (9 x 160 = 1440):
// 6040 characters, which in an alphabet that costs four bytes a character is
// about 24 KB. So the ceiling is about 10 MB.
//
// Against 8 MB, which is what the flat ring of two hundred actually held once
// its own action lists are counted - they were bounded only by summaryMax then,
// so 9 x 600 characters was 24 KB of buttons on top of 16 KB of message, and
// every stated figure in this package was describing a number smaller than the
// real one. Bounding the action text (notify.go, actionTextMax) gave back most
// of what the extra records cost, which is why nearly doubling the record count
// moved the ceiling by an eighth. It is still a few hundred kilobytes in any
// session made of real notifications: a ceiling only binds when something is
// trying to reach it.
//
// What goes when a sender is evicted is the whole of its ring, in one step:
// every record that name ever sent. Add answers with every id in it, so the
// caller can let go of all of them (see Add). There is no half-evicted sender,
// because what is bounded here is the number of names and not the number of
// records under them.
//
// It is a bound on memory first, but the rule that picks the ring decides
// whether it is also leverage for an app that renames itself, and it is chosen
// so that it is not: see dropExtraSenders. What none of it does is tell you who
// sent anything. That waits on a sender being known by the socket it arrived on
// rather than by what it says about itself (docs/vision.md, principle 6).
const SendersMax = 12

// nobody is the ring for a record whose sender named itself nothing. It is
// neither counted against SendersMax nor ever chosen by it.
//
// No app can land here. A notification off the bus that gives no name, or a
// name of "-", is recorded under the bus's own name for its connection instead
// (notify.go, claim), and the bus hands that out rather than letting the peer
// choose it.
//
// Nothing else in the tree reaches it either, and it is worth being exact about
// that rather than generous. zde's own notifications are not nameless: the one
// path that sends one sends as "zde" (internal/zded, launchFrom). Nor is a
// person's own reminder, because `zde queue add` writes the journal and never
// this - the queue and the history are two records, and only an arrival makes
// both (internal/zded, queueAdd against Arrived). What is actually here is a row
// off a snapshot file whose sender field is empty: one an older zde wrote before
// the name was filled in, or one somebody edited, since ReadSnapshot bounds that
// field without insisting on it (snapshot.go).
//
// So this ring is small and it is history rather than this session, which is the
// reason it is exempt and not a reason to trim it: losing what a restart brought
// back to make room for an app's twelfth invented name is the one eviction
// nobody could defend.
const nobody = ""

// unevictable is the pair of rings the bound above never spends: the nameless
// one, and the desktop's own (notify.go, SelfFrom).
//
// The same argument for both, and the reservation is what makes it an argument
// rather than a preference: neither name can be reached from the bus, so
// neither is a name an app can mint, so neither is part of the leak the bound
// exists to stop. Leaving zde's own ring in the count was the bound working for
// the attacker again, one level up from the eviction rule (see
// dropExtraSenders): "this desk could not start three of its apps" holds one
// record, which is the cheapest thing on the machine, so twelve invented names
// threw the desktop's own message out of the history first. Being able to
// silence what zde says about itself, by sending twelve notifications, is worth
// more to somebody than the ring it costs to stop it.
//
// On the name and not on Record.Self, and the difference is what each of the
// two is for. The badge says who a person is looking at; this says which ring a
// record is filed in, and a ring is a bound. Keyed on the badge, an arrival with
// no badge and the name "zde" would file into the exempt ring anyway, and a name
// that merely looks like "zde" gets a ring of its own here either way - counted
// against SendersMax and evicted like any other claim, which is exactly what it
// should be. So this stays the reservation's own job (notify.go, SelfFrom).
//
// Two names and not a rule, because it is two names. A list is what a caller
// can read and a predicate is what a caller has to trust.
func unevictable(from string) bool { return from == nobody || from == SelfFrom }

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
	// It is also which ring this record lives in, which is why the number of
	// distinct ones is bounded (see SendersMax).
	From string `json:"from,omitempty"`
	// Self says the desktop made this one, and it is the field every surface
	// that draws a sender has to read before it draws the name above: a claim
	// that is drawn like "zde" is a claim, and this is not (see
	// Notification.Self for why the name cannot carry it and what was tried).
	//
	// On the wire, because the surfaces are the point. The centre and the popup
	// draw it as a badge of their own rather than as text inside the sender
	// column, since anything drawn inside that column is a string an app can
	// send (shell/NotifCenter.qml, shell/AttnPopup.qml); `zde attn center` and
	// `zde queue` give it a column, since an app's name cannot hold a tab.
	//
	// Set where the record is made and never worked out from From. Reading it
	// back off the name would be the string being the fact again, one layer
	// down, and would hand any lookalike the badge it walked past the
	// reservation for.
	Self bool `json:"self,omitempty"`
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

// clone is this record as something a caller may keep.
//
// The struct copies on assignment, but Actions is a slice: a plain copy hands
// out a window into the history's own memory, and the daemon marshals what it
// gives out on another goroutine while arrivals are still coming in. A caller
// that wrote through it - the socket layer, a surface adapter, a test - would
// be editing what the notification center reads back, and the aliasing would
// not show up as a race the tools catch, because both sides are under different
// locks or none.
//
// Only the slice, not the strings in it: a string cannot be written through.
func (r Record) clone() Record {
	if r.Actions != nil {
		r.Actions = append([]Action(nil), r.Actions...)
	}
	return r
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

// History is what arrived: a bounded ring per sender, bounded again in how
// many senders have one. Safe for concurrent use, because arrivals come off
// the bus and the center asks over the socket, on different goroutines.
//
// The rings are what stops one app filling the history. Putting them back
// together is what reading does: Recent and Snapshot both answer with a single
// list, newest first across every sender, because that is the shape the center
// reads and the shape the question has - what happened while I was away, most
// recent thing first.
type History struct {
	mu sync.Mutex
	// rings is one sender's records each, keyed by the name that sender
	// claimed. A sender holding no records is not in here at all, so the map
	// is at most SendersMax names plus nobody's.
	rings map[string]*ring
	// seq numbers arrivals in the order this history received them, and it is
	// the only order it trusts. Record.At is when the daemon says a thing
	// arrived, and a restored record's At came off a file anything could have
	// edited - so merging the rings on it would let a snapshot with a date in
	// the year 3000 sit at the top of the center for ever.
	seq int64
	// back numbers restored arrivals, downwards from zero, so that everything
	// a snapshot puts back sorts behind everything this session received (see
	// Restore). Signed for exactly this: there is no room below zero in a
	// uint64, and the alternative is renumbering every live record on a
	// restore.
	back int64
	// rev counts the times what is in here changed. It is what lets the writer
	// tell an idle session from a busy one without comparing records: the
	// snapshot is rewritten on a clock, and a laptop where nothing has arrived
	// since the last write should not touch the disk every two minutes for the
	// rest of the afternoon (internal/zded, SaveHistory).
	rev uint64
}

// ring is one sender's records, oldest first, at most PerSenderMax of them.
type ring struct{ at []entry }

// entry is a record together with the order it arrived in.
//
// The order is here and not on the Record because a Record is what goes over
// the socket and into the snapshot file, and this number means nothing outside
// the history that issued it.
type entry struct {
	rec Record
	seq int64
}

// last is when this ring was last added to, and a ring is never empty while it
// is in the map.
func (r *ring) last() int64 { return r.at[len(r.at)-1].seq }

// prepend puts records in front of what this ring already holds, and trims to
// the bound from the front.
//
// A slice of its own rather than appending onto what the caller handed us:
// that slice is the file's, and growing it in place would write through to
// whatever else is still holding it.
func (r *ring) prepend(es []entry) {
	all := make([]entry, 0, len(es)+len(r.at))
	all = append(all, es...)
	all = append(all, r.at...)
	if len(all) > PerSenderMax {
		all = all[len(all)-PerSenderMax:]
	}
	r.at = all
}

// Add records an arrival and answers with the id of every record it pushed
// out, oldest first.
//
// The ids are answered rather than dropped on the floor because a record
// leaving here is the moment nothing can address that notification any more:
// it cannot be dismissed, invoked or listed, so whatever else is holding state
// about it should let go too (internal/attn, Forget).
//
// A list, where this used to answer with one id, because one arrival can now
// cost more than one record. It pushes the oldest out of its own sender's ring
// when that ring is full, and it pushes out a whole ring - every record a
// sender ever sent - when its name is the thirteenth and one of the twelve has
// to go. A caller that kept only the first would leak exactly what the answer
// exists to stop leaking.
//
// Nil when nothing went, which is the ordinary case and allocates nothing.
func (h *History) Add(r Record) []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.add(r)
}

// Replace is an arrival that supersedes one already here: the record with the
// old id comes out, and the new one goes in at the top. It answers as Add
// does, and an old id that is no longer here is not an error - there is
// nothing to supersede, and the arrival is still an arrival.
//
// This is what replaces_id means, kept rather than flattened. A notification
// carrying one is not a second notification: it is the same one saying
// something new, a download at 2% rather than at 1%. Appending was the root of
// the noise that the rings above only bound - a hundred progress updates left
// a hundred records, ninety-nine of them marked dismissed and not one of them
// worth reading.
//
// What it costs is the superseded text, which is gone rather than kept beside
// the new words. That is a sender editing its own history, and it is worth
// being plain that it is allowed. replaces_id is scoped to the connection that
// sent the original (notify.go), so a sender can only rewrite what it said
// itself; an app that does not want a thing read does not send it in the first
// place; and against that narrow loss, keeping every superseded rendering is
// what made the history unable to answer its one question. It is not what
// principle 3 forbids either - that is a *mode* deciding what is kept, and
// nothing on this path reads the mode.
//
// At the top and not in its old place, because a download that has just
// finished is the most recent thing that happened and the center is read
// newest first.
//
// The old id is not in what comes back. The bus side let go of it before this
// was called, on the same path that looked it up (notify.go, the replaces
// branch), and the notification is not gone in any case: it is this record,
// under a new id.
func (h *History) Replace(old uint64, r Record) []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remove(old)
	return h.add(r)
}

// add is Add with the lock already held.
func (h *History) add(r Record) []uint64 {
	h.seq++
	ring := h.ringFor(r.From)
	ring.at = append(ring.at, entry{rec: r, seq: h.seq})
	h.rev++
	var gone []uint64
	if len(ring.at) > PerSenderMax {
		gone = append(gone, ring.at[0].rec.ID)
		// Copied down rather than resliced from the front: a reslice leaves the
		// backing array growing to the right for ever, which is the leak this
		// bound exists to stop, only slower.
		copy(ring.at, ring.at[1:])
		ring.at = ring.at[:PerSenderMax]
	}
	// Exempt from the eviction below, or a sender's first notification is the
	// one its own arrival throws away (see dropExtraSenders).
	return append(gone, h.dropExtraSenders(r.From)...)
}

// remove takes one record out of whichever ring holds it, and says whether it
// found one.
//
// A ring left empty goes with it. Without that, a sender that has said nothing
// since would hold a name against SendersMax for the rest of the session while
// holding no records at all.
func (h *History) remove(id uint64) bool {
	for from, r := range h.rings {
		for i, e := range r.at {
			if e.rec.ID != id {
				continue
			}
			n := len(r.at)
			copy(r.at[i:], r.at[i+1:])
			// The slot the copy vacated still points at the record that was
			// there, and a body is up to bodyMax characters: left as it is, the
			// backing array would hold a message nothing can read for as long
			// as this ring lives.
			r.at[n-1] = entry{}
			r.at = r.at[:n-1]
			if len(r.at) == 0 {
				delete(h.rings, from)
			}
			return true
		}
	}
	return false
}

// ringFor is a sender's ring, made if this is the first thing it has sent.
func (h *History) ringFor(from string) *ring {
	if h.rings == nil {
		h.rings = map[string]*ring{}
	}
	r := h.rings[from]
	if r == nil {
		r = &ring{}
		h.rings[from] = r
	}
	return r
}

// dropExtraSenders drops whole rings until SendersMax names are left, and
// answers with every id that went with them.
//
// The ring that goes is the cheapest one to lose: fewest records first, and
// between two holding the same number, the one heard from longest ago.
//
// Not the least recently used, which is what this was, and which had the bound
// working for the attacker instead of against him. A name costs nothing to mint
// (notify.go, Notification.From), so under least-recently-used, twelve
// notifications under twelve invented names evicted the twelve senders you
// actually hear from - a bound keyed on a free claim, handing out leverage in
// proportion to that claim. It also picked exactly the wrong ring: an
// established app that told you something an hour ago lost everything to
// one-record rings that appeared a second ago.
//
// Cheapest-first turns that round. An invented name arrives holding one record,
// so the next invented name evicts that one rather than anybody real, and the
// invented names churn among themselves for as long as they keep coming. What
// it costs to displace a sender is then what it costs to out-hold it: a ring of
// thirty goes only once every other ring holds thirty, which is notifications
// sent and not names invented. That is the leverage the flat ring of two
// hundred charged, and getting back to it is the whole point of the change.
//
// Never the ring this arrival just landed in, and that is not a detail. Without
// the exemption a new sender's first record is the cheapest thing on the
// machine the instant it exists, so it is dropped by its own arrival, and an
// app you have just installed can never appear in the history at all while
// twelve others hold a record each. The exemption spends the cheapest existing
// ring once instead - after that the arriving sender holds the cheap ring and
// the next new name takes it.
//
// What it costs in ordinary use is the sender that has told you least: an app
// you hear from once a week loses its one record when a thirteenth name turns
// up, which is one row of the center and the smallest loss the machine can
// take. Worth saying plainly, because it is a real bias and it is the opposite
// of the order the history is read in: this prefers volume to recency, so the
// occasional sender is the one that churns. It is chosen that way because a
// session of nine ordinary senders never reaches this bound at all - so the
// case it has to be good at is the case that does reach it.
//
// The two rings nothing on the bus can reach are neither counted nor
// candidates: the nameless one and the desktop's own (see unevictable).
func (h *History) dropExtraSenders(arrived string) []uint64 {
	var gone []uint64
	for {
		named := len(h.rings)
		for _, ours := range []string{nobody, SelfFrom} {
			if _, held := h.rings[ours]; held {
				named--
			}
		}
		if named <= SendersMax {
			return gone
		}
		cheapest, held, since, found := "", 0, int64(0), false
		for from, r := range h.rings {
			if unevictable(from) || from == arrived {
				continue
			}
			// No two records share a seq, so the tie-break is total and the
			// map's random order cannot decide this differently on two runs.
			n, last := len(r.at), r.last()
			if !found || n < held || (n == held && last < since) {
				cheapest, held, since, found = from, n, last, true
			}
		}
		for _, e := range h.rings[cheapest].at {
			gone = append(gone, e.rec.ID)
		}
		delete(h.rings, cheapest)
	}
}

// newestFirst is every record here in the order this history received them,
// newest first, across every sender. Called with the lock held.
//
// Sorted rather than merged ring by ring: there are at most SendersMax+1 rings
// of PerSenderMax records, so this is a few hundred items, and it runs when
// somebody presses Mod+n or when the snapshot is written on its two-minute
// clock. The arrival path, which is the one a hundred notifications a minute
// reach, never comes through here.
func (h *History) newestFirst() []entry {
	n := 0
	for _, r := range h.rings {
		n += len(r.at)
	}
	all := make([]entry, 0, n)
	for _, r := range h.rings {
		all = append(all, r.at...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seq > all[j].seq })
	return all
}

// Recent is what arrived, newest first, as a copy. Newest first because that is
// the order the center reads in and the order the question is asked in: what
// happened while I was away, most recent thing first.
//
// One list, whatever the rings did. Which sender a record was filed under is a
// bound and not an answer, and nobody asks "what did I miss, per app".
func (h *History) Recent() []Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	all := h.newestFirst()
	out := make([]Record, len(all))
	for i, e := range all {
		out[i] = e.rec.clone()
	}
	return out
}

// Find is one record by id, for a caller that has to know what it is acting on
// before it acts. A copy, for the reason Recent's are: the actions are the one
// part of a record that is a slice (see clone).
func (h *History) Find(id uint64) (Record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.rings {
		for _, e := range r.at {
			if e.rec.ID == id {
				return e.rec.clone(), true
			}
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
	for _, r := range h.rings {
		for i := range r.at {
			if r.at[i].rec.ID == id {
				r.at[i].rec.Dismissed = true
				h.rev++
				return true
			}
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

// Restore puts a snapshot's records back, behind whatever this session has
// already received (snapshot.go, ReadSnapshot). It takes them oldest first,
// which is the order the file is in.
//
// Behind, because the history is read in the order things happened and these
// happened before the daemon started. They are numbered downwards from zero so
// that every one of them sorts below every live arrival, whichever ring each
// of them lands in.
//
// Both bounds hold over the result. A ring that overflows loses from its
// front, which is the restored end, for the reason the bound exists at all:
// the newest are what the question is about, and what this session actually
// received is worth more than what it was told about the last one. A name that
// pushes the sender count over its bound loses its ring whole, by the same rule
// an arrival evicts under (see dropExtraSenders): a file naming twenty senders
// a record each keeps the twelve it heard from last.
//
// What that eviction pushed out is not answered, unlike Add's, and what makes
// that safe is when this runs: before the daemon takes the bus name and before
// anything is Watching, so no id here is one the bus side is holding (cmd/zded,
// the order in run). Nothing on the bus could be holding a restored id in any
// case - the connection that sent it ended with the last session, which is the
// same reason a restored row refuses to invoke anything (internal/zded,
// invoke). The day this is called with live records already in the rings, it
// has to answer with ids the way Add does: the eviction rule is about what a
// ring costs to lose and not about where its records came from, so a live ring
// can be the cheapest one on the machine.
func (h *History) Restore(records []Record) {
	if len(records) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Numbered before they are filed, so that a second restore in one session
	// goes behind the first as well as behind every live record.
	h.back -= int64(len(records))
	// Grouped and then prepended once per sender: a ring is rebuilt once
	// however many of the file's records belong to it.
	order := make([]string, 0, len(records))
	byFrom := make(map[string][]entry, len(records))
	for i, r := range records {
		if _, seen := byFrom[r.From]; !seen {
			order = append(order, r.From)
		}
		byFrom[r.From] = append(byFrom[r.From], entry{rec: r, seq: h.back + int64(i)})
	}
	for _, from := range order {
		h.ringFor(from).prepend(byFrom[from])
	}
	// Nothing to exempt: every ring here was just filled from the file, so
	// there is no arriving sender to protect from its own arrival.
	h.dropExtraSenders(nobody)
	h.rev++
}
