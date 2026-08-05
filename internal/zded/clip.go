package zded

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/crispuscrew/zde/internal/clip"
)

// The clipboard half of the daemon: the watcher that keeps the history, and the
// verbs behind Mod+v (docs/model.md, section 6, clip).
//
// What a history is, what it keeps and what it refuses is internal/clip's - the
// bounds, the TTL, and the rule that a secret is never recorded. What is here
// is the session's use of it: the order the questions are asked in, which is
// where the sensitive invariant is actually enforced, and the loop that would
// otherwise record zde's own writing-back as something a person copied.

// Clipboard is what the daemon needs from the session's clipboard. An interface
// for the reason Compositor is one: zded has to run, and answer, on a machine
// where this is not there at all - a session with no wl-clipboard installed, or
// a compositor with no data-control protocol - and a test has to be able to
// drive a clipboard without one.
type Clipboard interface {
	// Watch rings the channel when the selection changes, and closes it when it
	// stops watching. More than one ring per change is allowed: the history
	// refuses a repeat of its own newest entry, so the cost of an extra ring is
	// two short-lived processes and nothing else (internal/clip, Tool.Watch).
	Watch(ctx context.Context) (<-chan struct{}, error)
	// Types is what the current selection is offered as, without reading any of
	// it. It is the whole of the sensitive invariant: a selection that says it
	// is a secret is answered here and never read.
	Types() ([]string, error)
	// Read is the selection in one of those types, up to limit bytes, and
	// whether there was more of it than that.
	Read(mime string, limit int) (data []byte, more bool, err error)
	// Write puts text on the clipboard, as text/plain.
	Write(text []byte) error
}

// UseClipboard says how to reach the session's clipboard. Nothing does until
// this is called, which is what keeps a Server built by a test from spawning
// wl-paste at the machine it is running on (cmd/zded is where the real one is
// wired, beside the compositor).
func (s *Server) UseClipboard(c Clipboard) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clipboard = c
}

// clipTool is who to ask, or nil when nothing can reach a clipboard. Read under
// the lock and used outside it, the way the notifier is: every call on it is a
// process, and holding s.mu across one would put every keybind behind whatever
// the clipboard is doing.
func (s *Server) clipTool() Clipboard {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clipboard
}

// watchingClipboard records whether anything is watching, and why not. It is
// what turns an empty history from a mystery into a sentence (see Clips.Why).
func (s *Server) watchingClipboard(why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clipWhy = why
}

func (s *Server) clipboardTrouble() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clipWhy
}

// sweepEvery is how often the history is swept for entries past their TTL.
//
// On a clock and not only when somebody looks, because a session where nobody
// opens the history is exactly the session where an entry would otherwise sit
// in memory until the next login - which is the opposite of what a TTL is for.
// Thirty seconds means an entry outlives its TTL by at most that, which is
// close enough for a bound that is measured in minutes and cheap enough to do
// for ever.
const sweepEvery = 30 * time.Second

// Clips is what `clip.history` answers: whether a surface took the job of
// showing the history, and the rows themselves for the caller that has to print
// them because nothing did. The same bargain as the notification center's (see
// Center), and for the same reason - a key that does nothing when the shell is
// unwell is a key nobody trusts.
type Clips struct {
	Shown   bool       `json:"shown"`
	Entries []clip.Row `json:"entries"`
	// Why nothing is being watched, when nothing is. An empty history has two
	// causes that look identical from a keyboard - nobody has copied anything,
	// and wl-clipboard is not installed - and only one of them is worth doing
	// something about. Said here rather than only in the daemon's log, because
	// the person who needs it is the one looking at an empty list.
	Why string `json:"why,omitempty"`
}

// WatchClipboard keeps the clipboard history for as long as ctx lasts.
//
// The sweep starts before the first watch and outlives every one of them. If it
// hung off a watch instead, a session whose wl-paste died halfway through would
// keep every entry it had already recorded for the rest of the login - the one
// state where an expiry that does not run is worse than one that never started.
// With no clipboard at all there is nothing to sweep, because nothing can have
// been recorded.
//
// A watch that ends is dialled again, the way the compositor's is (see Watch):
// wl-paste goes with a compositor restart, and a clipboard history that quietly
// stopped watching is indistinguishable from one nobody has copied into. The
// reason is said once rather than every two seconds, because the ordinary way
// to land here for ever is a machine with no wl-clipboard installed, and a log
// line per retry would be the whole journal by morning.
func (s *Server) WatchClipboard(ctx context.Context) {
	tool := s.clipTool()
	if tool == nil {
		s.watchingClipboard("this zded was started without a clipboard to watch")
		return
	}
	go s.sweepClips(ctx)
	said := false
	for ctx.Err() == nil {
		changes, err := tool.Watch(ctx)
		if err != nil {
			s.watchingClipboard(err.Error())
			if !said {
				log.Printf("zded: clipboard history: %v", err)
				said = true
			}
			if !sleep(ctx, retry) {
				return
			}
			continue
		}
		said = false
		s.watchingClipboard("")
		s.drainClipboard(ctx, changes)
		if !sleep(ctx, retry) {
			return
		}
	}
}

// drainClipboard reads the clipboard after each burst of changes until the
// watch ends. One copy rings several times (one line per offered mime type), so
// letting the burst finish is the difference between two processes per copy and
// ten - the same settling the compositor's events get, for the same reason.
func (s *Server) drainClipboard(ctx context.Context, changes <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-changes:
			if !open {
				return
			}
		}
		for settling := true; settling; {
			select {
			case <-ctx.Done():
				return
			case _, open := <-changes:
				if !open {
					return
				}
			case <-time.After(settle):
				settling = false
			}
		}
		s.take()
	}
}

// sweepClips expires entries on a clock (see sweepEvery).
func (s *Server) sweepClips(ctx context.Context) {
	for {
		if !sleep(ctx, sweepEvery) {
			return
		}
		s.clips.Expire(time.Now())
	}
}

// take records what is on the clipboard now, and the order of it is the point.
//
// Ask what the offer is made of; refuse it if it says it is a secret; only then
// ask for the bytes. That order is what makes docs/vision.md, principle 5 true
// rather than aspirational: a hinted entry is not read, so it is not in this
// process, not in a pipe, and not in anything zde spawned. A watcher written
// the ordinary way - `wl-paste --watch <something that reads stdin>` - has the
// secret in a process it started before it can decide anything, and "never
// recorded" would be "read and then dropped", which a core file does not agree
// with.
//
// Nothing here is logged. It runs on every copy, so a line per failure would be
// a log that fills at the speed of somebody working; what it does instead is
// keep no entry, which is what a person sees as a copy that is not in the list.
func (s *Server) take() {
	tool := s.clipTool()
	if tool == nil {
		return
	}
	types, err := tool.Types()
	if err != nil || len(types) == 0 {
		// An empty clipboard answers this way, and so does a selection that has
		// already been replaced by the time the question got asked. Neither is
		// something to record.
		return
	}
	if clip.Sensitive(types) {
		return
	}
	now := time.Now()
	mime, ok := clip.TextType(types)
	if !ok {
		// An image, a file, a private format between two applications: not read
		// at all, because reading it is what turns a bounded history into a
		// place screenshots accumulate. The row says what it was, so that Mod+v
		// after copying an image is a history that explains itself rather than
		// one that looks broken.
		s.clips.Note(clip.KindOther, clip.Offered(types)+" is not text, and the history keeps text only", 0, now)
		return
	}
	data, more, err := tool.Read(mime, clip.TextMax)
	if err != nil {
		return
	}
	if more {
		// Read up to the bound and no further, so the size of what somebody
		// copied is never the size of what this daemon holds. What was read is
		// wiped rather than kept as a shorter entry: half a file put back on the
		// clipboard is data loss that looks like a paste that worked.
		clear(data)
		s.clips.Note(clip.KindBig,
			fmt.Sprintf("more than %d KiB of %s, and an entry is kept whole or not at all",
				clip.TextMax>>10, clip.Offered([]string{mime})),
			clip.TextMax, now)
		return
	}
	s.clips.Add(data, now)
}

// clipHistory opens the clipboard history, or says that nothing could open it.
// The same shape as every other surface zded asks for (see switcher).
func (s *Server) clipHistory() Response {
	rows := s.clips.Rows(time.Now())
	// Which screen, and it does not mind not being told. Like the palette, this
	// surface is made of something the daemon already holds, so it is one of the
	// two that still work on a session whose compositor has stopped answering -
	// and a compositor that answers nothing costs the screen this appears on and
	// nothing else. Nil is a zded built without one at all, which is a test and
	// not a session.
	output := ""
	if s.niri != nil {
		if _, on, err := s.niri.FocusedPlace(); err == nil {
			output = on
		}
	}
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	why := s.clipboardTrouble()
	sent := s.broadcast(Event{
		Kind:   EventClip,
		Clips:  rows,
		Output: output,
		Token:  token,
	})
	if sent == 0 {
		return ok(Clips{Shown: false, Entries: rows, Why: why})
	}
	select {
	case <-acked:
		return ok(Clips{Shown: true, Entries: rows, Why: why})
	case <-time.After(ackWait):
		return ok(Clips{Shown: false, Entries: rows, Why: why})
	}
}

// clipPut puts one entry back on the clipboard, by the id from the list.
//
// The history is told what is coming before the clipboard is written, and not
// after. The change arrives on the watcher's own goroutine and can beat this
// call's return, so a guard set afterwards guards nothing - and what it would
// have cost is the list reordering itself every time somebody used it
// (internal/clip, Expect).
func (s *Server) clipPut(id string) Response {
	tool := s.clipTool()
	if tool == nil {
		return Response{Error: "nothing on this session can write to the clipboard: " +
			clip.Copy + " is what zde puts an entry back with"}
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return Response{Error: "clip.history wants the id from the list, not " + strconv.Quote(id)}
	}
	text, why := s.clips.Text(n, time.Now())
	if why != "" {
		return Response{Error: why}
	}
	// The copy this call was handed goes when the call does. The entry itself is
	// still in the ring and still expires on its own; what this stops is a spare
	// copy of it lying in the heap afterwards, outliving the thing it came from.
	defer clear(text)
	s.clips.Expect(text)
	if err := tool.Write(text); err != nil {
		return Response{Error: err.Error()}
	}
	return ok([]string{})
}

// clipClear forgets the history now, wiping it rather than unlisting it
// (internal/clip, Clear). It is the TTL by hand, for the moment before somebody
// else looks at this screen - and it says how many entries went, because "it
// did something" and "there was nothing there" are different answers.
func (s *Server) clipClear() Response {
	return ok(s.clips.Clear())
}
