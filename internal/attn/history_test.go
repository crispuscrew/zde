package attn

import (
	"strconv"
	"testing"
)

// The center answers "what did I miss", and that question is asked newest
// first. Oldest first would put a week-old row at the top of a surface somebody
// opened to see what just happened.
func TestHistoryIsNewestFirst(t *testing.T) {
	var h History
	for i := uint64(1); i <= 3; i++ {
		h.Add(Record{ID: i, Text: "number " + strconv.FormatUint(i, 10)})
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
// arrive at machine speed, and one chatty app is a hundred a day. The oldest go,
// because the newest are what the question is about.
func TestHistoryIsBounded(t *testing.T) {
	var h History
	for i := uint64(1); i <= HistoryMax+10; i++ {
		h.Add(Record{ID: i})
	}
	got := h.Recent()
	if len(got) != HistoryMax {
		t.Fatalf("history holds %d records, want the bound of %d", len(got), HistoryMax)
	}
	if got[0].ID != HistoryMax+10 {
		t.Errorf("newest is %d, want the last one added", got[0].ID)
	}
	if last := got[len(got)-1].ID; last != 11 {
		t.Errorf("oldest kept is %d, want 11: the ten before it should have gone", last)
	}
}

// What became of a notification is half of what the center shows: a row that
// still says "waiting" after it was finished sends somebody back to a desk for
// something that is done.
func TestHistoryMarksWhatWasDismissed(t *testing.T) {
	var h History
	h.Add(Record{ID: 7, Text: "the build failed", Queued: true})
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
// The ring is in the order things happened, so records from the last session
// belong at its old end - and when the two together are more than the bound, it
// is the restored ones that go, because the bound keeps the newest and what
// this session has actually received is worth more than what it was told about
// the last one.
func TestARestoredHistorySitsBehindWhatHasAlreadyArrived(t *testing.T) {
	var h History
	h.Add(Record{ID: 100, Text: "arrived just now"})
	h.Restore([]Record{{ID: 1, Text: "from last time", Restored: true}})
	got := h.Recent()
	if len(got) != 2 || got[0].ID != 100 || got[1].ID != 1 {
		t.Fatalf("history reads %+v, want the live one above the restored one", got)
	}

	// And the bound holds over the two together, or a snapshot could push out
	// what this session has just been sent.
	var full History
	for i := uint64(1); i <= HistoryMax; i++ {
		full.Add(Record{ID: 1000 + i, Text: "live"})
	}
	old := make([]Record, 10)
	for i := range old {
		old[i] = Record{ID: uint64(i + 1), Text: "from last time"}
	}
	full.Restore(old)
	seen := full.Recent()
	if len(seen) != HistoryMax {
		t.Fatalf("history holds %d records after a restore, want the bound of %d", len(seen), HistoryMax)
	}
	for _, r := range seen {
		if r.Text != "live" {
			t.Fatalf("a restored record survived a full history: %+v", r)
		}
	}
}

// Recent hands out a copy. A caller that could reach back into the history
// through it - the socket layer marshals whatever it is given, on another
// goroutine - would be writing into the daemon's state by accident.
func TestRecentIsACopy(t *testing.T) {
	var h History
	h.Add(Record{ID: 1, Text: "mine"})
	got := h.Recent()
	got[0].Text = "yours"
	if again := h.Recent(); again[0].Text != "mine" {
		t.Errorf("the history says %q after a caller edited its copy", again[0].Text)
	}
}
