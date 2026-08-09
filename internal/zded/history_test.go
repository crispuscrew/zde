package zded

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

// historyServer is a daemon standing on one desk of a directory of manifests,
// with a journal under it so that arrivals can be recorded. The manifests are
// the point: whether a desk says it is private is the one thing that decides
// what may be written down.
func historyServer(t *testing.T, focused string, manifests map[string]string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	desks := filepath.Join(dir, "desks")
	if err := os.MkdirAll(desks, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range manifests {
		if err := os.WriteFile(filepath.Join(desks, name+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	jrn, err := journal.Open(filepath.Join(dir, "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	m := desk.Rebuild([]desk.Workspace{
		{Name: "work.DP-1.code", Output: "DP-1"},
		{Name: "clinic.DP-1.mail", Output: "DP-1"},
	}, []string{"DP-1"})
	niri := &fakeCompositor{m: m, focused: focused, output: "DP-1"}
	return New("test", jrn, niri, manifest.Dir(desks)), filepath.Join(dir, "history.json")
}

const (
	openDesk    = "name: work\nmonitors: { DP-1: { workspaces: [code] } }\n"
	privateDesk = "name: clinic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n"
)

// The invariant, at the level where it is decided: the daemon knows which desk
// a notification arrived on, and a desk that says it is private is a desk whose
// arrivals stay in memory (docs/vision.md, section 3).
//
// Both halves in one test, because either alone would pass on a bug: writing
// nothing at all keeps every secret, and writing everything keeps none.
func TestANotificationFromAPrivateDeskNeverReachesTheSnapshot(t *testing.T) {
	s, path := historyServer(t, "clinic.DP-1.mail", map[string]string{"work": openDesk, "clinic": privateDesk})
	if _, err := s.Arrived(attn.Notification{From: "mail", Text: "your results are in", Body: "the clinic wrote back"}); err != nil {
		t.Fatal(err)
	}
	// And one from the ordinary desk, so the file has something in it.
	s.niri.(*fakeCompositor).focused = "work.DP-1.code"
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed", Body: "on the second stage"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHistory(path); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"your results are in", "the clinic wrote back", "mail"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%q reached the snapshot, and it arrived on a desk declared private", secret)
		}
	}
	if !strings.Contains(string(raw), "the build failed") {
		t.Error("nothing was written: a private desk should cost its own arrivals, not everyone's")
	}
	// The center still has both. Display policy, never data policy: private
	// means it is not written down, not that it did not happen.
	if seen := s.history.Recent(); len(seen) != 2 {
		t.Errorf("the history holds %+v, want both arrivals", seen)
	}
}

// Fail closed, which is principle 9. A notification the daemon cannot place -
// niri unreadable, or a fresh session where nothing has been named yet - could
// have arrived on the private desk, and there is no way to find out afterwards.
// On a machine that declares one, that doubt costs the record its place in the
// file; on a machine that declares none there is nothing to protect, and the
// record is kept.
func TestAnArrivalNobodyCanPlaceIsKeptOffTheDiskWhereADeskIsPrivate(t *testing.T) {
	nowhere := attn.Notification{From: "app", Text: "arrived from nowhere", Body: "with a desk nobody could name"}

	guarded, path := historyServer(t, "", map[string]string{"work": openDesk, "clinic": privateDesk})
	guarded.niri.(*fakeCompositor).err = errors.New("niri is not answering")
	if _, err := guarded.Arrived(nowhere); err != nil {
		t.Fatal(err)
	}
	if err := guarded.SaveHistory(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "arrived from nowhere") {
		t.Error("a notification that could not be placed was written down on a machine with a private desk")
	}

	// The ordinary machine, which declares no private desk at all: the same
	// unplaceable arrival is kept, because refusing it would protect nothing.
	plain, plainPath := historyServer(t, "", map[string]string{"work": openDesk})
	plain.niri.(*fakeCompositor).err = errors.New("niri is not answering")
	if _, err := plain.Arrived(nowhere); err != nil {
		t.Fatal(err)
	}
	if err := plain.SaveHistory(plainPath); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "arrived from nowhere") {
		t.Error("an unplaceable arrival was dropped on a machine where no desk is private, which protects nobody from anything")
	}
}

// A typo must not be how a desk stops being private.
//
// A manifest that will not parse is one desk's problem and not the machine's
// (internal/manifest, LoadDir), so the desk carries on as whatever has been
// named into it - which means nothing can say any more that it was declared
// private. The file it could not read is in `zde status` either way (see
// rememberProblems), so this is loud rather than quiet.
func TestABrokenManifestIsNotHowADeskStopsBeingPrivate(t *testing.T) {
	s, path := historyServer(t, "clinic.DP-1.mail", map[string]string{
		"work":   openDesk,
		"clinic": "name: clinic\nprivate: true\nmonitors: [this is not a monitor map]\n",
	})
	if _, err := s.Arrived(attn.Notification{From: "mail", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHistory(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "the build failed") {
		t.Error("an arrival on a desk whose manifest would not parse was written down, so a typo is all it takes for a private desk to stop being one")
	}
}

// A second manifest naming a desk that is already declared is not how the
// first one's `private: true` disappears.
//
// The manifests are keyed on the name inside the file and the first in
// directory order wins, with the loser reported as a problem
// (internal/manifest, LoadDir). So a copy of a private desk's manifest that
// leaves the flag out decides the question by alphabetical order - and the
// copy is a file zde itself used to write, since `zde desk snapshot` did not
// carry the flag through. Both files are refused instead: the one that lost
// could have been the private declaration, and nothing here can tell which.
func TestASecondManifestForADeskIsNotHowItStopsBeingPrivate(t *testing.T) {
	s, path := historyServer(t, "clinic.DP-1.mail", map[string]string{
		// Sorts first, so it is the one LoadDir keeps: this is the copy
		// winning, which is the case that has to be refused.
		"aa-clinic": "name: clinic\nmonitors: { DP-1: { workspaces: [mail] } }\n",
		"clinic":    privateDesk,
	})
	if _, err := s.Arrived(attn.Notification{From: "mail", Text: "your results are in"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHistory(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "your results are in") {
		t.Error("a duplicate manifest that says nothing about privacy was enough to write a private desk's arrivals to disk")
	}
}

// And the file zde writes itself says so, which is the other half of the same
// defect: a snapshot that dropped the flag is how the duplicate above gets on
// to a machine in the first place.
//
// The existing manifest is under a filename of its own here because that is the
// only shape this can happen in - Save refuses to overwrite the file it would
// write, so a desk declared in `clinic.yaml` cannot be snapshotted over at all.
func TestASnapshotOfAPrivateDeskWritesItDownAsPrivate(t *testing.T) {
	s, _ := historyServer(t, "clinic.DP-1.mail", map[string]string{"aa-clinic": privateDesk})
	resp := s.Dispatch(Request{Method: "desk.snapshot", Args: []string{"clinic"}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var path string
	if err := json.Unmarshal(resp.Ok, &path); err != nil {
		t.Fatal(err)
	}
	written, err := manifest.Load(path)
	if err != nil {
		t.Fatalf("what snapshot wrote does not load: %v", err)
	}
	if !written.Private {
		t.Errorf("%s does not say the desk is private, so taking a snapshot of a private desk is how it stops being one", path)
	}
}

// The regulars are reachable from every desk and cannot be declared by any
// manifest (internal/manifest, check), so they are the one name that is
// certainly not the private desk. Answering them with the doubt owed to an
// undeclared desk cost every arrival on them - most of a day, for somebody who
// works out of the regulars - on any machine that declares one private desk
// anywhere.
func TestTheRegularsAreNotTreatedAsAPrivateDesk(t *testing.T) {
	s, path := historyServer(t, "regulars.DP-1.2", map[string]string{"work": openDesk, "clinic": privateDesk})
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHistory(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "the build failed") {
		t.Error("an arrival on the regulars was kept off the disk, and no manifest can declare the regulars private")
	}
	if st := s.status(); st.Unplaced != 0 {
		t.Errorf("status counts %d unplaced arrivals, and the regulars are a desk zde named itself", st.Unplaced)
	}
}

// What the fail-closed answer costs is counted and said. Otherwise it is a
// notification history that will not fill up, on a machine where nothing is
// wrong with the manifests and nothing appears in any log: the daemon refusing
// to write things down looks exactly like the feature not working.
func TestAnArrivalNobodyCanPlaceIsCountedWhereADeskIsPrivate(t *testing.T) {
	guarded, _ := historyServer(t, "", map[string]string{"work": openDesk, "clinic": privateDesk})
	guarded.niri.(*fakeCompositor).err = errors.New("niri is not answering")
	if _, err := guarded.Arrived(attn.Notification{From: "app", Text: "arrived from nowhere"}); err != nil {
		t.Fatal(err)
	}
	if st := guarded.status(); st.Unplaced != 1 {
		t.Errorf("status counts %d unplaced arrivals, want the one it would not write down", st.Unplaced)
	}

	// And on the ordinary machine there is nothing to count: the same arrival
	// is kept, so nothing was refused and saying otherwise would send somebody
	// looking for a problem they do not have.
	plain, _ := historyServer(t, "", map[string]string{"work": openDesk})
	plain.niri.(*fakeCompositor).err = errors.New("niri is not answering")
	if _, err := plain.Arrived(attn.Notification{From: "app", Text: "arrived from nowhere"}); err != nil {
		t.Fatal(err)
	}
	if st := plain.status(); st.Unplaced != 0 {
		t.Errorf("status counts %d unplaced arrivals on a machine where no desk is private", st.Unplaced)
	}
}

// unreadableDesks is a manifest directory that cannot be read at all: no
// permission, a mount that went away, a home directory that is not there yet.
// It answers an error, which is the one thing internal/manifest reserves for
// the directory itself rather than for one file in it (LoadDir).
type unreadableDesks struct{}

func (unreadableDesks) All() (map[string]*manifest.Desk, []manifest.Problem, error) {
	return nil, nil, errors.New("desks: permission denied")
}

func (unreadableDesks) Save(*manifest.Desk) (string, error) {
	return "", errors.New("desks: permission denied")
}

// A daemon that cannot read the manifests cannot say which desk is private, and
// what it does with that is refuse to write anything down. It is the same
// fail-closed rule as the rest of privateArrival, at the one point where the
// doubt covers every desk at once: the alternative is a directory that went
// unreadable for a minute being how a private desk's bodies reach the disk.
func TestManifestsThatCannotBeReadAtAllKeepEveryArrivalOffTheDisk(t *testing.T) {
	s, path := historyServer(t, "work.DP-1.code", map[string]string{"work": openDesk})
	s.desks = unreadableDesks{}
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHistory(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "the build failed") {
		t.Error("an arrival was written down while nothing could read the manifests, so nothing could know whether its desk was private")
	}
	// And it is still in the center: private means it is not written down, not
	// that it did not happen.
	if seen := s.history.Recent(); len(seen) != 1 {
		t.Errorf("the history holds %+v, want the arrival", seen)
	}
}

// The whole point, end to end: a daemon writes what it has, and the next one
// comes up with it. This is the answer to "what did I miss" surviving the
// rebuild that restarts zded, which is the common way a session ends.
func TestTheNotificationCenterComesBackAfterTheDaemonRestarts(t *testing.T) {
	first, path := historyServer(t, "work.DP-1.code", map[string]string{"work": openDesk})
	if _, err := first.Arrived(attn.Notification{From: "ci", Text: "the build failed", Body: "on the second stage"}); err != nil {
		t.Fatal(err)
	}
	if err := first.SaveHistory(path); err != nil {
		t.Fatal(err)
	}

	second, _ := historyServer(t, "work.DP-1.code", map[string]string{"work": openDesk})
	if err := second.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	var center Center
	resp := second.Dispatch(Request{Method: "attn.center"})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if err := json.Unmarshal(resp.Ok, &center); err != nil {
		t.Fatal(err)
	}
	if len(center.Notifications) != 1 {
		t.Fatalf("the center shows %+v, want what the last session was sent", center.Notifications)
	}
	r := center.Notifications[0]
	if r.Text != "the build failed" || r.Body != "on the second stage" {
		t.Errorf("record = %+v, want what was sent", r)
	}
	if !r.Restored {
		t.Error("the row does not say it is from before the restart, so the center will draw it as live")
	}
	// And nothing can be pressed on it, in words that do not blame the app for
	// what a restart did.
	invoke := second.Dispatch(Request{Method: "attn.invoke", Args: []string{"1", "open"}})
	if invoke.Error == "" {
		t.Fatal("an action was invoked on a record whose sender is not on this session's bus")
	}
	if !strings.Contains(invoke.Error, "before this session started") {
		t.Errorf("refusal %q does not say why there is nothing to press", invoke.Error)
	}
}

// A snapshot that cannot be read is a session that starts with an empty center,
// and nothing else. The daemon is the session (docs/vision.md, section 2): the
// desks, the queue and every keybind are behind it starting.
func TestASnapshotThatWillNotLoadDoesNotStopTheDaemon(t *testing.T) {
	s, path := historyServer(t, "work.DP-1.code", map[string]string{"work": openDesk})
	if err := os.WriteFile(path, []byte("{\"version\":1,\"records\":[{\"id\""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadHistory(path); err == nil {
		t.Error("a torn snapshot loaded without a word, so nobody would ever find out")
	}
	if seen := s.history.Recent(); len(seen) != 0 {
		t.Errorf("the history holds %+v after a torn snapshot", seen)
	}
	// The daemon answers, and takes notifications, which is the whole of what
	// this has to prove.
	if resp := s.Dispatch(Request{Method: "status"}); resp.Error != "" {
		t.Fatalf("the daemon will not answer after a bad snapshot: %v", resp.Error)
	}
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatalf("nothing can arrive after a bad snapshot: %v", err)
	}
}

// An idle session should not touch the disk on a clock. The file is rewritten
// whole, so a machine where nothing has arrived since the last write would be
// writing the same bytes every two minutes for the rest of the afternoon -
// which keeps a disk awake and wears a stick of flash for nothing.
func TestAnIdleSessionStopsRewritingTheSameSnapshot(t *testing.T) {
	s, path := historyServer(t, "work.DP-1.code", map[string]string{"work": openDesk})
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHistory(path); err != nil {
		t.Fatal(err)
	}
	// Taken away rather than compared: two writes a moment apart produce the
	// same bytes and, on a coarse clock, the same timestamps, so the only
	// answer nothing can fake is whether the file came back.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHistory(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the snapshot was written again with nothing changed since the last one")
	}

	// And it starts again the moment something happens, or the file would be
	// stuck at whatever the session's first write said.
	if _, err := s.Arrived(attn.Notification{From: "chat", Text: "lunch?"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHistory(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("nothing was written after an arrival: %v", err)
	}
	if !strings.Contains(string(raw), "lunch?") {
		t.Errorf("the snapshot is %s, want the arrival that came after the last write", raw)
	}
}

// The last write is the one that matters, and the daemon has to wait for it: a
// logout is the ordinary way a session ends, so it must not be the ordinary way
// the last two minutes of it are lost. KeepHistory writing in its own goroutine
// and returning first would be a race nobody would ever see fail.
func TestTheLastSnapshotIsWrittenBeforeTheDaemonStops(t *testing.T) {
	s, path := historyServer(t, "work.DP-1.code", map[string]string{"work": openDesk})
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	s.KeepHistory(ctx, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the daemon stopped without writing the history: %v", err)
	}
	if !strings.Contains(string(raw), "the build failed") {
		t.Errorf("the snapshot is %s, want what had arrived by the time it stopped", raw)
	}
}

// A queue at its ceiling still lands the notification.
//
// The queue is bounded now (internal/journal, QueueMax), and the bound has to
// be a statement about the queue and not about arrivals: what a full queue
// refuses is a place in the list, and everything else a notification gets - the
// record, the popup, and the id its sender addresses it by - it still gets.
// Getting this wrong would mean a flood that fills the queue also silences the
// machine, which is a worse failure than the one the cap is for, and it is the
// difference between an error from the journal and this particular one.
func TestAFullQueueStillRecordsAndNumbersWhatArrives(t *testing.T) {
	s, _ := historyServer(t, "work.DP-1.code", map[string]string{"work": openDesk})
	for i := 0; i < journal.QueueMax; i++ {
		if _, err := s.Arrived(attn.Notification{From: "ci", Text: "build " + strconv.Itoa(i)}); err != nil {
			t.Fatalf("filling the queue at %d: %v", i, err)
		}
	}
	if n := len(s.jrn.Waiting()); n != journal.QueueMax {
		t.Fatalf("%d waiting, want the cap of %d", n, journal.QueueMax)
	}

	id, err := s.Arrived(attn.Notification{From: "mail", Text: "your results are in"})
	if err != nil {
		t.Fatalf("a full queue refused the arrival itself: %v", err)
	}
	if id == 0 {
		t.Error("the arrival got no id, so its sender cannot close it and the center cannot dismiss it")
	}
	if n := len(s.jrn.Waiting()); n != journal.QueueMax {
		t.Errorf("%d waiting, want the cap to hold at %d", n, journal.QueueMax)
	}
	var found bool
	for _, r := range s.history.Recent() {
		if r.ID == id && r.Text == "your results are in" {
			found = true
			if r.Queued {
				t.Error("the record says it is on the queue, and it is not")
			}
		}
	}
	if !found {
		t.Error("a full queue lost the notification: it is in neither the queue nor the history")
	}
}
