// Package clip is the clipboard history: what was copied, bounded, in memory
// only, and gone again on its own.
//
// It is here rather than being wl-clip-persist because of two rules that a
// general clipboard manager does not have, and they are the whole reason zde
// owns this (docs/vision.md, principle 5 - the clipboard has history, secrets
// never enter it):
//
//   - The hint. An entry a password manager marks as a secret is never recorded,
//     and nothing zde wrote ever asks for its bytes: the offer is asked what it
//     is made of first, and one carrying the hint is refused before any read
//     (see Sensitive, and internal/zded/clip.go for the order the watcher asks
//     in). So it is not in this daemon's heap, not in the ring, and not in a
//     core file of zded.
//
//     What that deliberately does not say, because it is not true: the secret
//     has already been through a process zde started. `wl-paste --watch` pipes
//     the selection to itself before it spawns anything, so the password manager
//     has written it into a pipe held by a child of zded by the time the types
//     are read (internal/clip/wl.go, Watch, which has the citation). Nothing
//     short of speaking the Wayland protocol changes that. The promise here is
//     the narrow one, and it is the one that outlives the copy: the daemon that
//     runs for the whole session never holds it.
//
//   - The TTL. An entry older than TTL is dropped and its bytes are overwritten
//     where they lie (see Expire). A history that only stops listing an entry is
//     a history a memory dump still reads, and this is a desktop whose users
//     expect the second thing to be true as well as the first.
//
// In memory only, and this is the line to argue with before anybody adds a
// file. The notification history is the precedent to argue against rather than
// from: it does write a bounded snapshot to disk (internal/attn, snapshotMax -
// forty records, so a restart still answers "what was happening"), and the
// reason that was affordable there is exactly the reason it is not affordable
// here. A notification history on disk is a log of what other people sent you,
// and a clipboard history on disk is a wallet: every password that was ever
// pasted rather than typed, every token copied out of a terminal, every
// address, in one file, in the clear, surviving a reboot - and the TTL above
// becomes a lie the moment the text is also somewhere with an inode. If a
// future zde wants persistence, it wants an argument against that paragraph
// first, and an encrypted store is not one: the key would have to be readable
// by the daemon that reads the clipboard, so it is the same file with a longer
// path.
//
// The bounds are all here because clipboard content is whatever an application
// felt like offering: Max entries, TextMax bytes in each, and anything bigger or
// anything that is not text is remembered as a note that says so rather than as
// content (see Note). That is what keeps a bounded design bounded when somebody
// copies a video.
package clip

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Max is how many entries are kept.
//
// Fifty rather than the notification history's two hundred, because these are
// two different questions. "What did I miss" is asked about a day; "where is
// that thing I copied" is asked about the last few minutes, and a list you
// scroll past fifty rows of is one you would have retyped by now. Fifty entries
// at TextMax each is 3 MB if every one of them is at its limit, and a few
// hundred kilobytes in a real session.
const Max = 50

// TextMax is the most one entry may hold.
//
// 64 KiB is a screenful of code or a long document's worth of prose, and it is
// well under what would make this ring the biggest thing in the daemon. What is
// bigger is not truncated: a clipboard entry is put back on the clipboard, and
// half a file pasted into an editor is data loss that looks like a paste that
// worked. So an offer past this bound is kept as a note that says how big it
// was (see Note), which is the difference between a history that dropped it and
// one that appears to be broken.
const TextMax = 64 << 10

// TTL is how long an entry lives.
//
// Fifteen minutes is the span the question is actually asked over: the thing
// you copied to paste in a moment, and the thing you copied before that. An
// hour would cover more and would also mean a laptop left alone at a table
// holds an hour of everything that passed through the clipboard, which is the
// trade this project settles the other way (docs/vision.md, priority order).
//
// docs/vision.md, principle 5 names a 30 second wipe for an entry that carries
// the sensitive hint. That entry never gets here at all, so what is left for
// the TTL is everything else, which is why it is minutes rather than seconds.
const TTL = 15 * time.Minute

// PreviewMax is how much of an entry's text leaves the daemon before somebody
// picks it.
//
// The surface is handed previews and not the entries themselves, so the whole
// of what was copied stays in zded until a row is chosen - one process holding
// it instead of two, and nothing in the shell to expire. The cost is that the
// surface's filter matches what is on the screen and not what is past the end
// of it, which is the honest bargain and is why this is 200 rather than 40:
// almost everything anybody copies is shorter than this line.
const PreviewMax = 200

// TypeMax bounds a mime type name kept for a note. The name comes from the
// application that offered it, so it is attacker-controlled text on a row, the
// same way a notification's summary is (docs/vision.md, principle 3).
const TypeMax = 60

// HintType is how a password manager says "this is a secret".
//
// `x-kde-passwordManagerHint` with the value `secret` is the de facto
// convention - Klipper defined it, KeePassXC and the rest offer it - and it is
// the only one anything actually implements. Nothing here reads the value: the
// type being offered at all is enough to refuse the entry, because reading the
// value means a second request to the source for the sake of letting some
// entries through, and the two ways of being wrong here are not the same size
// (docs/vision.md, principle 9 - what cannot be enforced correctly is
// rejected). A false positive costs one clipboard entry that is not in the
// history; a false negative is a password in it.
const HintType = "x-kde-passwordManagerHint"

// Kind is what a row is: text that can be put back, or a note about something
// that was not kept.
type Kind string

const (
	// KindText is an entry with content. It is the only kind Enter can put back
	// on the clipboard.
	KindText Kind = "text"
	// KindBig is something too large to keep whole (see TextMax).
	KindBig Kind = "too-big"
	// KindOther is an offer that was not text: an image, a file, a private
	// format between two applications. zde keeps text only, so what is
	// remembered is that it happened and what it was offered as.
	KindOther Kind = "other"
)

// Row is one entry as a surface reads it. It carries a preview and never the
// whole text (see PreviewMax): the text goes over the socket once, when
// somebody picks the row.
type Row struct {
	ID uint64 `json:"id"`
	// Preview is the first PreviewMax runes on one line, with the line breaks
	// and control characters turned into spaces - a row is a row, and a
	// clipboard entry is whatever an application put there.
	Preview string `json:"preview"`
	// Cut says the preview stops short of the entry, so a surface can say so
	// rather than showing a sentence that appears to end.
	Cut bool `json:"cut,omitempty"`
	// Bytes is how big the entry is. It is what tells two similar-looking rows
	// apart, and for a note it is the whole of what is known about the thing.
	Bytes int  `json:"bytes"`
	Kind  Kind `json:"kind"`
	// Why says why a row cannot be put back, in the words the refusal uses when
	// somebody picks one anyway - the palette's bargain (internal/zded,
	// whyNot), and for the same reason: a row that silently does nothing is the
	// thing these surfaces exist to stop.
	Why string    `json:"why,omitempty"`
	At  time.Time `json:"at"`
}

// entry is one thing that was copied, as the daemon holds it.
type entry struct {
	id uint64
	// text is the entry's own buffer, owned by this package and overwritten
	// when it expires. A []byte rather than a string because a string cannot be
	// wiped: the bytes behind it are immutable and stay in the heap until the
	// collector gets round to them, and "gone" is the promise the TTL makes.
	text  []byte
	kind  Kind
	why   string
	bytes int
	at    time.Time
}

// ExpectWindow is how long the loop guard waits for the change zde caused.
//
// The echo of zde's own write is the next clipboard change, arriving as fast as
// the compositor can deliver it; ten seconds is far past that on a machine that
// is working. What the bound is really for is the echo that never comes - a
// write that took the selection and then lost it, a wl-paste that died between
// the two - because a guard with no deadline is one that swallows the next real
// copy of that text whenever it happens, which could be an hour later.
const ExpectWindow = 10 * time.Second

// History is the ring. Safe for concurrent use: the watcher adds from its own
// goroutine while a surface reads over the socket.
type History struct {
	mu      sync.Mutex
	entries []entry
	next    uint64
	// expect is the id of the entry zde last put back on the clipboard, held
	// until the change it causes comes back round (see Expect), and expectAt is
	// when that was.
	//
	// An id and not the text. The text is in the ring already, under the ring's
	// TTL and the ring's wipe, so matching against it costs nothing and leaves
	// nothing behind: this guard used to be a full copy of an entry, living
	// outside h.entries where no sweep reached it, which made the one entry
	// somebody had just chosen to paste - disproportionately the password - the
	// one entry with no expiry at all.
	expect   uint64
	expectAt time.Time
}

// Add records what was copied and answers with the id of the new entry, or zero
// when it kept nothing.
//
// It takes the slice rather than copying it: the caller read those bytes off
// the clipboard for this and nothing else, and a copy would be a second copy of
// every password-adjacent thing anybody pastes. What it does not keep, it
// wipes, which is the other half of owning it.
//
// Two arrivals are deliberately not entries. One is zde's own writing-back (see
// Expect): pressing Enter on an old entry must not push a new one, or the
// history reorders itself every time it is used. The other is a repeat of the
// newest entry, which is what an application asserting the same selection twice
// looks like from here - and, since most repeats are exactly that, is also the
// second line of defence for the first case.
func (h *History) Add(text []byte, now time.Time) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(text) == 0 || len(text) > TextMax {
		// Nothing to keep, or more than one entry may be. The caller checks the
		// bound too, because it is the one that knows the read stopped short -
		// this is the belt to that pair of braces.
		clear(text)
		return 0
	}
	if h.echo(text, now) {
		h.expect = 0
		clear(text)
		return 0
	}
	if n := len(h.entries); n > 0 && bytes.Equal(h.entries[n-1].text, text) {
		clear(text)
		return 0
	}
	h.next++
	return h.push(entry{
		id:    h.next,
		text:  text,
		kind:  KindText,
		bytes: len(text),
		at:    now,
	})
}

// Note records that something was copied and not kept: too big, or not text.
//
// A row rather than nothing, because the alternative is a person copying an
// image, pressing Mod+v, and finding a history that looks broken. It holds no
// content - that is the point of it - so what a note costs is one line saying
// what happened and when.
func (h *History) Note(kind Kind, why string, size int, now time.Time) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.entries); n > 0 && h.entries[n-1].kind == kind && h.entries[n-1].why == why {
		// The same rule Add has, and the case it is really for: an application
		// that re-asserts a selection zde will not keep would otherwise fill the
		// ring with the same row, and push out everything worth having.
		return 0
	}
	h.next++
	// No text, and that is the whole difference between a note and an entry:
	// there is nothing here to put back on the clipboard, nothing to wipe, and
	// nothing that was ever read.
	return h.push(entry{id: h.next, kind: kind, why: why, bytes: size, at: now})
}

// push adds an entry and drops the oldest when the ring is full. Called with
// the lock held.
func (h *History) push(e entry) uint64 {
	h.entries = append(h.entries, e)
	if len(h.entries) > Max {
		// Wiped as it goes, not merely unlinked: an entry pushed off the end is
		// as gone as an expired one, and for the same reason (see Expire).
		clear(h.entries[0].text)
		// Copied down rather than resliced from the front, which would leave the
		// backing array growing to the right for ever - the same leak the bound
		// exists to stop, only slower (internal/attn, History.Add).
		copy(h.entries, h.entries[1:])
		h.entries[len(h.entries)-1] = entry{}
		h.entries = h.entries[:Max]
	}
	return e.id
}

// Expect says zde is about to put the entry with this id on the clipboard
// itself, so the change it causes is not a new entry. Zero means nothing is
// expected, which is what the caller says when the write it announced failed.
//
// Said before the write and not after it: the clipboard change arrives on
// another goroutine and can beat the write's own return, and a loop guard that
// is set after the loop has already gone round guards nothing. Which is also
// why it has to be retractable - announcing before the fact means announcing
// writes that then do not happen (internal/zded, clipPut).
//
// An id rather than the text, which is the whole of what makes this safe: the
// bytes it will be compared against are the ring's own, so they expire when that
// entry expires and are wiped when it is wiped. An entry that goes before its
// echo arrives simply stops matching, and the echo becomes an ordinary new entry
// - one spare row in a case that needs the compositor to lose fifteen minutes.
//
// One deep, and consumed by the first arrival that matches. A second write
// before the first came back replaces it, which is the right way round: the
// newer one is the one whose echo has not happened yet.
func (h *History) Expect(id uint64, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expect = id
	h.expectAt = now
}

// echo says whether this arrival is the change zde caused. Called with the lock
// held.
//
// Three things have to hold: something is expected, it was expected recently
// enough (see ExpectWindow), and the entry it names is still in the ring holding
// exactly these bytes. The last is what the id buys - the comparison is against
// the copy the ring already governs, so there is no second one to look after.
func (h *History) echo(text []byte, now time.Time) bool {
	if h.expect == 0 || now.Sub(h.expectAt) >= ExpectWindow {
		return false
	}
	for _, e := range h.entries {
		if e.id == h.expect {
			return e.kind == KindText && bytes.Equal(e.text, text)
		}
	}
	return false
}

// Text is one entry's content, for putting it back on the clipboard. It answers
// why not, in words, when there is nothing to put back - an id that has expired
// out of the history is the ordinary case and the person has to be told which
// of the two it was.
//
// A copy, because the ring's own buffer is wiped where it lies the moment that
// entry expires, and a write that was reading it would put zeros on the
// clipboard.
func (h *History) Text(id uint64, now time.Time) ([]byte, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire(now)
	for _, e := range h.entries {
		if e.id != id {
			continue
		}
		if e.kind != KindText {
			if e.why == "" {
				// A note is always given a reason, so this is a note that was
				// added without one - and a refusal with nothing in it would be
				// the silent key wearing a different hat.
				return nil, "that row is not something the history kept, so there is nothing to put back"
			}
			return nil, e.why
		}
		return append([]byte(nil), e.text...), ""
	}
	return nil, "nothing in the clipboard history has that number any more: entries last " +
		TTL.String() + " and there are at most " + strconv.Itoa(Max)
}

// Rows is what a surface shows, newest first - the order the question is asked
// in: the thing you copied last is the thing you most likely want back.
func (h *History) Rows(now time.Time) []Row {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expire(now)
	out := make([]Row, 0, len(h.entries))
	for i := len(h.entries) - 1; i >= 0; i-- {
		e := h.entries[i]
		line, cut := preview(e.text)
		out = append(out, Row{
			ID:      e.id,
			Preview: line,
			Cut:     cut,
			Bytes:   e.bytes,
			Kind:    e.kind,
			Why:     e.why,
			At:      e.at,
		})
	}
	return out
}

// Expire drops what is older than TTL and answers how many went. Called on a
// clock as well as on every read (internal/zded, WatchClipboard): a session
// where nobody opens the history is exactly the session where an entry would
// otherwise sit in memory until the next login, which is the opposite of what a
// TTL is for.
func (h *History) Expire(now time.Time) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.expire(now)
}

// expire is Expire with the lock held.
//
// The bytes are overwritten before the entry is dropped. Dropping alone would
// leave the text in the heap until the collector reached it and something else
// reused the memory, which is minutes or hours or never - and a history that
// only stops listing an entry is one a core file still reads. What this cannot
// do is reach the copies it has already handed out: a preview that went to the
// shell, the JSON a socket wrote, the text a surface asked to put back. Those
// are bounded and short-lived by design, and they are the reason the whole
// entry stays here until somebody picks it.
func (h *History) expire(now time.Time) int {
	kept := h.entries[:0]
	gone := 0
	for _, e := range h.entries {
		if now.Sub(e.at) < TTL {
			kept = append(kept, e)
			continue
		}
		clear(e.text)
		gone++
	}
	// The tail is zeroed so the entries that moved down are not still reachable
	// through the backing array - the same reasoning as wiping the text.
	for i := len(kept); i < len(h.entries); i++ {
		h.entries[i] = entry{}
	}
	h.entries = kept
	// And the loop guard, which holds no text of its own and so has nothing to
	// wipe - what it has is a deadline, and this is the sweep that reaches it.
	// The bytes it would have been compared against are an entry's, so they went
	// with the loop above like everything else.
	if h.expect != 0 && now.Sub(h.expectAt) >= ExpectWindow {
		h.expect = 0
	}
	return gone
}

// Clear forgets everything now, and wipes it on the way out. It is the TTL by
// hand, for the moment somebody is about to hand over the screen.
func (h *History) Clear() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(h.entries)
	for i := range h.entries {
		clear(h.entries[i].text)
		h.entries[i] = entry{}
	}
	h.entries = h.entries[:0]
	h.expect = 0
	return n
}

// hintLower is HintType folded once, because Contains has no case-insensitive
// form and this is asked of every type of every copy.
var hintLower = strings.ToLower(HintType)

// Sensitive reports whether this offer says it is a secret.
//
// The whole of the first invariant, and it is deliberately one line: it is
// asked before anything reads the content, so an entry that answers true is
// never requested from the application at all (internal/zded, take).
//
// Containment rather than equality, and case folded. A mime type is not case
// sensitive, and the hint is not always the whole of the name it arrives under:
// `x-kde-passwordManagerHint;charset=utf-8` carries a parameter, and
// `application/x-kde-passwordManagerHint` puts it in the subtype, and an
// equality test catches neither. No reviewer has found a manager that spells it
// either way, so this is a gap rather than a live break - but the two ways of
// being wrong here are still not the same size (docs/vision.md, principle 9). A
// false positive costs one clipboard entry that is not in the history; a false
// negative is a password in it. And `x-kde-passwordmanagerhint` is
// twenty-five characters of a spelling nothing else uses, so a type containing
// it is a type that means it.
func Sensitive(types []string) bool {
	for _, t := range types {
		if strings.Contains(strings.ToLower(t), hintLower) {
			return true
		}
	}
	return false
}

// TextType picks which of the offered types to ask for, and says when none of
// them is text.
//
// The order is what an application means by the same content offered several
// ways: UTF-8 first because it is the only one whose encoding is not a guess.
// The X11 names are here because half of Wayland's clipboard traffic still
// comes through XWayland, and an application that offers only STRING is
// offering text.
func TextType(types []string) (string, bool) {
	for _, want := range []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING", "STRING", "TEXT"} {
		for _, t := range types {
			if strings.EqualFold(strings.TrimSpace(t), want) {
				return t, true
			}
		}
	}
	// Anything else textual - text/html from a browser, text/uri-list from a
	// file manager - rather than nothing. It is put back as text/plain, which
	// loses the markup's type and keeps the words, and words are what somebody
	// pressing Mod+v is after.
	for _, t := range types {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "text/") {
			return strings.TrimSpace(t), true
		}
	}
	return "", false
}

// Offered is the type to name on a note about something that was not kept: the
// first one offered, which is what the application put forward as the best
// description of it. Empty when it offered nothing at all.
//
// Cleaned here, at the one place a mime type crosses from an application into
// something zde shows a person: it ends up on a row and in a refusal, and
// nothing about where it came from is trustworthy (the same bargain the queue
// makes with a notification's text).
func Offered(types []string) string {
	for _, t := range types {
		if s := clean(t, TypeMax); s != "" {
			return s
		}
	}
	return ""
}

// preview is one line of an entry, bounded, and whether it stops short.
func preview(text []byte) (string, bool) {
	out := make([]rune, 0, PreviewMax)
	cut := false
	for _, r := range string(text) {
		if len(out) == PreviewMax {
			cut = true
			break
		}
		switch {
		case r == utf8.RuneError:
			// Not text after all, or text in an encoding nobody named. One
			// glyph rather than nothing, so a row is still a row.
			out = append(out, '.')
		case unicode.IsSpace(r):
			// A newline in a row makes one entry look like two, and a tab is a
			// column separator to everything that reads this from a terminal
			// (the queue makes the same bargain, internal/zded, checkQueueText).
			out = append(out, ' ')
		case !unicode.IsPrint(r):
			out = append(out, '.')
		default:
			out = append(out, r)
		}
	}
	return strings.TrimSpace(string(out)), cut
}

// clean is a string from somewhere else, made safe to put on a row: printable,
// one line, bounded.
func clean(s string, max int) string {
	out := make([]rune, 0, max)
	for _, r := range s {
		if len(out) == max {
			break
		}
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
