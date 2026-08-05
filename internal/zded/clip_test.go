package zded

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/clip"
)

// A clipboard with nothing behind it: what an application is offering, and a
// count of the times anybody asked for the content. The count is the point of
// the whole fake - the sensitive invariant is not "the entry is not in the
// list", it is "the bytes were never requested", and only the thing being asked
// can say whether they were.
type fakeClipboard struct {
	mu    sync.Mutex
	types []string
	data  []byte
	reads int
	wrote [][]byte
	// writeErr is what Write answers with when nothing can take the selection:
	// a session with no wayland display, or no wl-copy on the PATH. It is the
	// case that used to strand the loop guard, because the guard is announced
	// before the write and only the write's failure can take it back.
	writeErr string
	changes  chan struct{}
}

func newFakeClipboard() *fakeClipboard {
	return &fakeClipboard{changes: make(chan struct{}, 1)}
}

// offer is an application taking the selection, and the ring that follows it.
func (f *fakeClipboard) offer(data string, types ...string) {
	f.mu.Lock()
	f.types = types
	f.data = []byte(data)
	f.mu.Unlock()
	f.ring()
}

func (f *fakeClipboard) ring() {
	select {
	case f.changes <- struct{}{}:
	default:
	}
}

func (f *fakeClipboard) Watch(context.Context) (<-chan struct{}, error) { return f.changes, nil }

func (f *fakeClipboard) Types() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.types...), nil
}

func (f *fakeClipboard) Read(_ string, limit int) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if len(f.data) > limit {
		return append([]byte(nil), f.data[:limit+1]...), true, nil
	}
	return append([]byte(nil), f.data...), false, nil
}

// Write is a real clipboard's behaviour and not a recorder's: what is written
// becomes the selection, and the watcher hears about it. Without that this fake
// could not show the loop the loop guard exists to break.
func (f *fakeClipboard) Write(text []byte) error {
	f.mu.Lock()
	if f.writeErr != "" {
		err := errors.New(f.writeErr)
		f.mu.Unlock()
		return err
	}
	f.wrote = append(f.wrote, append([]byte(nil), text...))
	f.types = []string{"text/plain"}
	f.data = append([]byte(nil), text...)
	f.mu.Unlock()
	f.ring()
	return nil
}

func (f *fakeClipboard) counts() (reads int, writes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads, len(f.wrote)
}

func clipServer(t *testing.T) (*Server, *fakeClipboard) {
	t.Helper()
	f := newFakeClipboard()
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}, nil)
	s.UseClipboard(f)
	return s, f
}

func clipRows(t *testing.T, s *Server) []clip.Row {
	t.Helper()
	resp := s.Dispatch(Request{Method: "clip.history"})
	if resp.Error != "" {
		t.Fatalf("clip.history: %s", resp.Error)
	}
	var got Clips
	if err := json.Unmarshal(resp.Ok, &got); err != nil {
		t.Fatal(err)
	}
	return got.Entries
}

// The first invariant, where it is actually enforced: an offer that carries the
// password manager hint is not read at all. Not read and dropped, not recorded
// and hidden - zded never makes the request, so the secret is never in this
// process's heap.
//
// Not "not in a pipe", which this comment used to say: wl-paste has already
// received the selection into one by the time these types reach here
// (internal/clip, Tool.Watch). What this test pins is the reachable half, which
// is also the whole of what a test can pin - that nothing zde wrote asks for the
// bytes.
//
// The read count is what makes that a real assertion. A history that recorded
// the password and then declined to list it would pass a test that only looked
// at the rows, and it is exactly the design docs/vision.md, principle 5 is
// written against: what is in memory is what a memory dump has.
//
// If this regresses, zde is a program that collects passwords.
func TestAnOfferThatSaysItIsASecretIsNeverEvenRead(t *testing.T) {
	s, f := clipServer(t)

	// What a password manager puts on the clipboard: the secret as ordinary
	// text, with the hint beside it.
	f.offer("hunter2", "text/plain;charset=utf-8", "text/plain", clip.HintType)
	s.take()

	if reads, _ := f.counts(); reads != 0 {
		t.Errorf("the clipboard was read %d times for an offer that said it was a secret", reads)
	}
	if rows := clipRows(t, s); len(rows) != 0 {
		t.Errorf("a secret is in the history: %+v", rows)
	}

	// And the ordinary case still works, or a test that passes because nothing
	// is ever recorded would look exactly like this one.
	f.offer("an address", "text/plain")
	s.take()
	rows := clipRows(t, s)
	if len(rows) != 1 || rows[0].Preview != "an address" {
		t.Fatalf("ordinary text was not recorded: %+v", rows)
	}
}

// Putting an entry back on the clipboard is a clipboard change like any other,
// and zde must not record its own. Without this, using the history rewrites it:
// every Enter pushes what was already in the list back on top of it, and the
// row you reached for is somewhere else the next time you look.
func TestPuttingAnEntryBackDoesNotRecordItAgain(t *testing.T) {
	s, f := clipServer(t)
	f.offer("the first thing", "text/plain")
	s.take()
	f.offer("the second thing", "text/plain")
	s.take()

	rows := clipRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("%d rows before anything was put back: %+v", len(rows), rows)
	}
	oldest := rows[1]

	resp := s.Dispatch(Request{Method: "clip.history", Args: []string{itoa(oldest.ID)}})
	if resp.Error != "" {
		t.Fatalf("putting an entry back: %s", resp.Error)
	}
	if _, writes := f.counts(); writes != 1 {
		t.Fatalf("the clipboard was written %d times", writes)
	}
	// The change zde caused, arriving the way any other would.
	s.take()

	after := clipRows(t, s)
	if len(after) != 2 {
		t.Fatalf("%d rows after putting one back, want the two that were there: %+v", len(after), after)
	}
	if after[0].Preview != "the second thing" {
		t.Errorf("the newest row is %q, so the list reordered itself under the person using it", after[0].Preview)
	}
}

// A write that failed is not a change that is coming. The history is told what
// is about to be written before it is written - it has to be, because the echo
// can beat the write's own return - so the write failing is the only moment
// anything can take that back (internal/clip, Expect).
//
// Without the retraction, one `wl-copy: no wayland display` leaves the history
// waiting for an echo nothing will send, and the next genuine copy of the text
// somebody just tried to paste is swallowed as though zde had caused it. That is
// the entry a person reached for on purpose, which is the worst one to lose.
func TestAWriteThatFailedDoesNotSwallowTheNextCopy(t *testing.T) {
	s, f := clipServer(t)
	f.offer("a token from a terminal", "text/plain")
	s.take()
	rows := clipRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("%d rows before anything was put back: %+v", len(rows), rows)
	}

	f.mu.Lock()
	f.writeErr = "wl-copy: no wayland display"
	f.mu.Unlock()
	resp := s.Dispatch(Request{Method: "clip.history", Args: []string{itoa(rows[0].ID)}})
	if resp.Error == "" {
		t.Fatal("a clipboard that cannot be written answered as though it had been")
	}

	// The session comes back, and the person copies that same text again by
	// hand. Nothing zde did took the selection, so this is an ordinary copy.
	f.mu.Lock()
	f.writeErr = ""
	f.mu.Unlock()
	f.offer("something else", "text/plain")
	s.take()
	f.offer("a token from a terminal", "text/plain")
	s.take()

	after := clipRows(t, s)
	if len(after) != 3 {
		t.Fatalf("%d rows, want three - the copy after a failed write was taken for zde's own echo: %+v",
			len(after), after)
	}
	if after[0].Preview != "a token from a terminal" {
		t.Errorf("the newest row is %q", after[0].Preview)
	}
}

// An image or a file is the case that turns a bounded history into an unbounded
// one, and it is answered before anything is read: a non-text offer is a row
// saying what it was, holding nothing. A row rather than silence, because
// copying an image and finding an empty history reads as a broken key.
func TestSomethingThatIsNotTextIsARowAndNeverContent(t *testing.T) {
	s, f := clipServer(t)

	f.offer("...the whole png...", "image/png", "image/bmp")
	s.take()

	if reads, _ := f.counts(); reads != 0 {
		t.Errorf("an image was read %d times, and none of it is worth holding", reads)
	}
	rows := clipRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("%d rows for a copied image: %+v", len(rows), rows)
	}
	if rows[0].Kind != clip.KindOther {
		t.Errorf("an image reads as %q", rows[0].Kind)
	}
	if !strings.Contains(rows[0].Why, "image/png") {
		t.Errorf("the row does not say what was copied: %+v", rows[0])
	}
	// And it cannot be put back, with the reason rather than a silent nothing.
	resp := s.Dispatch(Request{Method: "clip.history", Args: []string{itoa(rows[0].ID)}})
	if resp.Error == "" {
		t.Error("an image was put back on the clipboard, and there is nothing behind that row to put")
	}
	if _, writes := f.counts(); writes != 0 {
		t.Errorf("the clipboard was written %d times for a row holding nothing", writes)
	}
}

// A huge paste is read up to the bound and no further, so what somebody copied
// is never the size of what this daemon holds - and it is not kept in part,
// because half a file put back on the clipboard is data loss that looks like a
// paste that worked.
func TestAHugePasteIsBoundedAndNotKeptInPart(t *testing.T) {
	s, f := clipServer(t)

	f.offer(strings.Repeat("x", clip.TextMax*2), "text/plain")
	s.take()

	rows := clipRows(t, s)
	if len(rows) != 1 || rows[0].Kind != clip.KindBig {
		t.Fatalf("a paste past the bound came out as %+v", rows)
	}
	if rows[0].Preview != "" {
		t.Errorf("the row is holding %d characters of what was too big to hold", len(rows[0].Preview))
	}
	if !strings.Contains(rows[0].Why, "whole") {
		t.Errorf("the row does not say why it is not the entry: %q", rows[0].Why)
	}
}

// The surface gets its rows with the event rather than after it, the same
// bargain every other surface zded asks for makes - and the acknowledgement is
// what tells the key whether anything drew it.
func TestTheClipboardHistoryTellsAListener(t *testing.T) {
	s, f := clipServer(t)
	f.offer("something", "text/plain")
	s.take()
	rec := &recorder{}
	s.listen(&sink{w: rec})

	rows := clipRows(t, s)
	var got struct{ Event Event }
	line := strings.TrimSpace(rec.String())
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the event is not one line of json: %q", line)
	}
	if got.Event.Kind != EventClip {
		t.Errorf("kind = %q, want the clipboard history's own kind", got.Event.Kind)
	}
	if len(got.Event.Clips) != len(rows) {
		t.Errorf("the event carries %d rows and the answer has %d", len(got.Event.Clips), len(rows))
	}
	if got.Event.Token == "" {
		t.Error("the event carries no token, so nothing can say it drew the surface")
	}
}

// The whole of what is on the wire is a preview. The entries stay in the daemon
// until somebody picks one, so a shell holding the history - with no TTL of its
// own and no reason to have one - is a thing that cannot happen by accident.
func TestOnlyAPreviewLeavesTheDaemon(t *testing.T) {
	s, f := clipServer(t)
	long := strings.Repeat("secret-ish ", clip.PreviewMax)
	f.offer(long, "text/plain")
	s.take()

	rows := clipRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	if n := len([]rune(rows[0].Preview)); n > clip.PreviewMax {
		t.Errorf("%d characters went over the socket, want at most %d", n, clip.PreviewMax)
	}
	if !rows[0].Cut {
		t.Error("the row does not say it is showing part of an entry")
	}
	// And what was copied is still whole where it is kept: the preview is what
	// is shown, not what is held.
	resp := s.Dispatch(Request{Method: "clip.history", Args: []string{itoa(rows[0].ID)}})
	if resp.Error != "" {
		t.Fatalf("putting it back: %s", resp.Error)
	}
	f.mu.Lock()
	put := string(f.wrote[0])
	f.mu.Unlock()
	if put != long {
		t.Errorf("what went back on the clipboard is %d bytes of the %d that were copied", len(put), len(long))
	}
}

// An id nobody has any more is refused in words, because the ordinary way to
// meet this is a list that was drawn before an entry expired out of it - and a
// key that quietly does nothing is what every surface here exists to stop.
func TestAnEntryThatIsGoneRefusesInWords(t *testing.T) {
	s, _ := clipServer(t)
	resp := s.Dispatch(Request{Method: "clip.history", Args: []string{"404"}})
	if resp.Error == "" {
		t.Fatal("putting back an entry that does not exist worked")
	}
	if !strings.Contains(resp.Error, "expire") && !strings.Contains(resp.Error, "last") {
		t.Errorf("the refusal is %q, and it has to say why the entry is not there", resp.Error)
	}
	// And a clear says how much went, so "it did something" and "there was
	// nothing there" are different answers.
	if resp := s.Dispatch(Request{Method: "clip.clear"}); resp.Error != "" {
		t.Errorf("clip.clear: %s", resp.Error)
	}
}

// An empty history has two causes that look identical from a keyboard: nobody
// has copied anything, and nothing is watching the clipboard at all. Only one
// of them is worth doing something about, so the answer says which it is -
// otherwise a machine with no wl-clipboard is a key that appears to work and
// never shows anything, which is the silent key in a new disguise.
func TestAnEmptyHistorySaysWhetherAnythingIsWatching(t *testing.T) {
	s, f := clipServer(t)
	f.offer("something", "text/plain")
	s.take()
	resp := s.Dispatch(Request{Method: "clip.history"})
	var watching Clips
	if err := json.Unmarshal(resp.Ok, &watching); err != nil {
		t.Fatal(err)
	}
	if watching.Why != "" {
		t.Errorf("a working clipboard says %q is wrong with it", watching.Why)
	}

	// A daemon with nothing to watch with. WatchClipboard is what finds that
	// out, and it is also what says so.
	none := New("test", nil, nil, nil)
	none.WatchClipboard(t.Context())
	resp = none.Dispatch(Request{Method: "clip.history"})
	var quiet Clips
	if err := json.Unmarshal(resp.Ok, &quiet); err != nil {
		t.Fatal(err)
	}
	if quiet.Why == "" {
		t.Error("nothing is watching the clipboard and the answer does not say so")
	}
}

// The watcher is wired: something copied arrives in the history without
// anybody asking for it. Everything else here drives take() by hand, which
// would pass just as well on a daemon that never started a watch at all.
func TestWhatIsCopiedArrivesWithoutAnybodyAsking(t *testing.T) {
	s, f := clipServer(t)
	ctx, stop := context.WithCancel(context.Background())
	// Stopped and waited for, not just stopped. A watcher still winding down
	// while the next test runs is a goroutine allocating inside somebody else's
	// measurement, and the test that measures allocations per poll is two files
	// away (TestTheBarsPollDoesNotCopyTheWholeJournal, which this made flake).
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		s.WatchClipboard(ctx)
	}()
	defer func() {
		stop()
		<-watching
	}()

	f.offer("copied while nobody was looking", "text/plain")

	deadline := time.Now().Add(3 * time.Second)
	for {
		if rows := clipRows(t, s); len(rows) == 1 {
			if rows[0].Preview != "copied while nobody was looking" {
				t.Fatalf("the watcher recorded %+v", rows[0])
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatal("nothing reached the history, so the watcher is not connected to the clipboard")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// itoa is one entry id as the wire spells it.
func itoa(id uint64) string { return strconv.FormatUint(id, 10) }
