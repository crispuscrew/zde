package clip

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// now is a fixed clock. The TTL is the point of half of these tests, and a test
// that read the wall clock would be asserting about how long it took to run.
var now = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func text(s string) []byte { return []byte(s) }

// The first invariant, at the level where it is decided. A password manager
// offers the hint alongside the ordinary text types, so a check that only
// looked at the first type, or that wanted the hint to be the only thing
// offered, would pass every password straight through.
//
// If this regresses, the one thing this feature promises never to hold is the
// thing it holds first.
func TestASourceThatOffersTheHintIsSensitiveWhateverElseItOffers(t *testing.T) {
	// What KeePassXC actually puts on the clipboard: the secret as text, with
	// the hint beside it.
	if !Sensitive([]string{"text/plain;charset=utf-8", "text/plain", HintType}) {
		t.Error("an offer carrying the password manager hint reads as ordinary text")
	}
	// Case, because a mime type is not case sensitive and a manager spelling it
	// its own way would otherwise be a password in the history.
	if !Sensitive([]string{"x-kde-passwordmanagerhint"}) {
		t.Error("the hint in another case reads as ordinary text")
	}
	if Sensitive([]string{"text/plain", "text/html"}) {
		t.Error("ordinary text reads as a secret, so nothing would ever be recorded")
	}
	if Sensitive(nil) {
		t.Error("an offer with no types at all reads as a secret")
	}
}

// The second invariant. An entry past its TTL is not merely filtered out of the
// list: the bytes it was holding are overwritten where they lie, because a
// history that only stops listing an entry is one a memory dump still reads.
//
// The test can assert that because Add takes the caller's slice rather than
// copying it, so the slice here is the one the ring holds.
//
// If this regresses, the TTL becomes a display rule - which is exactly the
// thing docs/vision.md, principle 5 asks this daemon not to be.
func TestAnExpiredEntryIsWipedAndNotOnlyUnlisted(t *testing.T) {
	var h History
	secret := text("s3cret-token-from-a-terminal")
	id := h.Add(secret, now)
	if id == 0 {
		t.Fatal("nothing was recorded, so this test is not about expiry")
	}

	if n := h.Expire(now.Add(TTL - time.Minute)); n != 0 {
		t.Fatalf("%d entries expired before the TTL was up", n)
	}
	if got := h.Rows(now.Add(TTL - time.Minute)); len(got) != 1 {
		t.Fatalf("%d rows a minute before the TTL, want the entry still there", len(got))
	}

	if n := h.Expire(now.Add(TTL + time.Second)); n != 1 {
		t.Fatalf("%d entries expired past the TTL, want 1", n)
	}
	if got := h.Rows(now.Add(TTL + time.Second)); len(got) != 0 {
		t.Errorf("an expired entry is still listed: %+v", got)
	}
	if _, why := h.Text(id, now.Add(TTL+time.Second)); why == "" {
		t.Error("an expired entry can still be put back on the clipboard")
	}
	for i, b := range secret {
		if b != 0 {
			t.Fatalf("the text is still in memory after expiry: byte %d is %q of %q",
				i, b, secret)
		}
	}
}

// Reading the list is not the only thing that expires an entry. A session where
// nobody opens the history is exactly the session where an entry would
// otherwise sit in memory until the next login, so the daemon sweeps on a clock
// (internal/zded, WatchClipboard) - and this is the sweep it calls.
func TestExpiryDoesNotWaitForSomebodyToLook(t *testing.T) {
	var h History
	h.Add(text("something"), now)
	if n := h.Expire(now.Add(2 * TTL)); n != 1 {
		t.Errorf("Expire dropped %d entries with nobody reading the list, want 1", n)
	}
}

// Putting an entry back on the clipboard must not record it as a new one.
// Without this, every use of the history reorders it: pick the third row, and
// the third row becomes the first, so the next Mod+v shows a list that has
// moved under the hand that just used it.
//
// The echo is a real clipboard change from wl-paste's point of view, so the
// only thing that can tell it apart is zde knowing it caused it.
func TestPuttingAnEntryBackIsNotANewEntry(t *testing.T) {
	var h History
	h.Add(text("first"), now)
	h.Add(text("second"), now)
	h.Add(text("third"), now)

	// Enter on the oldest row: zde says what it is about to write, then the
	// change comes back round from the compositor.
	back := text("first")
	h.Expect(back)
	if id := h.Add(text("first"), now.Add(time.Second)); id != 0 {
		t.Errorf("the echo of zde's own write was recorded as entry %d", id)
	}
	rows := h.Rows(now.Add(time.Second))
	if len(rows) != 3 {
		t.Fatalf("%d rows after putting one back, want the three that were there", len(rows))
	}
	if rows[0].Preview != "third" {
		t.Errorf("the newest row is %q, so the list reordered itself under the person using it", rows[0].Preview)
	}

	// And only once: the guard is spent on the echo it was set for, so the next
	// time somebody genuinely copies that text it is an entry like any other.
	if id := h.Add(text("first"), now.Add(2*time.Second)); id == 0 {
		t.Error("copying the same text again by hand was swallowed too")
	}
}

// An application asserting the same selection twice is not two entries. Some do
// it on every focus change, and a history that counted each one would be one
// row repeated to the bottom of the screen.
func TestTheSameThingCopiedTwiceIsOneEntry(t *testing.T) {
	var h History
	h.Add(text("once"), now)
	if id := h.Add(text("once"), now.Add(time.Second)); id != 0 {
		t.Errorf("a repeat of the newest entry was recorded as %d", id)
	}
	if got := h.Rows(now); len(got) != 1 {
		t.Errorf("%d rows for one thing copied twice", len(got))
	}
}

// The ring is bounded, and what falls off the end is wiped on the way out - the
// same promise the TTL makes, for the same reason. Unbounded, this is the one
// table in the daemon that grows by whatever anybody copies, for as long as the
// session lasts.
func TestTheOldestEntryGoesWhenTheRingIsFullAndIsWipedWithIt(t *testing.T) {
	var h History
	oldest := text("the first thing")
	h.Add(oldest, now)
	for i := range Max {
		h.Add([]byte(strings.Repeat("x", i+1)), now)
	}
	rows := h.Rows(now)
	if len(rows) != Max {
		t.Fatalf("%d rows, want the bound of %d", len(rows), Max)
	}
	for _, r := range rows {
		if r.Preview == "the first thing" {
			t.Fatal("the oldest entry is still listed past the bound")
		}
	}
	for _, b := range oldest {
		if b != 0 {
			t.Fatalf("an entry pushed off the end is still in memory: %q", oldest)
		}
	}
}

// Something too big is not kept in part. A clipboard entry is put back on the
// clipboard, so half of one is not a shorter entry, it is data loss that looks
// like a paste that worked: the file that was copied and the file that was
// pasted differ, and nothing says so.
func TestSomethingTooBigIsNotKeptInPart(t *testing.T) {
	var h History
	huge := bytes.Repeat([]byte("a"), TextMax+1)
	if id := h.Add(huge, now); id != 0 {
		t.Errorf("an entry past the bound was recorded as %d", id)
	}
	if got := h.Rows(now); len(got) != 0 {
		t.Errorf("%d rows, and none of them can be put back whole: %+v", len(got), got)
	}
	for _, b := range huge {
		if b != 0 {
			t.Fatal("the bytes of something too big to keep are still in memory")
		}
	}

	// What the person gets instead: a row that says what happened, holding no
	// content, and refusing to be put back with the reason on it.
	id := h.Note(KindBig, "more than 64 KiB of text/plain, and an entry is kept whole or not at all", TextMax+1, now)
	rows := h.Rows(now)
	if len(rows) != 1 || rows[0].Kind != KindBig {
		t.Fatalf("the note is %+v", rows)
	}
	if rows[0].Preview != "" {
		t.Errorf("a note is holding %q, and it is supposed to hold nothing", rows[0].Preview)
	}
	if rows[0].Why == "" {
		t.Error("a row that cannot be put back says nothing about why")
	}
	if _, why := h.Text(id, now); why == "" {
		t.Error("a note can be put back on the clipboard, and it holds no content to put")
	}
}

// A preview is one line and a bounded one, because the whole of it is drawn on
// a row and a clipboard entry is whatever an application put there: a newline
// would make one entry look like two, and a tab is a column separator to
// everything that reads this from a terminal.
func TestARowIsOneBoundedLineOfWhateverWasCopied(t *testing.T) {
	var h History
	h.Add(text("two\nlines\tand\ra return"), now)
	h.Add([]byte(strings.Repeat("z", PreviewMax*2)), now)
	rows := h.Rows(now)

	long := rows[0]
	if len([]rune(long.Preview)) > PreviewMax {
		t.Errorf("a preview of %d runes, want at most %d", len([]rune(long.Preview)), PreviewMax)
	}
	if !long.Cut {
		t.Error("a preview that stops short does not say so, so the row reads as the whole entry")
	}
	if long.Bytes != PreviewMax*2 {
		t.Errorf("the row says %d bytes, want the size of the entry", long.Bytes)
	}

	short := rows[1]
	if strings.ContainsAny(short.Preview, "\n\t\r") {
		t.Errorf("a preview carries its line breaks: %q", short.Preview)
	}
	if short.Cut {
		t.Error("a short entry claims to be cut short")
	}
}

// Which type to ask for, and the answer that stops anything being read at all.
// An image on the clipboard is the case that turns a bounded design into an
// unbounded one, and the decision is made before a byte is requested.
func TestTheTextTypeIsPickedBeforeAnythingIsRead(t *testing.T) {
	got, ok := TextType([]string{"image/png", "text/html", "text/plain", "text/plain;charset=utf-8"})
	if !ok || got != "text/plain;charset=utf-8" {
		t.Errorf("picked %q, want the one type whose encoding is not a guess", got)
	}
	// XWayland's names are text too: half the clipboard traffic on a real
	// session comes through it, and an application offering only STRING is
	// offering text.
	if got, ok := TextType([]string{"STRING"}); !ok || got != "STRING" {
		t.Errorf("picked %q from the X11 names", got)
	}
	if got, ok := TextType([]string{"text/uri-list"}); !ok || got != "text/uri-list" {
		t.Errorf("picked %q, want any text/* rather than nothing", got)
	}
	if _, ok := TextType([]string{"image/png", "application/x-thing"}); ok {
		t.Error("an image reads as text, so zde would read a screenshot into the history")
	}
	if got := Offered([]string{"", "image/png"}); got != "image/png" {
		t.Errorf("a note about a non-text offer would be named %q", got)
	}
}

// Clear is the TTL by hand, for the moment before somebody else looks at the
// screen - so it has to wipe as well as forget, or "cleared" is a word about a
// list and not about the machine.
func TestClearWipesRatherThanForgets(t *testing.T) {
	var h History
	kept := text("a token somebody pasted")
	h.Add(kept, now)
	if n := h.Clear(); n != 1 {
		t.Errorf("Clear dropped %d entries, want 1", n)
	}
	if got := h.Rows(now); len(got) != 0 {
		t.Errorf("%d rows after clearing", len(got))
	}
	for _, b := range kept {
		if b != 0 {
			t.Fatalf("the text survived a clear: %q", kept)
		}
	}
}
