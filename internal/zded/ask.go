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
	"os"
	"os/exec"
	"strings"
	"syscall"
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
// end for a tier that has stopped answering without exiting. That only became
// true with the pipe below being zde's own - until then this deadline fired
// into a goroutine that was blocked in a read nothing would end.
const askTimeout = 2 * time.Minute

// askGrace is how long Wait may go on waiting after the tier has been told to
// stop. It is the guard on the one pipe zde does not own - stderr, which exec
// copies for us - and on a process that somehow survives its group being
// killed. Short, because by this point everything that was going to answer has.
const askGrace = 5 * time.Second

// askDrain is how long the answer may keep arriving after the tier has exited.
// Enough for what is already in the pipe to be read, and no longer: past that,
// what is holding the pipe open is something the tier forked and not the answer.
// A tier that exits before it has written its answer is broken, and what it did
// write is what there is.
const askDrain = 200 * time.Millisecond

// askSendWait is how long one piece of an answer may take to be taken by the
// client it is being written to. A client that has not read a byte for five
// seconds while an answer is being pushed at it is not going to - and until this
// existed the whole path used the broadcast's 200ms, which a window doing layout
// on the thread that drains its socket trips at the first long paragraph. The
// answer then stopped mid-sentence, the end of it failed the same way, and the
// window waited for ever.
const askSendWait = 5 * time.Second

// askMax bounds one answer. zded itself does not care - it streams and forgets
// - but the window keeps the whole thing in one text item on the thread that
// draws the bar, so a tier stuck in a loop is not just a long answer, it is the
// shell. A quarter of a megabyte is about forty pages: past that, nothing is
// being answered any more.
const askMax = 256 << 10

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
	// One answer at a time down one connection (see sink.asking). Claimed after
	// the request has been read and understood, so that a second ask with a tier
	// nobody configured still hears about the tier - the more useful of the two
	// things wrong with it.
	if !k.asking.CompareAndSwap(false, true) {
		k.reply(Response{Error: "this connection is still answering the last question: wait for it, or ask on another"})
		return
	}
	defer k.asking.Store(false)
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

	// The pipe is ours rather than one exec owns, because EOF on it needs every
	// holder of the write end to let go of it - and a tier that forks leaves one
	// behind. `zcr run <app> --exec` and a shell script are the two shapes this
	// option exists for and both fork, so this is the ordinary case and not an
	// exotic one: with exec's own pipe, a tier whose child outlived it parked
	// this goroutine in Read for the life of the session, past the timeout,
	// because the timeout is enforced by Wait and Wait is after the read.
	pr, pw, err := os.Pipe()
	if err != nil {
		askDone(k, err.Error())
		return
	}
	cmd.Stdout = pw
	// Its own process group, so that stopping the tier stops what the tier
	// started. Killing the one pid zde knows about leaves exactly the children
	// that were the problem still running, still holding the pipe.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killGroup(cmd)
		return nil
	}
	// The second guard, and the one that covers stderr: that pipe is exec's,
	// with a copying goroutine Wait waits for, and the same forked child can
	// hold it open after the process is gone. WaitDelay is what closes it and
	// lets Wait return instead of waiting on a grandchild nobody asked about.
	cmd.WaitDelay = askGrace

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		// One sentence for every way a tier fails to start, naming the tier,
		// what it tried to run, and the option that would fix it.
		// exec.ErrNotFound was the only case this used to name, and it is the
		// one a zde machine almost never has: nix writes an absolute store path
		// into this option and exec only consults PATH for a bare name, so a
		// stale store path arrived as "fork/exec /nix/store/...: no such file or
		// directory" and named neither the tier nor the option.
		reason := err
		if inner := errors.Unwrap(err); inner != nil {
			// The path is already in the sentence, so what is wanted from the
			// error is the reason rather than a second copy of it.
			reason = inner
		}
		askDone(k, fmt.Sprintf("the %s tier could not start %q: %s (set zde.ask.tiers.%s to something this machine has)",
			tier, argv[0], reason, tier))
		return
	}
	// zded's own copy of the write end, closed now: while this process holds it,
	// nothing the tier does can produce an EOF on the read end.
	pw.Close()
	// And the read end, when the run is over one way or the other. This close is
	// what unblocks pump: the deadline fires, or something cancels, or this
	// function returns and its deferred cancel runs.
	go func() {
		<-ctx.Done()
		pr.Close()
	}()

	// Waiting alongside the reading rather than after it, which is the other
	// half of the same lesson: an answer is over when the tier is, and not when
	// the last thing the tier forked lets go of the pipe. Once the process has
	// gone, pump gets a deadline - long enough to drain what is already in the
	// pipe, short enough that a forked child holding it is not somebody's whole
	// session. The pipe is zde's, so reading it while Wait runs is safe in a way
	// it never is with exec's own.
	waited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pr.SetReadDeadline(time.Now().Add(askDrain))
		waited <- err
	}()

	said := pump(k, pr, cancel)
	werr := <-waited
	// Whatever it left behind. Cancel is not called once Wait has returned, so
	// without this a tier that forked leaves its children running for as long as
	// they feel like: the run is over, and a run that is over should not still
	// be producing anything.
	killGroup(cmd)

	switch {
	case said >= askMax:
		// Stopped rather than quietly truncated: the window holds the whole
		// answer in one text item, so a tier that has started looping takes the
		// bar and the picker down with it, and dropping the rest without saying
		// so would read as the answer ending.
		askDone(k, fmt.Sprintf("the %s tier went past %d KiB and was stopped: what is above is as much of the answer as arrived",
			tier, askMax>>10))
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

// killGroup stops the tier and everything the tier started.
//
// The group and not the process, because a tier that forks is the ordinary
// shape here and killing the one pid zde knows about leaves exactly the
// children that were the problem. ESRCH means there is nothing left, which is
// the outcome this exists for, so nothing is reported.
//
// What it does not reach is worth being plain about: a tier that is a `zcr run`
// client attached to a container leaves the container running when the client
// dies, so this stops zde's side of the work and not always the work. Stopping
// that is zinc's to offer. Reasoning from how podman treats an attached client,
// not something measured here.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// askDone ends the stream, with the reason when there is one. Every way out of
// a run that started goes through it, and a run that never started answers with
// a refusal instead: either way something comes back, because a surface that
// hears neither is a surface waiting for a piece that is not coming.
//
// On the answer's own patience rather than a broadcast's, and the difference is
// the whole reason this is not a plain send: a broadcast gives a listener 200ms
// because a wedged shell must not hold up a keypress, and this is one of
// thousands of pieces being pushed at a client that also has to draw them.
func askDone(k *sink, reason string) {
	k.sendWithin(Event{Kind: EventAskText, Done: true, Error: reason}, askSendWait)
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
				if err := k.sendWithin(Event{Kind: EventAskText, Text: string(chunk)}, askSendWait); err != nil {
					// The answer cannot be delivered: the terminal was closed,
					// or the surface stopped reading for longer than a client
					// being written to ever should. sendWithin has closed the
					// connection, so whoever is there sees the end of it rather
					// than a half-written line and a wait with no end.
					//
					// Stopping the tier is what is left to do. It stops the
					// process zde started and everything in its group, which is
					// as far as this reaches: a tier that is a `zcr run` client
					// attached to a container leaves the container running, and
					// stopping that is zinc's to offer, not something a killed
					// client does. Untested reasoning, from how podman treats an
					// attached client rather than from a measurement here.
					stop()
					return said
				}
				if said >= askMax {
					// The cap, checked after the piece that reached it so that
					// what arrives is a whole answer up to the line rather than
					// one cut mid-character. Stopping the tier is the point of
					// having it at all.
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
				k.sendWithin(Event{Kind: EventAskText, Text: string(carry)}, askSendWait)
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
