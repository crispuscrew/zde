package attn

import (
	"strconv"
	"strings"
	"testing"
)

// The center answers "what did I miss", and that question is asked newest
// first. Oldest first would put a week-old row at the top of a surface somebody
// opened to see what just happened.
//
// Across senders, because that is where the order could get lost now: each of
// these lives in a ring of its own, and one list is what the center reads.
func TestHistoryIsNewestFirst(t *testing.T) {
	var h History
	for i := uint64(1); i <= 3; i++ {
		h.Add(Record{ID: i, From: "app " + strconv.FormatUint(i, 10), Text: "number " + strconv.FormatUint(i, 10)})
	}
	got := h.Recent()
	if len(got) != 3 {
		t.Fatalf("history = %+v", got)
	}
	if got[0].ID != 3 || got[2].ID != 1 {
		t.Errorf("history reads %d, %d, %d: not newest first", got[0].ID, got[1].ID, got[2].ID)
	}
}

// Bounded, or a daemon that runs for a week grows for a week: notifications
// arrive at machine speed, and one chatty app is a hundred a day. The oldest
// go, because the newest are what the question is about.
func TestOneSendersHistoryIsBounded(t *testing.T) {
	var h History
	for i := uint64(1); i <= PerSenderMax+10; i++ {
		h.Add(Record{ID: i, From: "chat"})
	}
	got := h.Recent()
	if len(got) != PerSenderMax {
		t.Fatalf("the sender holds %d records, want the bound of %d", len(got), PerSenderMax)
	}
	if got[0].ID != PerSenderMax+10 {
		t.Errorf("newest is %d, want the last one added", got[0].ID)
	}
	if last := got[len(got)-1].ID; last != 11 {
		t.Errorf("oldest kept is %d, want 11: the ten before it should have gone", last)
	}
}

// The whole reason for a ring each. One download posting progress updates used
// to be a hundred records, and everything else in the session fell off the end
// behind it - so "what did I miss" came back as one download and nothing else.
//
// A loud sender now spends its own ring and nobody else's.
func TestALoudSenderPushesOutNothingButItsOwn(t *testing.T) {
	var h History
	h.Add(Record{ID: 1, From: "mail", Text: "Ilya: about the invoice"})
	for i := uint64(0); i < PerSenderMax*3; i++ {
		h.Add(Record{ID: 100 + i, From: "curl", Text: "downloading"})
	}
	seen := h.Recent()
	if len(seen) != PerSenderMax+1 {
		t.Fatalf("the history holds %d records, want the loud sender's %d and the one quiet record", len(seen), PerSenderMax)
	}
	oldest := seen[len(seen)-1]
	if oldest.ID != 1 || oldest.From != "mail" {
		t.Fatalf("the oldest record is %+v, want the quiet sender's: a loud one pushed it out", oldest)
	}
}

// The adversarial case, and the reason the eviction is keyed on what a ring is
// worth rather than on when it was last heard from.
//
// A name costs nothing to mint. Under least-recently-used, twelve notifications
// under twelve invented names evicted the twelve senders you actually hear
// from, which is a bound handing an attacker leverage in proportion to a value
// he gets for free. Cheapest-first makes the invented names evict each other:
// each arrives holding one record, so it is the next one's cheapest candidate.
func TestInventedSenderNamesCannotEvictAnEstablishedSender(t *testing.T) {
	var h History
	// The sender you actually hear from, with a ring worth keeping.
	for i := uint64(1); i <= PerSenderMax; i++ {
		h.Add(Record{ID: i, From: "mail", Text: "one of many"})
	}
	// The rest of the bound, a few records each: a session in ordinary use.
	for s := 2; s <= SendersMax; s++ {
		for k := 0; k < 3; k++ {
			h.Add(Record{ID: uint64(1000 + s*10 + k), From: "app " + strconv.Itoa(s), Text: "hello"})
		}
	}
	// And now an app that varies what it calls itself, one notification each.
	for n := 1; n <= SendersMax*4; n++ {
		h.Add(Record{ID: uint64(90000 + n), From: "impostor " + strconv.Itoa(n), Text: "hello"})
	}

	kept, impostors, ordinary := 0, 0, 0
	for _, r := range h.Recent() {
		switch {
		case r.From == "mail":
			kept++
		case strings.HasPrefix(r.From, "impostor "):
			impostors++
		case strings.HasPrefix(r.From, "app "):
			ordinary++
		}
	}
	if kept != PerSenderMax {
		t.Errorf("the established sender kept %d of its %d records after %d invented names", kept, PerSenderMax, SendersMax*4)
	}
	if impostors > 1 {
		t.Errorf("%d invented names hold records at once, want them evicting each other", impostors)
	}
	// One ordinary ring went and only one: the cheapest on the machine when the
	// first invented name turned up. After that the invented ones are the cheap
	// ones. That single ring is what this bound costs, and it is the price of
	// having one at all.
	if want := (SendersMax - 2) * 3; ordinary != want {
		t.Errorf("%d ordinary records survived, want %d: one ring of three, and no more, is what the invented names should have cost", ordinary, want)
	}
}

// The ring that goes is the cheapest to lose, and between two that cost the
// same it is the one heard from longest ago.
//
// The fixture is built so that no other rule reaches the same answer, because
// three plausible ones would otherwise agree with this one and the test would
// pass on any of them. The ring holding three is older than everything else by
// every measure and survives, so this is not least-recently-used. The rings
// holding two are heard from again in the reverse of the order they were made
// in, so the one that goes is the one created last, and this is not
// first-created either.
func TestTheCheapestRingToLoseIsTheOneThatGoes(t *testing.T) {
	var h History
	// One ring worth three records, made before anything else and never
	// touched again: the oldest ring on the machine, and the most expensive.
	for i := uint64(1); i <= 3; i++ {
		h.Add(Record{ID: i, From: "three of these", Text: "one of three"})
	}
	// And the rest worth two each, made in order.
	for s := 2; s <= SendersMax; s++ {
		h.Add(Record{ID: uint64(100 + s), From: "app " + strconv.Itoa(s), Text: "hello"})
	}
	// Heard from again in the opposite order, which is what separates "least
	// recently heard from" from "made first": app 12 was the last of them to be
	// made and is now the longest since anybody heard from it.
	for s := SendersMax; s >= 2; s-- {
		h.Add(Record{ID: uint64(200 + s), From: "app " + strconv.Itoa(s), Text: "and another"})
	}
	gone := h.Add(Record{ID: 999, From: "one name too many", Text: "hello"})

	last := uint64(SendersMax)
	if len(gone) != 2 || gone[0] != 100+last || gone[1] != 200+last {
		t.Fatalf("the arrival answered %v, want app %d's two records oldest first: the two-record rings are cheaper than the three, and that one is the longest since anybody heard from it", gone, last)
	}
	if _, still := h.Find(1); !still {
		t.Error("the ring holding three went while cheaper rings were there to take, so this is picking the oldest and not the cheapest")
	}
	if _, still := h.Find(102); !still {
		t.Error("the first of the two-record rings went, so this is picking the one made first and not the one heard from longest ago")
	}
	if _, still := h.Find(999); !still {
		t.Error("the name that needed the room is not here, so something else was evicted for nothing")
	}
}

// A sender you have just installed has to be able to appear at all.
//
// Cheapest-first would otherwise evict the arriving ring itself: it holds one
// record, so it is the cheapest thing on the machine the instant it is made,
// and an app that has just sent you its first notification would never reach
// the center while twelve others hold a record each. Silently, and for ever.
func TestAFirstNotificationFromANewSenderIsNotTheOneEvicted(t *testing.T) {
	var h History
	for s := 1; s <= SendersMax; s++ {
		for k := 0; k < 2; k++ {
			h.Add(Record{ID: uint64(s*10 + k), From: "app " + strconv.Itoa(s), Text: "hello"})
		}
	}
	h.Add(Record{ID: 999, From: "just installed", Text: "hello"})
	if _, found := h.Find(999); !found {
		t.Fatal("a new sender's first notification was the record its own arrival threw away")
	}
	// And it establishes itself: a second one costs nobody anything, because
	// the name is already in the table.
	if gone := h.Add(Record{ID: 1000, From: "just installed", Text: "and another"}); len(gone) != 0 {
		t.Errorf("a second notification from a sender already here evicted %v", gone)
	}
}

// A sender goes whole: there is no half-evicted one, and every id in its ring
// comes back, because each is a notification nothing can address any more.
func TestAnEvictedSenderLosesItsWholeRingAndEveryIdComesBack(t *testing.T) {
	var h History
	for i := uint64(1); i <= 3; i++ {
		h.Add(Record{ID: i, From: "the cheapest", Text: "one of three"})
	}
	// Everything else holds more, so the ring of three is the one to go.
	for s := 2; s <= SendersMax; s++ {
		for k := 0; k < 5; k++ {
			h.Add(Record{ID: uint64(1000 + s*10 + k), From: "app " + strconv.Itoa(s), Text: "hello"})
		}
	}
	gone := h.Add(Record{ID: 999, From: "one name too many", Text: "hello"})

	if len(gone) != 3 || gone[0] != 1 || gone[1] != 2 || gone[2] != 3 {
		t.Fatalf("the arrival answered %v, want every id of the ring that went with it, oldest first", gone)
	}
	for _, r := range h.Recent() {
		if r.From == "the cheapest" {
			t.Fatalf("%+v is still here, and its sender's was the ring to go", r)
		}
	}
}

// An empty From is never an app's doing: one that sends no name is recorded
// under the bus's own name for its connection instead (notify.go, claim), and
// the bus hands that out rather than letting the peer pick it. Nothing live
// reaches this ring at all - zde's own notification sends as "zde", and `zde
// queue add` writes the journal and not the history - so what is in it came off
// a snapshot file with an empty sender field, and losing what a restart brought
// back to make room for an app's invented names is the one eviction nobody
// could defend (see nobody).
func TestARecordWithNoSenderKeepsARingNoAppCanEvict(t *testing.T) {
	var h History
	h.Add(Record{ID: 1, Text: "restored, with nothing saying who sent it"})
	// The whole of the bound, in names an app made up.
	for s := 1; s <= SendersMax; s++ {
		h.Add(Record{ID: uint64(100 + s), From: "invented name " + strconv.Itoa(s), Text: "hello"})
	}
	// Nor is it counted against the bound: an app has as many names here as it
	// would have had if no such row had ever come back.
	if seen := h.Recent(); len(seen) != SendersMax+1 {
		t.Errorf("the history holds %d records, want %d senders and the nameless one: the nameless ring cost an app its place", len(seen), SendersMax)
	}
	for s := SendersMax + 1; s <= SendersMax*2; s++ {
		h.Add(Record{ID: uint64(100 + s), From: "invented name " + strconv.Itoa(s), Text: "hello"})
	}
	if _, found := h.Find(1); !found {
		t.Error("the nameless record went, to make room for an app's invented names")
	}
	// And it is not free of the bound on records, only of the bound on names.
	for i := uint64(0); i < PerSenderMax*2; i++ {
		h.Add(Record{ID: 1000 + i, Text: "and another"})
	}
	nameless := 0
	for _, r := range h.Recent() {
		if r.From == "" {
			nameless++
		}
	}
	if nameless != PerSenderMax {
		t.Errorf("the nameless ring holds %d records, want the bound of %d", nameless, PerSenderMax)
	}
}

// replaces_id is a sender saying this is the same notification with something
// new to say - a download at 2% rather than at 1%. Appending was the root of
// the noise: a hundred progress updates left a hundred records, ninety-nine of
// them dismissed and not one worth reading.
//
// At the top afterwards, because a download that has just moved is the most
// recent thing that happened and the center is read newest first.
func TestANotificationThatReplacesAnotherTakesItsPlace(t *testing.T) {
	var h History
	h.Add(Record{ID: 1, From: "curl", Text: "downloading 1%"})
	h.Add(Record{ID: 2, From: "mail", Text: "Ilya: about the invoice"})
	last := uint64(1)
	for i := uint64(3); i <= 100; i++ {
		h.Replace(last, Record{ID: i, From: "curl", Text: "downloading"})
		last = i
	}

	seen := h.Recent()
	if len(seen) != 2 {
		t.Fatalf("the history holds %d records after one download and ninety-eight updates of it", len(seen))
	}
	if seen[0].ID != last {
		t.Errorf("the newest is %d, want the replacement %d: it is the most recent thing that happened", seen[0].ID, last)
	}
	if seen[1].Text != "Ilya: about the invoice" {
		t.Errorf("the record behind it is %+v, want the one the download never touched", seen[1])
	}

	// An id nothing here holds any more is not an error. There is nothing to
	// supersede, and an arrival is still an arrival.
	h.Replace(9999, Record{ID: 500, From: "curl", Text: "downloading something else"})
	if len(h.Recent()) != 3 {
		t.Errorf("history = %+v, want the arrival kept: it replaced nothing, which is not the same as saying nothing", h.Recent())
	}
}

// What became of a notification is half of what the center shows: a row that
// still says "waiting" after it was finished sends somebody back to a desk for
// something that is done.
func TestHistoryMarksWhatWasDismissed(t *testing.T) {
	var h History
	h.Add(Record{ID: 7, From: "build", Text: "the build failed", Queued: true})
	if !h.Dismiss(7) {
		t.Fatal("dismissing a record that is there answered no")
	}
	got := h.Recent()
	if !got[0].Dismissed {
		t.Errorf("record = %+v, want it marked dismissed", got[0])
	}
	// An id that has fallen off the end is not an error: the thing is not
	// waiting either way, which is what was asked for.
	if h.Dismiss(999) {
		t.Error("dismissing an id that was never here answered yes")
	}
}

// A restore is yesterday, and yesterday goes behind today.
//
// The history is read in the order things happened, so records from the last
// session belong at its old end - and when the two together are more than a
// ring holds, it is the restored ones that go, because the bound keeps the
// newest and what this session has actually received is worth more than what
// it was told about the last one.
func TestARestoredHistorySitsBehindWhatHasAlreadyArrived(t *testing.T) {
	var h History
	h.Add(Record{ID: 100, From: "mail", Text: "arrived just now"})
	h.Restore([]Record{{ID: 1, From: "mail", Text: "from last time", Restored: true}})
	got := h.Recent()
	if len(got) != 2 || got[0].ID != 100 || got[1].ID != 1 {
		t.Fatalf("history reads %+v, want the live one above the restored one", got)
	}

	// Behind holds across senders too, or a snapshot of one app would sort
	// itself among this session's arrivals from another.
	var mixed History
	mixed.Add(Record{ID: 100, From: "mail", Text: "live"})
	mixed.Restore([]Record{{ID: 1, From: "chat", Text: "from last time", Restored: true}})
	if seen := mixed.Recent(); seen[0].ID != 100 {
		t.Errorf("history reads %+v, want this session's arrival above another sender's restored one", seen)
	}

	// And the bound holds over the two together, or a snapshot could push out
	// what this session has just been sent.
	var full History
	for i := uint64(1); i <= PerSenderMax; i++ {
		full.Add(Record{ID: 1000 + i, From: "mail", Text: "live"})
	}
	old := make([]Record, 10)
	for i := range old {
		old[i] = Record{ID: uint64(i + 1), From: "mail", Text: "from last time"}
	}
	full.Restore(old)
	seen := full.Recent()
	if len(seen) != PerSenderMax {
		t.Fatalf("the sender holds %d records after a restore, want the bound of %d", len(seen), PerSenderMax)
	}
	for _, r := range seen {
		if r.Text != "live" {
			t.Fatalf("a restored record survived a full ring: %+v", r)
		}
	}
}

// The names are bounded over a restore as well, or a snapshot file naming
// twenty senders would put twenty rings in a daemon that bounds itself to
// twelve. What goes is what a restore can afford to lose: a restored record
// sorts below every live one, so it is a restored ring that comes out.
func TestARestoreCannotBringBackMoreSendersThanTheBound(t *testing.T) {
	var h History
	old := make([]Record, 0, SendersMax*2)
	for i := 1; i <= SendersMax*2; i++ {
		old = append(old, Record{ID: uint64(i), From: "app " + strconv.Itoa(i), Text: "from last time"})
	}
	h.Restore(old)
	seen := h.Recent()
	if len(seen) != SendersMax {
		t.Fatalf("the restore left %d records from %d senders, want the bound of %d", len(seen), SendersMax*2, SendersMax)
	}
	// The newest of the file, because that is what a bound on the newest means.
	if seen[0].ID != uint64(SendersMax*2) {
		t.Errorf("the newest restored record is %d, want the last one in the file", seen[0].ID)
	}
}

// Recent hands out a copy. A caller that could reach back into the history
// through it - the socket layer marshals whatever it is given, on another
// goroutine - would be writing into the daemon's state by accident.
//
// The whole record, and the actions with it. Every scalar copies on assignment
// and the actions do not: they are a slice, so a plain struct copy hands out a
// window into the ring itself, and a caller writing through it edits what the
// notification center reads back. Find hands out a record too, and by the same
// rule.
func TestRecentIsACopy(t *testing.T) {
	var h History
	h.Add(Record{
		ID:      1,
		From:    "mail",
		Text:    "mine",
		Actions: []Action{{Key: "reply", Label: "Reply"}},
	})

	got := h.Recent()
	got[0].Text = "yours"
	got[0].Actions[0].Label = "Delete everything"
	again := h.Recent()
	if again[0].Text != "mine" {
		t.Errorf("the history says %q after a caller edited its copy", again[0].Text)
	}
	if again[0].Actions[0].Label != "Reply" {
		t.Errorf("the history offers %q after a caller edited the copy it was handed: the actions share the ring's own memory", again[0].Actions[0].Label)
	}

	found, ok := h.Find(1)
	if !ok {
		t.Fatal("the record is not there")
	}
	found.Actions[0].Key = "delete"
	if after, _ := h.Find(1); after.Actions[0].Key != "reply" {
		t.Errorf("the history's key is %q after a caller edited what Find gave it", after.Actions[0].Key)
	}
}
