package clip

import (
	"bytes"
	"strconv"
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
	// And the two shapes an equality test misses. Nobody has found a manager
	// that spells it either way, so this is the gap being closed rather than a
	// break being fixed - but the cost of being wrong is one entry in one
	// direction and a password in the other.
	if !Sensitive([]string{"x-kde-passwordManagerHint;charset=utf-8"}) {
		t.Error("the hint with a parameter on it reads as ordinary text")
	}
	if !Sensitive([]string{"application/x-kde-passwordManagerHint"}) {
		t.Error("the hint as a subtype reads as ordinary text")
	}
	if !Sensitive([]string{"  " + HintType + "\t"}) {
		t.Error("the hint with whitespace round it reads as ordinary text")
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
	first := h.Add(text("first"), now)
	h.Add(text("second"), now)
	h.Add(text("third"), now)

	// Enter on the oldest row: zde says which entry it is about to write, then
	// the change comes back round from the compositor.
	h.Expect(first, now)
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

// The loop guard is not a place an entry survives its own expiry. It used to be
// a full copy of the text, held outside the ring where no sweep and no wipe
// reached it, so the one entry somebody had just chosen to paste - which is
// disproportionately the password - was the one entry with no TTL at all.
//
// It is an id now, so the bytes it is compared against are the ring's own and
// expire with them. What that has to look like from outside is this: once the
// entry is gone, nothing is being held on its behalf, and text that matches it
// is an ordinary new copy.
//
// If this regresses, the TTL has a hole in it shaped like the last thing pasted.
func TestTheLoopGuardDoesNotOutliveTheEntryItNames(t *testing.T) {
	var h History
	secret := text("s3cret-token-from-a-terminal")
	id := h.Add(secret, now)
	h.Expect(id, now)
	if h.expect != id {
		t.Fatalf("the guard names %d and not the entry that was put back, so this test proves nothing", h.expect)
	}

	// The echo never arrives - the compositor lost it, the window closed - and
	// the entry reaches its TTL still expected.
	if n := h.Expire(now.Add(TTL + time.Second)); n != 1 {
		t.Fatalf("%d entries expired, want the one that was expected", n)
	}
	// The sweep reaches the guard too. It holds no text, so this is not a wipe -
	// it is the guard's lifetime being decided by the same clock as everything
	// else's, rather than by whether an echo ever turned up.
	if h.expect != 0 {
		t.Errorf("the guard still names entry %d after a sweep that dropped it", h.expect)
	}
	for i, b := range secret {
		if b != 0 {
			t.Fatalf("the text is still in memory after expiry: byte %d is %q of %q", i, b, secret)
		}
	}
	// And nothing is being kept for it: the same text copied afresh is an entry
	// like any other. Held anywhere, this arrival would be swallowed as an echo.
	if got := h.Add(text("s3cret-token-from-a-terminal"), now.Add(TTL+2*time.Second)); got == 0 {
		t.Error("a copy made after the entry expired was swallowed, so something outlived the wipe")
	}
}

// The echo that never comes. Copying something else before zde's own write
// arrives back leaves the guard unspent, and with no deadline on it the next
// genuine copy of that text is swallowed - an hour later, silently, and only for
// the entry that was last put on the clipboard.
//
// If this regresses, the history quietly refuses to record one particular thing
// for the rest of the session.
func TestAnEchoThatNeverArrivesStopsBeingExpected(t *testing.T) {
	var h History
	id := h.Add(text("the address"), now)
	h.Expect(id, now)

	// Something else is copied first, so the guard is not consumed.
	if got := h.Add(text("something else entirely"), now.Add(time.Second)); got == 0 {
		t.Fatal("an ordinary copy was swallowed")
	}
	// Past the window, the address is copied by hand and is an entry like any
	// other.
	if got := h.Add(text("the address"), now.Add(ExpectWindow+time.Second)); got == 0 {
		t.Error("a copy made past ExpectWindow was still being taken for zde's own echo")
	}
}

// A write that was announced and then failed has to be retractable, because the
// announcement has to come first: the echo can beat the write's own return, so a
// guard set afterwards guards nothing (see Expect). Zero is how the caller takes
// it back (internal/zded, clipPut).
//
// If this regresses, `wl-copy: no wayland display` costs the person the next
// copy of whatever they tried to paste.
func TestARetractedExpectationSwallowsNothing(t *testing.T) {
	var h History
	id := h.Add(text("the thing to paste"), now)
	h.Expect(id, now)
	h.Expect(0, now)

	// Something else in between, or the arrival below is refused as a repeat of
	// the newest entry and this test passes without ever reaching the guard.
	if got := h.Add(text("something else entirely"), now.Add(time.Second)); got == 0 {
		t.Fatal("an ordinary copy was swallowed")
	}
	if got := h.Add(text("the thing to paste"), now.Add(2*time.Second)); got == 0 {
		t.Error("the echo of a write that never happened was still being waited for")
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

// Nothing copied is still readable an hour later, whatever the TTL says.
//
// The consequence a TTL holds back is not memory, so measuring memory would be
// measuring the wrong thing: what it holds back is how long a password lives in
// a process that runs for the whole session. The thing to assert is therefore a
// wall-clock one - by this hour, the bytes are zeroes - and the hour is not
// invented for this test. TTL's own comment names it: "An hour would cover more
// and would also mean a laptop left alone at a table holds an hour of
// everything that passed through the clipboard, which is the trade this project
// settles the other way." So an hour is the number the code has already
// rejected, and a clipboard history that still holds a password at an hour is
// one that made the opposite trade without saying so.
//
// Deliberately not compared against TTL. TestAnExpiredEntryIsWipedAndNotOnlyUnlisted
// above asks about TTL minus a minute and TTL plus a second, which pins that
// expiry happens and wipes rather than delists - and passes just as well at
// fifteen minutes, at two hours and at a week. This one is the other half.
//
// Free of wall-clock cost, because Expire takes the time as an argument: the
// clock this asks about is the session's, not the test runner's.
func TestNothingCopiedIsStillReadableAnHourLater(t *testing.T) {
	var h History
	// A full ring, so this is about everything the history can be holding and
	// not about one entry that happened to be old.
	held := make([][]byte, 0, Max)
	for i := 0; i < Max; i++ {
		// Distinct, or Add drops a repeat of the newest and there is nothing
		// here to expire.
		secret := text("s3cret-token-" + strconv.Itoa(i))
		held = append(held, secret)
		if id := h.Add(secret, now); id == 0 {
			t.Fatalf("entry %d was not recorded, so this test is not about expiry", i)
		}
	}
	if len(h.Rows(now)) != Max {
		t.Fatalf("%d rows were recorded, want a full ring of %d", len(h.Rows(now)), Max)
	}

	const hour = time.Hour
	h.Expire(now.Add(hour))

	if rows := h.Rows(now.Add(hour)); len(rows) != 0 {
		t.Errorf("%d entries are still listed %s after they were copied, and the TTL is the "+
			"promise that a password copied at a table is not still there when somebody else sits down",
			len(rows), hour)
	}
	// The half that makes it a promise rather than a display rule: a history
	// that only stopped listing these is one a core file still reads.
	for i, secret := range held {
		for b := range secret {
			if secret[b] != 0 {
				t.Fatalf("entry %d is still in memory %s after it was copied: %q", i, hour, secret)
			}
		}
	}
}

// The whole clipboard history has a ceiling, and it is TextMax times Max.
//
// The consequence is the one TextMax's neighbours already do the arithmetic
// for: "Fifty entries at TextMax each is 3 MB if every one of them is at its
// limit" (see Max). That product is what makes an in-memory clipboard history
// affordable, and it is the argument the package header makes against ever
// putting this on disk - so it is worth a test that fails when the product
// stops being a size a daemon can carry, rather than three tests that each
// prove a trim happened.
//
// What the fixture copies is three large things to every ordinary one. Large
// is 384 KiB, six times the bound: at the bound as it stands Add refuses every
// one of those whole, so the ring fills with the ordinary 64 KiB copies beside
// them and holds the 3 MB the arithmetic says. What a person sees instead of a
// refused entry is a note, and that is the caller's half rather than this one's
// (internal/zded, take) - a note holds no content, so it is not part of the
// number being asserted here. Widen TextMax and the same copies are accepted,
// the ring fills three quarters with them, and the history is the largest thing
// in the daemon.
//
// Three to one rather than one to one because the ratio is what decides how
// much headroom the ceiling can have. Alternating, a widened ring is half large
// copies and half small, which lands close enough to the ceiling that the
// ceiling has to sit almost on top of the true figure to catch it - and a bound
// test that cannot tolerate a retune is the thing this whole exercise is
// against.
//
// Twelve megabytes is the ceiling: four times the three the package argues for,
// so deciding that a screenful of code is bigger than it used to be, or that
// fifty rows is not enough, is a decision somebody can make without this test
// having an opinion. It catches both terms of the product at eight times.
//
// Counted over the ring's own slices rather than through Rows, because Rows
// hands out previews of two hundred characters and the question here is what
// the daemon is still holding.
func TestTheWholeClipboardHistoryHasACeilingHoweverMuchIsCopied(t *testing.T) {
	var h History
	// Absolute rather than TextMax times six, for the reason the socket's own
	// bound test spells its sizes out: a fixture written in terms of the number
	// it is testing grows to meet whatever that number became.
	const big = 384 << 10
	const ordinary = 64 << 10
	// Copied exactly this long, prefix included. An entry a few bytes over a
	// bound is refused by it, so a fixture that appended its serial number to a
	// blob of the right size would be testing the wrong side of the comparison.
	// The serial also makes every copy distinct, because a repeat of the newest
	// entry is not a new one and a fixture of identical blobs would record one.
	copies := 0
	blob := func(n int) []byte {
		copies++
		b := bytes.Repeat([]byte("a"), n)
		copy(b, strconv.Itoa(copies)+":")
		return b
	}
	// Two hundred rounds of four copies, which is four times the ring as it
	// stands, so what is left at the end is what the ring chose to keep and not
	// simply everything that was copied. A number rather than Max times four,
	// so that widening the ring does not widen the fixture to match it.
	for i := 0; i < 200; i++ {
		h.Add(blob(big), now)
		h.Add(blob(big), now)
		h.Add(blob(big), now)
		h.Add(blob(ordinary), now)
	}

	held := 0
	for _, e := range h.entries {
		held += len(e.text)
	}
	if held > 12<<20 {
		t.Errorf("%d copies, three in four of them 384 KiB, left %d entries holding %d MB, "+
			"past the 12 MB a bounded clipboard history may cost: TextMax times Max is the whole "+
			"of what this daemon carries for the clipboard, and one of them has stopped being a bound",
			copies, len(h.entries), held>>20)
	}
	// And it is a history rather than an empty one, so the number above is a
	// ceiling something reached.
	if len(h.entries) == 0 {
		t.Error("nothing was kept at all, so the ceiling above is about an empty history")
	}
}
