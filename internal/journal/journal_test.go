package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	first, err := j.Queue("reply to ilya", "vshop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Queue("pay the invoice", "haven"); err != nil {
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
	one, _ := j.Queue("first", "")
	if err := j.Done(one.ID); err != nil {
		t.Fatal(err)
	}
	two, _ := j.Queue("second", "")
	j.Close()

	j, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	three, _ := j.Queue("third", "")
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
	gone, _ := j.Queue("done with this", "vshop")
	j.Queue("still waiting", "vshop")
	j.Queue("also waiting", "haven")
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
	j.Queue("mine", "vshop")
	st := j.State()
	st.Queue[0].Text = "theirs"
	if got := j.State().Queue[0].Text; got != "mine" {
		t.Errorf("journal item = %q, a caller wrote through the copy", got)
	}
}
