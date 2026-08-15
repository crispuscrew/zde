package journal

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/desk"
)

func open(t *testing.T, path string) *Journal {
	t.Helper()
	j, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func name(t *testing.T, s string) desk.Name {
	t.Helper()
	n, err := desk.ParseName(s)
	if err != nil {
		t.Fatalf("ParseName(%q): %v", s, err)
	}
	return n
}

// A journal that has never been written is not an error: it is the first boot.
func TestOpenMissing(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "sub", "journal.jsonl"))
	if st := j.State(); len(st.LastActive) != 0 || st.LastDesk != "" {
		t.Errorf("a fresh journal remembered %+v", st)
	}
}

// The whole point: what was recorded is there after a restart.
func TestRecordAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := open(t, path)
	if err := j.SetActive(name(t, "vshop.DP-1.code")); err != nil {
		t.Fatal(err)
	}
	if err := j.SetActive(name(t, "vshop.HDMI-A-1.aux")); err != nil {
		t.Fatal(err)
	}
	if err := j.SetLastDesk("vshop"); err != nil {
		t.Fatal(err)
	}
	j.Close()

	again := open(t, path)
	st := again.State()
	if got := st.LastActive["vshop"]["DP-1"]; got != "code" {
		t.Errorf("DP-1 last active = %q, want code", got)
	}
	if got := st.LastActive["vshop"]["HDMI-A-1"]; got != "aux" {
		t.Errorf("HDMI-A-1 last active = %q, want aux", got)
	}
	if st.LastDesk != "vshop" {
		t.Errorf("LastDesk = %q, want vshop", st.LastDesk)
	}
}

// Last write wins, per desk and monitor.
func TestLatestWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := open(t, path)
	j.SetActive(name(t, "vshop.DP-1.code"))
	j.SetActive(name(t, "vshop.DP-1.agent"))
	j.Close()

	if got := open(t, path).State().LastActive["vshop"]["DP-1"]; got != "agent" {
		t.Errorf("last active = %q, want the most recent", got)
	}
}

// A crash mid-write leaves a partial final line. That costs the last entry and
// must cost nothing else - the alternative is a session that will not start
// because the tail of a log is torn.
func TestTornTailKeepsEverythingBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := open(t, path)
	j.SetActive(name(t, "vshop.DP-1.code"))
	j.SetLastDesk("vshop")
	j.Close()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"kind":"active","desk":"vshop","monit`) // power cut here
	f.Close()

	again := open(t, path)
	st := again.State()
	if got := st.LastActive["vshop"]["DP-1"]; got != "code" {
		t.Errorf("lost an entry written before the tear: %q", got)
	}
	if st.LastDesk != "vshop" {
		t.Errorf("lost LastDesk: %q", st.LastDesk)
	}
	if again.Skipped() != 1 {
		t.Errorf("Skipped = %d, want the torn line counted", again.Skipped())
	}
}

// Writing after a torn tail must not build on the broken line.
func TestWriteAfterTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := open(t, path)
	j.SetActive(name(t, "vshop.DP-1.code"))
	j.Close()

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"kind":"acti`)
	f.Close()

	again := open(t, path)
	if err := again.SetActive(name(t, "vshop.DP-1.agent")); err != nil {
		t.Fatal(err)
	}
	if err := again.Compact(); err != nil { // compaction is what heals the file
		t.Fatal(err)
	}
	again.Close()

	third := open(t, path)
	if got := third.State().LastActive["vshop"]["DP-1"]; got != "agent" {
		t.Errorf("last active = %q, want agent", got)
	}
	if third.Skipped() != 0 {
		t.Errorf("Skipped = %d after compaction, want a healed file", third.Skipped())
	}
}

// A workspace that moves monitors is renamed, and the position remembered for
// it has to move too or it points at a workspace that is not there.
func TestRenameMovesRememberedPosition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := open(t, path)
	j.SetActive(name(t, "vshop.DP-1.code"))
	if err := j.Renamed(desk.Rename{From: name(t, "vshop.DP-1.code"), To: name(t, "vshop.HDMI-A-1.code")}); err != nil {
		t.Fatal(err)
	}
	j.Close()

	st := open(t, path).State()
	if _, stale := st.LastActive["vshop"]["DP-1"]; stale {
		t.Error("the old monitor still claims a last-active workspace")
	}
	if got := st.LastActive["vshop"]["HDMI-A-1"]; got != "code" {
		t.Errorf("HDMI-A-1 last active = %q, want the renamed workspace", got)
	}
}

// A rename of some other workspace must not disturb what is remembered.
func TestRenameOfAnotherWorkspaceIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := open(t, path)
	j.SetActive(name(t, "vshop.DP-1.code"))
	j.Renamed(desk.Rename{From: name(t, "vshop.DP-1.agent"), To: name(t, "vshop.HDMI-A-1.agent")})
	j.Close()

	if got := open(t, path).State().LastActive["vshop"]["DP-1"]; got != "code" {
		t.Errorf("last active = %q, want code untouched", got)
	}
}

// Compaction rewrites the file to the shortest thing that replays the same,
// and must not change what is remembered.
func TestCompactPreservesState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := open(t, path)
	for i := 0; i < 200; i++ {
		j.SetActive(name(t, "vshop.DP-1.code"))
		j.SetActive(name(t, "haven.DP-1.db"))
	}
	j.SetLastDesk("haven")
	before := j.State()

	big, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Compact(); err != nil {
		t.Fatal(err)
	}
	small, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(small) >= len(big) {
		t.Errorf("compaction did not shrink the file: %d -> %d", len(big), len(small))
	}
	if lines := strings.Count(string(small), "\n"); lines != 3 {
		t.Errorf("compacted to %d lines, want one per remembered thing", lines)
	}
	j.Close()

	after := open(t, path).State()
	if after.LastDesk != before.LastDesk ||
		after.LastActive["vshop"]["DP-1"] != before.LastActive["vshop"]["DP-1"] ||
		after.LastActive["haven"]["DP-1"] != before.LastActive["haven"]["DP-1"] {
		t.Errorf("compaction changed the state: %+v -> %+v", before, after)
	}
}

// Writing has to keep working after a compaction swapped the file underneath.
func TestWriteAfterCompact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := open(t, path)
	j.SetActive(name(t, "vshop.DP-1.code"))
	if err := j.Compact(); err != nil {
		t.Fatal(err)
	}
	if err := j.SetActive(name(t, "vshop.DP-1.agent")); err != nil {
		t.Fatalf("write after compaction: %v", err)
	}
	j.Close()

	if got := open(t, path).State().LastActive["vshop"]["DP-1"]; got != "agent" {
		t.Errorf("last active = %q, want the write after compaction", got)
	}
}

// The state a caller gets is a copy. The journal's own maps stay its own.
func TestStateIsACopy(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.jsonl"))
	j.SetActive(name(t, "vshop.DP-1.code"))

	st := j.State()
	st.LastActive["vshop"]["DP-1"] = "hijacked"
	st.LastActive["injected"] = map[string]string{"DP-1": "x"}

	if got := j.State().LastActive["vshop"]["DP-1"]; got != "code" {
		t.Errorf("writing to the returned state changed the journal: %q", got)
	}
	if _, injected := j.State().LastActive["injected"]; injected {
		t.Error("a caller added a desk to the journal through its own copy")
	}
}

// An entry from a newer zde is counted and left alone rather than treated as
// corruption.
func TestUnknownKindIsSkippedNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte(
		`{"kind":"active","desk":"vshop","monitor":"DP-1","slot":"code"}`+"\n"+
			`{"kind":"from-the-future","desk":"vshop"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := open(t, path)
	if got := j.State().LastActive["vshop"]["DP-1"]; got != "code" {
		t.Errorf("last active = %q, want the entry we understand", got)
	}
	if j.Skipped() != 1 {
		t.Errorf("Skipped = %d, want the unknown kind counted", j.Skipped())
	}
}

// Concurrent writers: zded will have niri events and IPC calls on different
// goroutines. Run with -race.
func TestConcurrentWrites(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.jsonl"))
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for k := 0; k < 25; k++ {
				j.SetActive(name(t, "vshop.DP-1.code"))
				j.State()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if got := j.State().LastActive["vshop"]["DP-1"]; got != "code" {
		t.Errorf("last active = %q after concurrent writes", got)
	}
}

// A journal long enough to be worth rewriting is compacted when it is opened,
// so it cannot grow without bound across restarts.
func TestOpenCompactsALongJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	var b strings.Builder
	for i := 0; i < compactAt+1; i++ {
		b.WriteString(`{"kind":"active","desk":"vshop","monitor":"DP-1","slot":"code"}` + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	j := open(t, path)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(after), "\n"); lines != 1 {
		t.Errorf("opened a %d-entry journal and it still has %d lines", compactAt+1, lines)
	}
	if got := j.State().LastActive["vshop"]["DP-1"]; got != "code" {
		t.Errorf("compaction on open lost the state: %q", got)
	}
}

func TestDefaultPathIsState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got, want := DefaultPath(), "/xdg/state/zde/journal.jsonl"; got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

// The queue outlives the session it was written in: what interrupted you last
// night is worth the same in the morning.
func TestQueueSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := j.Queue(Item{Text: "reply to ilya", Desk: "vshop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Queue(Item{Text: "pay the invoice", Desk: "haven"}); err != nil {
		t.Fatal(err)
	}
	j.Close()

	j, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	q := j.State().Queue
	if len(q) != 2 || q[0].Text != "reply to ilya" || q[0].Desk != "vshop" {
		t.Fatalf("queue = %+v, want both items oldest first", q)
	}
	if err := j.Done(first.ID); err != nil {
		t.Fatal(err)
	}
	if q := j.State().Queue; len(q) != 1 || q[0].Text != "pay the invoice" {
		t.Errorf("queue = %+v, want the finished one gone", q)
	}
}

// Ids are the journal's and never repeat, including after a restart. Numbering
// from the length of the queue hands the same id to two things as soon as one
// is finished - and then finishing one finishes the other.
func TestQueueIDsDoNotRepeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := j.Queue(Item{Text: "first", Desk: ""})
	if err := j.Done(one.ID); err != nil {
		t.Fatal(err)
	}
	two, _ := j.Queue(Item{Text: "second", Desk: ""})
	j.Close()

	j, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	three, _ := j.Queue(Item{Text: "third", Desk: ""})
	if two.ID == one.ID || three.ID == two.ID || three.ID == one.ID {
		t.Errorf("ids %d, %d, %d: one was handed out twice", one.ID, two.ID, three.ID)
	}
}

// Compaction rewrites the journal as the shortest thing that replays to the
// same state, and the queue's order is part of that state.
func TestQueueSurvivesCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	gone, _ := j.Queue(Item{Text: "done with this", Desk: "vshop"})
	j.Queue(Item{Text: "still waiting", Desk: "vshop"})
	j.Queue(Item{Text: "also waiting", Desk: "haven"})
	if err := j.Done(gone.ID); err != nil {
		t.Fatal(err)
	}
	if err := j.Compact(); err != nil {
		t.Fatal(err)
	}
	j.Close()

	j, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	q := j.State().Queue
	if len(q) != 2 || q[0].Text != "still waiting" || q[1].Text != "also waiting" {
		t.Errorf("queue after compaction = %+v", q)
	}
	if n := j.Skipped(); n != 0 {
		t.Errorf("%d entries could not be read back", n)
	}
}

// A caller holding the state cannot reach back into the journal through it.
func TestQueueStateIsACopy(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	j.Queue(Item{Text: "mine", Desk: "vshop"})
	st := j.State()
	st.Queue[0].Text = "theirs"
	if got := j.State().Queue[0].Text; got != "mine" {
		t.Errorf("journal item = %q, a caller wrote through the copy", got)
	}
}

// Compaction drops the entries the ids were learned from, so it has to carry
// the counter itself. Otherwise the next reminder takes a number somebody
// already wrote down next to a different one, and finishing by that number
// finishes the wrong thing.
func TestQueueIDsSurviveCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	j.Queue(Item{Text: "older", Desk: "vshop"})
	newest, _ := j.Queue(Item{Text: "newest, and finished", Desk: "vshop"})
	if err := j.Done(newest.ID); err != nil {
		t.Fatal(err)
	}
	if err := j.Compact(); err != nil {
		t.Fatal(err)
	}
	j.Close()

	j, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	next, _ := j.Queue(Item{Text: "after the compaction", Desk: "vshop"})
	if next.ID <= newest.ID {
		t.Errorf("id %d reuses %d, which was handed out before the compaction", next.ID, newest.ID)
	}
}

// An entry that cannot be an item is counted, not shown: a blank row with id 0
// looks like the queue's own fault, and doctor reports the count.
func TestQueueSkipsEntriesThatAreNotItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	if err := os.WriteFile(path, []byte(
		`{"kind":"queued","desk":"vshop"}`+"\n"+
			`{"kind":"queued","id":2,"text":"a real one","desk":"vshop"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if q := j.State().Queue; len(q) != 1 || q[0].Text != "a real one" {
		t.Errorf("queue = %+v, want only the item that is one", q)
	}
	if j.Skipped() != 1 {
		t.Errorf("skipped = %d, want the unusable entry counted", j.Skipped())
	}
}

// A mode the daemon forgets is a mode that lies: nothing arrives, the bar and
// `zde status` both say work, and the person is left looking for a broken app.
// zded restarts on every rebuild that touches it, so this is the ordinary case
// and not the crash case.
func TestModeSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j := open(t, path)
	if err := j.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}
	j.Close()

	if got := open(t, path).State().Mode; got != "quiet" {
		t.Errorf("mode after a restart = %q, want the quiet it was left in", got)
	}
}

// And through a compaction, which rewrites the file as the shortest sequence
// that replays to the same state. Left out of that sequence, the mode would
// reset at whichever moment the journal happened to get long enough - a session
// that goes loud by itself, hours after anybody touched it.
func TestModeSurvivesCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j := open(t, path)
	if err := j.SetMode("focus"); err != nil {
		t.Fatal(err)
	}
	if err := j.Compact(); err != nil {
		t.Fatal(err)
	}
	j.Close()

	if got := open(t, path).State().Mode; got != "focus" {
		t.Errorf("mode after a compaction = %q, want focus", got)
	}
}

// Which desk lent the mode survives a restart, because the mode does. zded
// restarts on every rebuild that touches it, and the two halves are one fact: a
// session that came back in focus but had forgotten whose focus it was would
// carry that focus onto the next desk and never give it back.
func TestWhichDeskLentTheModeSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j := open(t, path)
	if err := j.SetMode("focus"); err != nil {
		t.Fatal(err)
	}
	if err := j.SetBorrowed(Borrowed{Desk: "vshop", Mode: "work"}); err != nil {
		t.Fatal(err)
	}
	j.Close()

	got := open(t, path).Borrowed()
	if got.Desk != "vshop" || got.Mode != "work" {
		t.Errorf("borrowed after a restart = %+v, want vshop holding a work it can give back", got)
	}
}

// And through a compaction, for the reason the mode does: a compaction that
// dropped the lender would leave the borrowed mode in force with nothing to
// return it to, at whichever moment the journal happened to get long enough.
func TestWhichDeskLentTheModeSurvivesCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j := open(t, path)
	if err := j.SetBorrowed(Borrowed{Desk: "vshop", Mode: "quiet"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Compact(); err != nil {
		t.Fatal(err)
	}
	j.Close()

	if got := open(t, path).State().Borrowed; got.Desk != "vshop" || got.Mode != "quiet" {
		t.Errorf("borrowed after a compaction = %+v, want vshop holding a quiet", got)
	}
}

// Nobody holding the mode is a state that gets written down, not one inferred
// from silence: it is where a mode chosen by hand leaves things, and a replay
// that skipped the empty entry would have the desk still holding a mode it gave
// up - which comes back the next time you walk off that desk.
func TestGivingTheModeBackIsRecordedRatherThanInferred(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j := open(t, path)
	if err := j.SetBorrowed(Borrowed{Desk: "vshop", Mode: "work"}); err != nil {
		t.Fatal(err)
	}
	if err := j.SetBorrowed(Borrowed{}); err != nil {
		t.Fatal(err)
	}
	j.Close()

	reopened := open(t, path)
	if got := reopened.Borrowed(); got.Desk != "" {
		t.Errorf("borrowed after it was given back = %+v, want nobody holding it", got)
	}
	if n := reopened.Skipped(); n != 0 {
		t.Errorf("%d entries were skipped, and giving the mode back is not a torn line", n)
	}
}

// An id claimed for something that never reached the queue is spent for good.
// Handing it out again would let an app that still holds that number close
// whatever ends up with it - a reminder somebody typed, most likely, since the
// two share one counter (internal/attn scopes replaces to the sender for the
// same reason).
func TestClaimedIDsAreNeverHandedOutAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j := open(t, path)
	claimed, err := j.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	queued, err := j.Queue(Item{Text: "after it"})
	if err != nil {
		t.Fatal(err)
	}
	if queued.ID == claimed {
		t.Fatalf("the queue took id %d, which was already claimed", claimed)
	}
	// And across a restart, which is where a counter kept only in memory would
	// start again from whatever is still waiting.
	j.Close()
	again := open(t, path)
	next, err := again.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	if next == claimed || next == queued.ID {
		t.Errorf("id %d after a restart repeats %d or %d", next, claimed, queued.ID)
	}
}

// A compaction with nothing waiting must still write the counter down.
//
// This is the crossing neither of the other two makes: the id test never
// compacts, and the compaction test leaves an item on the queue. So the line
// that saves the counter could be made conditional on the queue having
// something in it and both would stay green - while quiet mode is exactly that
// state, nothing ever queued and every arrival spending an id. Compaction is
// automatic at Open past a thousand entries, so this is not a rare shape: it is
// what a night in quiet mode looks like, and the id it hands out afterwards is
// one an app is still holding.
func TestClaimedIDsSurviveACompactionOfAnEmptyQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	j := open(t, path)
	claimed, err := j.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	if q := j.State().Queue; len(q) != 0 {
		t.Fatalf("queue = %+v, want nothing waiting: this test is about the empty case", q)
	}
	if err := j.Compact(); err != nil {
		t.Fatal(err)
	}
	j.Close()

	again := open(t, path)
	next, err := again.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	if next <= claimed {
		t.Errorf("id %d after a compaction repeats %d, which an app is still holding", next, claimed)
	}
}

// Reading one field costs one field, whatever else the journal is remembering.
//
// The bar asks for the queue every two seconds and for the mode every two
// seconds after that, which is sixty questions a minute for as long as the
// session runs. Both of them used to go through State, which copies everything:
// the queue, and a map of desks with a map of monitors inside each. So the cost
// of the cheapest question in the daemon grew with how long somebody had been
// using their machine.
//
// Measured as allocations rather than as time, and by comparison rather than
// against a number: what is wrong with State here is that its cost follows the
// size of what is remembered, so the test is that these two do not.
func TestReadingOneFieldDoesNotCopyTheWholeJournal(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "j.jsonl"))
	if _, err := j.Queue(Item{Text: "reply to ilya", Desk: "vshop"}); err != nil {
		t.Fatal(err)
	}
	if err := j.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}

	small := testing.AllocsPerRun(100, func() { j.Waiting(); j.Mode() })

	// A session that has been running a while: a hundred desks, each with a
	// remembered workspace per monitor. Written straight into the state rather
	// than through the file, because this is about what a read copies and not
	// about what a replay produces.
	j.mu.Lock()
	for i := 0; i < 100; i++ {
		j.state.LastActive["desk"+strconv.Itoa(i)] = map[string]string{"DP-1": "code", "DP-2": "web"}
	}
	j.mu.Unlock()

	big := testing.AllocsPerRun(100, func() { j.Waiting(); j.Mode() })
	if big != small {
		t.Errorf("reading the queue and the mode costs %v allocations with a hundred desks remembered and %v with none: it is copying the whole state to answer with one field",
			big, small)
	}
}

// And it is still a copy: a caller that could reach back in here through the
// slice it was handed would be able to take something off the queue without
// writing a line for it.
func TestWaitingHandsBackACopy(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "j.jsonl"))
	if _, err := j.Queue(Item{Text: "reply to ilya"}); err != nil {
		t.Fatal(err)
	}
	got := j.Waiting()
	got[0].Text = "something else entirely"
	if again := j.Waiting(); again[0].Text != "reply to ilya" {
		t.Errorf("the queue now says %q, edited through what a reader was handed", again[0].Text)
	}
}

// The mode is the whole of what keeps this file to the person it belongs to.
//
// The queue in here is the summary of every notification that reached it, and
// the directory above it is not always a private one - XDG_STATE_HOME goes
// wherever it is pointed and DefaultPath falls back to /tmp (see journalMode).
// Checked after a compaction as well, because compaction writes a new file and
// renames it over this one: a mode set only at Open would hold until the
// journal got long enough to be rewritten, and then quietly stop holding.
func TestTheJournalAndTheDirectoryZdeMakesForItAreReadableByNobodyElse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "zde")
	path := filepath.Join(dir, "journal.jsonl")
	j := open(t, path)
	if err := j.SetLastDesk("vshop"); err != nil {
		t.Fatal(err)
	}

	if got := mode(t, path); got != journalMode {
		t.Errorf("a new journal is %04o, and anything wider than %04o is somebody else's read of your notifications", got, journalMode)
	}
	if got := mode(t, dir); got != stateDirMode {
		t.Errorf("the directory zde made for it is %04o, want %04o", got, stateDirMode)
	}

	if err := j.Compact(); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, path); got != journalMode {
		t.Errorf("a compacted journal is %04o: the rewrite widened it back to what anybody can read", got)
	}
}

// The machine that has been running zde since before this was fixed.
//
// Its journal is 0644 and full of what it has been told since login, and a fix
// that reached only the files it creates would leave that one exactly as it was
// and call the problem solved. The directory goes with it, but only because it
// is the one zde chose for itself - see the test below.
func TestAJournalAnEarlierZdeLeftReadableIsTightenedWhenItIsOpened(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(
		`{"kind":"queued","id":7,"text":"ilya: about the invoice","desk":"vshop"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Said again rather than left to WriteFile, whose mode is masked by whatever
	// umask the test is running under: this has to start wide to prove anything.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	j := open(t, path)
	if got := mode(t, path); got != journalMode {
		t.Errorf("a journal that was already there is still %04o, so every notification an earlier zde wrote down is still readable by anybody with an account here", got)
	}
	if got := mode(t, filepath.Dir(path)); got != stateDirMode {
		t.Errorf("zde's own state directory is still %04o, want %04o", got, stateDirMode)
	}
	// And it is still the journal it was. Tightening a file is not a reason to
	// forget what somebody owes.
	if q := j.State().Queue; len(q) != 1 || q[0].Text != "ilya: about the invoice" {
		t.Errorf("queue = %+v, want what the older journal had on it", q)
	}
}

// A directory somebody named is not zde's to take private.
//
// `zded -journal /tmp/live.jsonl` is what the smoke test runs, and it puts the
// journal in a directory belonging to the whole machine. Chmodding that would
// do far more harm than the listing it prevents, and it would do it to a
// directory zde does not own. The file's own mode is what keeps the lines
// unreadable, and that one is set wherever the journal was put.
func TestADirectorySomebodyElseNamedIsLeftAlone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(shared, "live.jsonl")
	open(t, path)

	if got := mode(t, shared); got != 0o755 {
		t.Errorf("a directory zde was pointed at is now %04o: it took a shared directory private on its way past", got)
	}
	if got := mode(t, path); got != journalMode {
		t.Errorf("the journal in it is %04o, want %04o wherever it was put", got, journalMode)
	}
}

// The other half of the same upgrade: what the earlier zde already wrote.
//
// It put the whole of every notification in here, so tightening the mode alone
// leaves a file that is private and still full of somebody's mail. Compaction
// runs at Open and nowhere else, so this is the one chance to rewrite it, and a
// journal that carried a body is rewritten whatever its length.
func TestAJournalWrittenByAnEarlierZdeLosesTheNotificationBodiesInIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte(
		`{"kind":"queued","id":1,"text":"your results are in","body":"the biopsy came back clear","from":"clinic"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	j := open(t, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "the biopsy came back clear") {
		t.Error("the journal still holds a body an older zde wrote, so this protects the next notification and none of the ones already on the disk")
	}
	// The item itself stays. What is owed is not the part being taken away.
	if q := j.State().Queue; len(q) != 1 || q[0].Text != "your results are in" || q[0].From != "clinic" {
		t.Errorf("queue = %+v, want the item that was waiting, minus the message", q)
	}
	if !strings.Contains(string(raw), "your results are in") {
		t.Error("the rewrite dropped the item as well as the body: a message is worth protecting, an empty queue is not")
	}
}

// A symlink where the journal should be is refused, not followed.
//
// Without O_NOFOLLOW the open lands on whatever the link points at, and then
// everything downstream is right about the wrong file: the descriptor chmod
// tightens the target, and a session's worth of notification summaries is
// appended to a file somebody else chose. ELOOP instead, and the target is
// left exactly as it was found.
func TestASymlinkAtTheJournalItselfIsRefusedRatherThanFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "somebody-elses.jsonl")
	if err := os.WriteFile(target, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Past the umask, so that a chmod landing here would be visible.
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.jsonl")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	j, err := Open(path)
	if err == nil {
		j.Close()
		t.Fatal("opened a journal through a symlink: everything after this is done to a file somebody else named")
	}
	if got := mode(t, target); got != 0o644 {
		t.Errorf("the far end of the link is now %04o: the chmod went through the symlink", got)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "theirs\n" {
		t.Errorf("the far end of the link now holds %q: zde wrote through the symlink", raw)
	}
}

// A FIFO where the journal should be is refused, and refused now.
//
// This is the shape of the bug rather than a variation on the one above. The
// symlink was refused by the write open, which comes second; the read open
// came first and was a plain os.Open, and a plain os.Open of a FIFO does not
// fail - it waits in the kernel for a writer that is never coming. zded did
// that with its listener unbound and its signal handling not yet reached, so
// SIGTERM and SIGINT were both swallowed and only SIGKILL ended it: one
// `mkfifo ~/.local/state/zde/journal.jsonl` and the account had no desktop,
// through every reboot, with nothing in any log to say why.
//
// The deadline is not decoration. Without the fix this test does not fail, it
// hangs - and a CI job that hangs is a regression nobody gets told about.
func TestAFifoAtTheJournalIsRefusedRatherThanWaitedOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		j, err := Open(path)
		if j != nil {
			j.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("opened a FIFO as a journal")
		}
		if !strings.Contains(err.Error(), "named pipe") {
			t.Errorf("error is %q, and somebody with an unexplained daemon needs it to name what is at that path", err)
		}
	case <-time.After(10 * time.Second):
		// Leaked on purpose: it is blocked in the kernel with nothing to
		// unblock it, and the test binary is on its way out.
		t.Fatal("Open did not return in 10s, which is the hang zded shipped with")
	}
}

// And the symlink that is allowed, which is why O_NOFOLLOW and not something
// that walks the whole path.
//
// A state directory on another disk, reached through a link, is an ordinary
// setup. O_NOFOLLOW constrains the last component only, so it stays ordinary.
func TestAStateDirectoryThatIsItselfASymlinkStillWorks(t *testing.T) {
	onTheOtherDisk := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(onTheOtherDisk, stateDirMode); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(onTheOtherDisk, link); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(link, "journal.jsonl")
	j := open(t, path)
	if err := j.SetLastDesk("vshop"); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, filepath.Join(onTheOtherDisk, "journal.jsonl")); got != journalMode {
		t.Errorf("the journal through a linked directory is %04o, want %04o", got, journalMode)
	}
}

// What a refused chmod means, which is two opposite things.
//
// This is a decision that cannot be arranged on the disk a test runs on: it
// needs a file belonging to another account, or a filesystem with no
// permission bits, and a unit test has neither. So the choice itself is the
// thing under test, and the inputs it is made from are what tighten reads off
// the open descriptor.
func TestAJournalThatIsSomebodyElsesIsFatalAndOneThatCannotHoldAModeIsNot(t *testing.T) {
	refused := errors.New("operation not permitted")

	// Somebody else's. The old code read every refusal this way, which is the
	// half that was right.
	err := chmodRefused("/home/them/.local/state/zde/journal.jsonl", refused, 1001, 1000)
	if err == nil {
		t.Fatal("a journal belonging to another account started anyway, and zde is now appending your notifications to it")
	}
	if !errors.Is(err, refused) {
		t.Errorf("the refusal lost what the kernel said: %v", err)
	}

	// Ours, and the filesystem cannot hold a mode. vfat, exFAT and some 9p and
	// SMB mounts, where refusing to start costs a person the whole session to
	// enforce something the disk was never able to have.
	if err := chmodRefused("/mnt/stick/zde/journal.jsonl", refused, 1000, 1000); err != nil {
		t.Errorf("a journal that is ours on a filesystem with no permission bits stopped the daemon: %v", err)
	}
}

// And where those two numbers come from.
//
// Off the descriptor, not the path: it names the file that was opened, which
// is the same reason the chmod is on the descriptor. A descriptor that cannot
// answer reports -1, which equals no uid, so an unanswerable file is treated
// as somebody else's - the fail-closed way round.
func TestTheOwnerOfAJournalIsReadOffTheOpenDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, journalMode)
	if err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(f); got != os.Getuid() {
		t.Errorf("ownerOf = %d, want %d: a file this process just made is this process's", got, os.Getuid())
	}
	f.Close()
	if got := ownerOf(f); got != -1 {
		t.Errorf("ownerOf = %d on a descriptor that cannot answer, want -1 so that it counts as somebody else's", got)
	}
}

// The queue has a ceiling, and what is already waiting is what survives it.
//
// The one thing in zde that grew without a bound. Everything else that grows
// with what arrives has one (internal/attn, PerSenderMax and bodyMax), and this
// did not: a flood filled a 16 GB tmpfs once, and nothing shortened the file in
// between - compaction keeps whatever is still waiting, so 49 MB of queued
// entries compacted to 49 MB.
//
// Which end gives way is the half worth pinning. Dropping the oldest would let
// one chatty program erase everything real a person owed; refusing the newest
// keeps every promise about what is already there.
func TestTheQueueHasACeilingAndTheOldestSurvivesIt(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.jsonl"))
	first, err := j.Queue(Item{Text: "the one that has waited longest", Desk: "vshop"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < QueueMax; i++ {
		if _, err := j.Queue(Item{Text: "item " + strconv.Itoa(i), Desk: "vshop"}); err != nil {
			t.Fatalf("queueing %d of %d: %v", i, QueueMax, err)
		}
	}
	if n := len(j.Waiting()); n != QueueMax {
		t.Fatalf("%d waiting, want the cap of %d", n, QueueMax)
	}

	if _, err := j.Queue(Item{Text: "one too many", Desk: "vshop"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Queue past the cap = %v, want ErrQueueFull", err)
	}
	q := j.Waiting()
	if len(q) != QueueMax {
		t.Errorf("%d waiting after a refusal, want the cap of %d", len(q), QueueMax)
	}
	if q[0].ID != first.ID {
		t.Errorf("the oldest item is now %d, want %d: the flood pushed out what was owed", q[0].ID, first.ID)
	}

	// And finishing one makes room, so the cap is a ceiling and not a wall.
	if err := j.Done(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Queue(Item{Text: "there is room now", Desk: "vshop"}); err != nil {
		t.Errorf("the queue stayed shut after something was finished: %v", err)
	}
}

// A journal written before the cap loads whole.
//
// Refusing to replay it, or trimming it on the way in, would delete what
// somebody already owes to enforce a number invented afterwards. It comes back
// over the cap and nothing new is taken until it drains.
func TestAJournalLongerThanTheCapStillLoadsWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	var b strings.Builder
	for i := 1; i <= QueueMax+50; i++ {
		b.WriteString(`{"kind":"queued","id":` + strconv.Itoa(i) + `,"text":"owed"}` + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	j := open(t, path)
	if n := len(j.Waiting()); n != QueueMax+50 {
		t.Errorf("%d waiting, want all %d that were written down", n, QueueMax+50)
	}
	if _, err := j.Queue(Item{Text: "not while that is outstanding"}); !errors.Is(err, ErrQueueFull) {
		t.Errorf("Queue = %v, want ErrQueueFull while the queue is over its cap", err)
	}
}

// mode is a path's permission bits and nothing else about it.
func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// A flood leaves a journal a restart can shorten, and that is what the cap
// bought.
//
// The consequence chosen here is the file and not the queue, because the file
// is what the incident was: a flood filled a 16 GB tmpfs and took every shell
// on the machine with it. The queue being long is a nuisance; the queue being
// long on disk, in a file that is only ever rewritten when a daemon starts, is
// the thing that ran a machine out of space. So what is asserted is the size of
// the journal after a compaction - which is exactly the "49 MB compacted to
// 49 MB" that QueueMax's comment says used to happen, measured rather than
// described.
//
// An absolute ceiling and not a comparison against QueueMax, which is the whole
// point: TestTheQueueHasACeilingAndTheOldestSurvivesIt above pins which end
// gives way, and it queues QueueMax items to do it, so the number can be
// anything at all and that test still passes. This one fails when the number
// stops being one a disk can hold.
//
// Eight megabytes, against the two and a half QueueMax's own arithmetic
// arrives at for a thousand items at their ceiling. Three times over, so that
// deciding a queue may hold two or three thousand things is a decision somebody
// can make without this test arguing about it, and eight times over is caught.
//
// The items are as large as an item gets: a summary and a sender at the 300
// characters each that internal/zded clamps them to, written in an alphabet
// that costs four bytes a character. Nothing in this package clamps them - the
// caller does - so building them here is the only way to ask what the worst
// case costs.
func TestAFloodLeavesAJournalThatCompactsToSomethingADiskCanHold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := open(t, path)

	// Four bytes a character, and 300 of them, which is what a queued line is
	// at its ceiling. An emoji in a notification summary is not an exotic case.
	wide := strings.Repeat("\U0001F642", 300)
	// Spelled as a number rather than as QueueMax times eight, so that widening
	// the cap does not widen the flood along with it: a fixture written in
	// terms of the bound it is testing grows to meet whatever the bound became,
	// and here it would also mean eight thousand fsynced appends on the way.
	const flood = 8001
	refused := 0
	for i := 0; i < flood; i++ {
		_, err := j.Queue(Item{Text: wide, From: wide, Desk: "vshop"})
		switch {
		case err == nil:
		case errors.Is(err, ErrQueueFull):
			refused++
		default:
			t.Fatalf("queueing %d of %d: %v", i, flood, err)
		}
	}
	// The rewrite a restart does, which is the only thing that ever shortens
	// this file: Compact has no other caller in the tree, and it runs at Open.
	if err := j.Compact(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 8<<20 {
		t.Errorf("%d arrivals left a journal of %d MB after the compaction a restart does, "+
			"past the 8 MB a bounded queue can cost: this is the file that filled a 16 GB tmpfs",
			flood, fi.Size()>>20)
	}
	// And the queue is still a queue, so the size above is a ceiling something
	// reached rather than a journal that lost what was owed.
	if len(j.Waiting()) == 0 {
		t.Error("nothing is waiting after the flood, so the size above is about an empty queue")
	}
	// Said after the size, and not instead of it: a cap so high that eight
	// thousand arrivals never reach it is a cap that no flood a machine can
	// produce will ever meet, which is the same fault as having none.
	if refused == 0 {
		t.Errorf("%d arrivals and none of them was refused, so nothing here is capping anything", flood)
	}
}
