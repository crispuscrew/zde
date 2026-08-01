package zded

// ask is the quick LLM (docs/vision.md, section 2). What zde will not do is the
// part that decides the shape of this file: there is no API key and no HTTP
// client inside zded. "The provider tier's egress is its one API host" is zinc's
// enforcement, and zinc enforces what zinc runs - so the call belongs in a
// container, and what the daemon holds is the seam. One configured command per
// tier, the question on its stdin, its stdout coming back as it arrives.
//
// Layer 2's app definitions are still put on a machine by hand
// (docs/delivery.md), so a tier points at whatever that machine has:
// `zcr run ask-provider --exec` once there is such an app, a local model's CLI,
// a script. Nothing here knows which, and nothing here is a default - an unset
// tier is unset, because a default would be zde picking somebody's cloud for
// them and the local tier is the one people mean when they say private.
//
// Never an agent: a question goes in and text comes out. No tools, no files, and
// nothing written down (see askRun).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crispuscrew/zde/internal/apps"
)

// The tiers the keys and the surfaces reach: fast by default, private when it
// matters, and one for a question the first two got wrong. Not a closed set -
// a tier is whatever name the home-manager option was given - but these three
// are what has keys on them.
const (
	TierProvider = "provider"
	TierLocal    = "local"
	TierEscalate = "escalate"
)

// askFile is layer 1's answer to what this machine runs for a tier, beside its
// answer to what it calls a terminal (nix/home.nix).
const askFile = "ask.json"

// MethodAskRun runs a tier. Answered by the connection it arrives on rather
// than by the dispatcher, because the answer is not one reply (see askRun and
// handle).
const MethodAskRun = "ask.run"

// askTimeout bounds one answer. A tier that never returns would otherwise leave
// a surface waiting for ever with nothing on it, which is the whole failure this
// path is arranged against; two minutes is long enough for a hard question on a
// local model that has to load one, and short enough that a wedged one ends in
// words somebody can read.
//
// It is also what unsticks a window: the surface will not take a second
// question while an answer is on its way, and this is what makes "on its way"
// end for a tier that has stopped answering without exiting.
const askTimeout = 2 * time.Minute

// askSurface asks the shell to open the popup or the panel. The picker's
// bargain exactly (see switcher): zded tells whoever is listening, and waits to
// hear that something actually drew it.
//
// The answer is only whether it was shown. There is nothing to fall back to
// printing - the question has not been asked yet - so the caller says how to ask
// from a terminal instead, and that is the CLI's business rather than the
// daemon's.
func (s *Server) askSurface(kind string) Response {
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		// Which screen is a detail; not knowing it is not worth refusing over,
		// and the shell falls back to the screen it can see.
		output = ""
	}
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	if sent := s.broadcast(Event{Kind: kind, Output: output, Token: token}); sent == 0 {
		return ok(false)
	}
	select {
	case <-acked:
		return ok(true)
	case <-time.After(ackWait):
		return ok(false)
	}
}

// askRun runs the tier and streams what it says back to the one connection that
// asked for it.
//
// To that connection and not to every listener, which is the difference between
// an answer and an announcement: a question asked on the local tier because it
// is nobody else's business has no business arriving at every surface that
// happens to be subscribed.
//
// In pieces rather than in one reply at the end, because an answer arrives over
// seconds and a window that shows nothing until the last token is a window that
// looks broken - which is the failure mode this whole component is arranged to
// design out. The pieces are events, because that is the line shape zded already
// pushes and every client here already knows how to tell from a reply.
//
// In its own goroutine, because the connection it arrives on has other work to
// do meanwhile: the shell acknowledges a picker on the connection it asked on,
// and a twenty second answer holding the read loop would mean Mod+Tab printing a
// list for the whole of it. zded answers every keybind, and none of them may
// wait for this.
//
// Nothing is written down anywhere. There is no history by default
// (docs/vision.md, section 2), and the way to have none is to write none: no
// journal entry, no cache, no transcript. What the surface shows is in the
// surface, and closing it is what forgetting is.
func (s *Server) askRun(k *sink, args []string) {
	if len(args) != 2 {
		k.reply(Response{Error: MethodAskRun + " takes a tier and a question"})
		return
	}
	tier, question := args[0], strings.TrimSpace(args[1])
	if question == "" {
		k.reply(Response{Error: "nothing to ask: say what the question is"})
		return
	}
	argv, err := askTier(tier)
	if err != nil {
		// Before anything runs, and as the reply rather than as a chunk: a
		// machine with no tier configured has to hear which option to set, and
		// hear it from the CLI and the surface alike.
		k.reply(Response{Error: err.Error()})
		return
	}
	// Started, said first, so a client reading its connection in order never
	// meets a piece of an answer before it has been told there is one coming.
	k.reply(ok("asking"))

	ctx, cancel := context.WithTimeout(context.Background(), askTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// The question, and only the question. A tier is a program that reads one
	// and answers it: no arguments to quote, and nothing about it on a command
	// line that `ps` prints for the whole machine to read.
	cmd.Stdin = strings.NewReader(question)
	// Whatever it complains about, kept for the end. A tier that fails says why
	// here - the wrong model name, no credentials in the container, no network
	// where it wanted one - and without it a surface can report an exit status
	// and nothing else.
	var complaint bytes.Buffer
	cmd.Stderr = &complaint
	out, err := cmd.StdoutPipe()
	if err != nil {
		askDone(k, err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			askDone(k, fmt.Sprintf("the %s tier is configured to run %q, which is not there", tier, argv[0]))
			return
		}
		askDone(k, err.Error())
		return
	}
	said := pump(k, out, cancel)
	werr := cmd.Wait()

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// "Finish" rather than "answer", because this is also where a tier that
		// was streaming happily and never stopped ends up.
		askDone(k, fmt.Sprintf("the %s tier did not finish within %s", tier, askTimeout))
	case werr != nil:
		askDone(k, fmt.Sprintf("the %s tier failed: %s", tier, complaintOf(&complaint, werr)))
	case said == 0:
		// A window that never answers is the thing to design out, and a tier
		// exiting happily having said nothing is exactly one - which is also
		// what a mis-typed command usually does.
		askDone(k, "the "+tier+" tier ran and said nothing")
	default:
		askDone(k, "")
	}
}

// askDone ends the stream, with the reason when there is one. Every way out of
// a run goes through it: a surface that never hears the end is a surface waiting
// for a piece that is not coming.
func askDone(k *sink, reason string) {
	k.send(Event{Kind: EventAskText, Done: true, Error: reason})
}

// pump copies the tier's output to the connection as it arrives, and answers
// with how much of it there was.
//
// As it arrives, not a line at a time: a model that streams tokens has no reason
// to end them with newlines, and buffering until one would hold a paragraph back
// until the paragraph after it started.
func pump(k *sink, r io.Reader, stop func()) int {
	buf := make([]byte, 4096)
	var carry []byte
	said := 0
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			keep := partialRune(chunk)
			carry = append([]byte(nil), chunk[len(chunk)-keep:]...)
			if chunk = chunk[:len(chunk)-keep]; len(chunk) > 0 {
				said += len(chunk)
				if err := k.send(Event{Kind: EventAskText, Text: string(chunk)}); err != nil {
					// Nobody is reading it any more - the terminal was closed,
					// the surface went away - so the rest of this answer is for
					// nobody. Stopping the tier is the point: a container left
					// generating into a closed pipe is somebody's quota.
					stop()
					return said
				}
			}
		}
		if err != nil {
			if len(carry) > 0 {
				// Whatever it ended on, even if those bytes are not a character:
				// one replacement mark at the very end beats silently dropping
				// what a tier said last.
				said += len(carry)
				k.send(Event{Kind: EventAskText, Text: string(carry)})
			}
			return said
		}
	}
}

// partialRune is how many bytes at the end of b start a character whose
// remaining bytes have not arrived yet.
//
// A read ends wherever the pipe had bytes, which is not where a character ends,
// and json.Marshal turns half of one into a replacement mark - so an answer in
// Cyrillic would come out pitted with them, one per buffer boundary. Those bytes
// wait for the read that finishes them. A byte that is not the start of any
// character waits too, and is flushed at the end rather than dropped.
func partialRune(b []byte) int {
	for i := len(b) - 1; i >= 0 && len(b)-i < utf8.UTFMax; i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if r, size := utf8.DecodeRune(b[i:]); r == utf8.RuneError && size <= 1 {
			return len(b) - i
		}
		return 0
	}
	return 0
}

// complaintOf is what to say a tier failed with: its own words where it had
// any, and the exit status where it said nothing.
func complaintOf(complaint *bytes.Buffer, err error) string {
	msg := strings.TrimSpace(complaint.String())
	if msg == "" {
		return err.Error()
	}
	// Bounded, because a tier that logs its whole startup to stderr should not
	// be able to put a page of it into a popup. Runes rather than bytes: the
	// reason it failed may well be written in the language it was asked in.
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "..."
	}
	return msg
}

// askTier is what this machine runs for that tier, or why it cannot.
//
// The same file shape as the apps (internal/apps): a name to an argv, written by
// layer 1 and read here. A tier is not an app - it is not launched, it is run
// with a question on its stdin and it draws nothing - but "what this machine
// calls that" is one question, and answering it twice is two places for the
// answer to drift.
func askTier(tier string) ([]string, error) {
	path := apps.Path(askFile)
	all, err := apps.Load(path)
	if err != nil {
		return nil, err
	}
	if argv := all[tier]; len(argv) > 0 {
		return argv, nil
	}
	// The option, by name. "No tier configured" leaves somebody reading three
	// documents to find out what to type; this is a line they can paste into a
	// config, and it is the whole of failing closed and loud.
	msg := fmt.Sprintf("no %s tier to ask: set zde.ask.tiers.%s in your home-manager config, which is what writes %s",
		tier, tier, path)
	if names := all.Names(); len(names) > 0 {
		msg += " (this machine has " + strings.Join(names, ", ") + ")"
	}
	return nil, errors.New(msg)
}
