package attn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// filled is a history with n arrivals in it, numbered from one, so a test can
// say what it expects to find at either end of the bound.
//
// Spread over senders, because the history is a ring each now: one sender's
// worth of arrivals would be measuring PerSenderMax wherever the test meant to
// measure the whole of it (history.go).
func filled(n int) *History {
	var h History
	for i := 1; i <= n; i++ {
		h.Add(Record{
			ID:   uint64(i),
			From: sender(i),
			Text: "number " + strconv.Itoa(i),
			Body: "the body of number " + strconv.Itoa(i),
			At:   time.Now(),
		})
	}
	return &h
}

// sender is which of the bounded number of senders arrival i came from.
func sender(i int) string { return "app " + strconv.Itoa(i%SendersMax) }

// The question the whole file answers: a person logs out, logs in, presses
// Mod+n, and what was happening before the restart is still there.
//
// Newest first when it is read back, because that is the order the center reads
// in and a restore that put yesterday morning at the top would be worse than no
// restore at all.
func TestWhatArrivedBeforeARestartIsStillThereAfterIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := WriteSnapshot(path, filled(3).Snapshot()); err != nil {
		t.Fatal(err)
	}

	records, err := ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	var next History
	next.Restore(records)
	seen := next.Recent()
	if len(seen) != 3 {
		t.Fatalf("the history came back as %+v, want the three that were written", seen)
	}
	if seen[0].ID != 3 || seen[2].ID != 1 {
		t.Errorf("it reads %d, %d, %d: not newest first", seen[0].ID, seen[1].ID, seen[2].ID)
	}
	if seen[0].Text != "number 3" || seen[0].Body != "the body of number 3" || seen[0].From != sender(3) {
		t.Errorf("record = %+v, want what was sent, not a headline", seen[0])
	}
	if !seen[0].Restored {
		t.Error("a record off the disk does not say it came from before this session, so the center will draw it as live")
	}
}

// Only the newest few. The rings hold a day or two of every sender because
// that is the span a session's "what did I miss" asks about; the file is
// snapshotMax because across a restart the question is what was happening when
// it ended, and everything before that is archaeology somebody has to scroll
// past.
func TestASnapshotKeepsOnlyTheNewestFewOfTheHistory(t *testing.T) {
	full := SendersMax * PerSenderMax
	recs := filled(full).Snapshot()
	if len(recs) != snapshotMax {
		t.Fatalf("the snapshot holds %d records, want the bound of %d", len(recs), snapshotMax)
	}
	// Oldest first in the file, which is the order the history is read back in:
	// Restore puts them back without reversing anything.
	if recs[0].ID != uint64(full-snapshotMax+1) {
		t.Errorf("the oldest kept is %d, want %d: it should be the newest snapshotMax and no more", recs[0].ID, full-snapshotMax+1)
	}
	if recs[len(recs)-1].ID != uint64(full) {
		t.Errorf("the newest kept is %d, want the last one added", recs[len(recs)-1].ID)
	}
}

// The file answers the same question the center does, so it is filled the same
// way the center is read: the newest across every sender, and not the newest
// of one.
//
// The trap is a snapshot that walked the rings instead of the history. It
// would come back the right length and hold one app's whole morning, which is
// the thing a per-sender history could quietly turn a restart into.
func TestASnapshotTakesTheNewestAcrossEverySender(t *testing.T) {
	var h History
	senders := []string{"mail", "curl", "chat"}
	id := uint64(0)
	for round := 0; round < PerSenderMax; round++ {
		for _, from := range senders {
			id++
			h.Add(Record{ID: id, From: from, Text: "number " + strconv.FormatUint(id, 10), At: time.Now()})
		}
	}

	recs := h.Snapshot()
	if len(recs) != snapshotMax {
		t.Fatalf("the snapshot holds %d records, want the bound of %d", len(recs), snapshotMax)
	}
	if recs[0].ID != id-snapshotMax+1 || recs[len(recs)-1].ID != id {
		t.Errorf("the file runs %d to %d, want the newest %d of everything that arrived", recs[0].ID, recs[len(recs)-1].ID, snapshotMax)
	}
	written := map[string]bool{}
	for _, r := range recs {
		written[r.From] = true
	}
	if len(written) != len(senders) {
		t.Errorf("the file holds %d senders, want all %d: the newest across them, not one sender's newest", len(written), len(senders))
	}
}

// A row whose body was cut must not pretend to be whole. The bodies are what
// make a history large, so most of one stays in RAM and dies with the daemon -
// and a record that came back with the front of a message and said nothing
// about it would be the one place zde shows less than the app sent without
// admitting it (docs/vision.md, principle 3).
func TestARestoredBodyThatWasCutSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	long := strings.Repeat("é", bodyMax)
	var h History
	h.Add(Record{ID: 1, Text: "short body", Body: "two words"})
	h.Add(Record{ID: 2, Text: "long body", Body: long})
	if err := WriteSnapshot(path, h.Snapshot()); err != nil {
		t.Fatal(err)
	}

	records, err := ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("read %+v, want both records", records)
	}
	if records[0].Clipped {
		t.Errorf("a body of %q says it was cut", records[0].Body)
	}
	cut := records[1]
	if !cut.Clipped {
		t.Error("a body cut to the snapshot's bound came back claiming to be the whole message")
	}
	if n := utf8.RuneCountInString(cut.Body); n != snapshotBodyMax {
		t.Errorf("the kept body is %d characters, want the bound of %d - counted in characters, or a message in Cyrillic is cut to a quarter of one", n, snapshotBodyMax)
	}
}

// The invariant. A desk declared private is somebody saying that what arrives
// there is not to be left lying around, and a notification body in a file is
// exactly that: it outlives the session and anything that can read the state
// directory can read it (docs/vision.md, section 3).
//
// Asserted against the bytes of the file rather than against what comes back
// out of it, because what is on the disk is the whole of what this promises.
func TestWhatArrivedOnAPrivateDeskNeverReachesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	var h History
	h.Add(Record{ID: 1, Text: "the build failed", Body: "on the second stage", Desk: "work"})
	h.Add(Record{ID: 2, Text: "clinic appointment", Body: "results are back", Desk: "private", Private: true})
	if err := WriteSnapshot(path, h.Snapshot()); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"clinic appointment", "results are back"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%q is in the snapshot file, and it arrived on a private desk", secret)
		}
	}
	if !strings.Contains(string(raw), "the build failed") {
		t.Error("nothing was written at all: the private one should be left out, not take the rest with it")
	}
}

// A file is a file: between two sessions anything can have edited it, and what
// it says about a record is not what decides what that record is. A snapshot
// that could mark its own rows live, or list actions, would be a way to make
// the center offer buttons for a session that has ended.
func TestASnapshotCannotClaimItsRowsAreLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	forged, err := json.Marshal(map[string]any{
		"version": snapshotVersion,
		"records": []map[string]any{{
			"id":          7,
			"text":        "your account needs attention",
			"restored":    false,
			"bodyClipped": false,
			"body":        strings.Repeat("x", bodyMax),
			"actions":     []map[string]string{{"key": "open", "label": "Open"}},
			"moreActions": 3,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, forged, 0o600); err != nil {
		t.Fatal(err)
	}

	records, err := ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("read %+v, want the one record", records)
	}
	r := records[0]
	if !r.Restored {
		t.Error("a record off the disk claimed to be from this session, and the center will draw it as live")
	}
	if !r.Clipped {
		t.Error("a body past the bound came back claiming to be whole")
	}
	if len(r.Actions) != 0 || r.Extra != 0 {
		t.Errorf("actions = %+v (+%d): a file must not be able to put buttons on a row whose app is gone", r.Actions, r.Extra)
	}
}

// History is not the session. A snapshot that cannot be read costs a person the
// answer to "what did I miss" and nothing else - the desks, the queue and every
// keybind are on the other side of the daemon starting, so every one of these
// has to come back empty rather than failing.
func TestASnapshotThatCannotBeReadCostsTheHistoryAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	newer, err := json.Marshal(snapshot{Version: snapshotVersion + 1, Records: []Record{{ID: 1, Text: "from a newer zde"}}})
	if err != nil {
		t.Fatal(err)
	}
	whole, err := json.Marshal(snapshot{Version: snapshotVersion, Records: []Record{{ID: 1, Text: "a whole one"}}})
	if err != nil {
		t.Fatal(err)
	}

	broken := map[string][]byte{
		"garbage.json":   []byte("this is not json at all\n"),
		"truncated.json": whole[:len(whole)/2],
		"newer.json":     newer,
		"empty.json":     nil,
		// A record whose id is zero addresses nothing and one with no summary
		// draws a blank row: both are things only an edited file holds.
		"useless.json": []byte(`{"version":` + strconv.Itoa(snapshotVersion) + `,"records":[{"id":0,"text":"no id"},{"id":4,"text":""}]}`),
	}
	for name, data := range broken {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		records, err := ReadSnapshot(path)
		if len(records) != 0 {
			t.Errorf("%s came back with %+v, and none of it can be trusted", name, records)
		}
		if err == nil && name != "useless.json" {
			t.Errorf("%s was read without a word, so nobody would ever find out it is unreadable", name)
		}
	}

	// A path that is not a readable file at all, which is what a state
	// directory somebody has been tidying looks like.
	if records, err := ReadSnapshot(dir); len(records) != 0 || err == nil {
		t.Errorf("reading a directory answered %+v, %v: want nothing, and a reason", records, err)
	}
	// And the ordinary one: no file yet, which is every first login. Not an
	// error, or a fresh machine prints a failure at every startup.
	records, err := ReadSnapshot(filepath.Join(dir, "not-there.json"))
	if len(records) != 0 || err != nil {
		t.Errorf("a missing snapshot answered %+v, %v: want nothing, and no complaint", records, err)
	}
}

// An enormous file is not read into the daemon's memory to find out what it is.
// Forty records at their limit is under 250 KB, so anything past a megabyte was
// not written by this zde, and finding that out by allocating it is the failure
// the bound exists to stop.
//
// Written as a snapshot that is perfectly well formed and simply too big, so
// that what refuses it can only be the size. A file of rubbish would be turned
// away by the parser and prove nothing about the bound - and the complaint has
// to name the size for the same reason: a person reading "invalid character" in
// the log would go looking for the wrong problem.
func TestASnapshotTooLargeToBeOneIsNotReadIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	huge, err := json.Marshal(snapshot{Version: snapshotVersion, Records: []Record{{
		ID:   1,
		Text: "a well formed record, and far too much of it",
		Body: strings.Repeat("x", snapshotBytesMax+1),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, huge, 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := ReadSnapshot(path)
	if len(records) != 0 {
		t.Errorf("read %d records out of a file too big to be a snapshot", len(records))
	}
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("the complaint is %v, and it should say the file is too large rather than blame its shape", err)
	}
}

// These are notification bodies: somebody's mail, their two-factor codes, the
// subject lines of everything they were sent while they were away. The journal
// beside it holds window positions and is 0644; this one is nobody's but its
// owner's.
func TestTheSnapshotIsReadableOnlyByWhoeverItIsAbout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zde", "history.json")
	if err := WriteSnapshot(path, filled(1).Snapshot()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the snapshot is %04o, want 0600: it is a file of notification bodies", perm)
	}
	// Written over an existing one, which is what every save after the first
	// does - the mode has to survive the rename as well as the create.
	if err := WriteSnapshot(path, filled(2).Snapshot()); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("after a rewrite the snapshot is %04o, want 0600", perm)
	}
}
