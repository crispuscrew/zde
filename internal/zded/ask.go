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
//
// The panel asks more than once, so what went before has to reach the tier
// somehow, and the only thing that reliably crosses `zcr run <app> --exec` into
// a container is the three standard streams. So it goes on stdin, in front of
// the question, framed (see askDoc). zded still keeps none of it: the turns
// arrive with each request from the surface that is showing them, which is what
// keeps "closing the window is forgetting" true.

import (
	"context"
	"encoding/json"
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
// stop. With both pipes held here rather than by exec, all it guards is a
// process that outlives its own group being killed, which is a stopped one or
// one in a state no signal reaches. Short, because by this point everything
// that was going to answer has.
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

// complaintMax is how much of a tier's stderr is worth keeping to explain a
// failure with. Generous for a stack trace, and nothing like enough to be a
// place a daemon accidentally stores a log.
const complaintMax = 64 << 10

// askMax bounds one answer. zded itself does not care - it streams and forgets
// - but the window keeps the whole thing in one text item on the thread that
// draws the bar, so a tier stuck in a loop is not just a long answer, it is the
// shell. A quarter of a megabyte is about forty pages: past that, nothing is
// being answered any more.
const askMax = 256 << 10

// askContextMax bounds a whole conversation: everything a tier is handed on one
// run, the turns before the question and the question itself.
//
// Measured on the document exactly as the tier receives it (see askDoc), which
// is the frame line, the turns, and the JSON that separates them. That is the
// only measurement that means anything to somebody who has to decide what will
// fit: a budget counted on some other rendering of the same words is a budget
// the panel cannot advertise, and it offers ctrl+v paste.
//
// It exists because a conversation that grows without bound is a bill, then a
// timeout, then a tier that refuses every question because the last twenty are
// still in front of it. 64 KiB is about sixteen thousand tokens - twenty or so
// ordinary exchanges - and it is chosen together with askTimeout rather than
// separately: a tier is asked to read the conversation and answer within the
// same two minutes as a bare question, so the conversation may not grow to a
// length that makes that deadline a lie. A local model reprocessing this much
// has time to answer; four times this and two minutes would be a number that
// only ever fires.
//
// It is not askMax, and the two bound different things. askMax is what one
// answer may be, because the window holds it in one text item. This is what one
// run may read. One answer that reached askMax is by itself four times this,
// which ends the conversation loudly at the next question rather than quietly
// dropping the turns that no longer fit.
const askContextMax = 64 << 10

// askFrame is the first line of stdin when there is more than a question on it.
// A tier that has never heard of it reads one and knows this is not the bare
// question it was written for; a tier that has reads the rest as turns. Versioned
// so that a second shape later is a different line rather than a guess.
const askFrame = "zde-ask 1"

// Who said a turn. "tier" and not "model", because that is all zde knows: what
// came back last time from the command this machine names, which may be a model
// or may be a shell script.
const (
	askWhoPerson = "person"
	askWhoTier   = "tier"
)

// askTurn is one turn of the conversation, as the tier reads it.
type askTurn struct {
	Who  string `json:"who"`
	Text string `json:"text"`
}

// askDoc is what goes on the tier's stdin: the question on its own where there
// is nothing before it, and the whole conversation where there is.
//
// The encoding is the decision here, and it is made about a program somebody
// else wrote. A tier is handed this and has to say which words were typed by a
// person and which it produced itself last time, because that is the difference
// between context and instructions. A plain transcript - "person:" and "tier:"
// down the left margin - cannot support that claim: an answer containing a line
// that begins "person:" is indistinguishable from a person's turn, and getting a
// model to emit one is a sentence of somebody's pasted text away. The frame has
// to be one the payload cannot forge.
//
// So: one JSON object per line, one line per turn, with the role a field rather
// than a prefix. Escaping is the encoder's, not a quoting rule written here, so
// no newline, brace or "who" inside a turn's text can end that turn early. Line
// delimited because that is the shape zded already speaks everywhere else, so a
// tier author meets one format in this project rather than two - and because
// the question is the last line, a tier that wants only the question can take
// the last line and ignore the rest.
//
// Roles come from position and never from the wire: the caller sends the turns
// before the question alternating, asked then answered, so nothing an answer
// contains can arrive claiming to be a question.
//
// Unframed where there is nothing before the question, which is every oneshot,
// every `zde ask` in a terminal, and the panel's first question: those are
// byte-for-byte what a tier has always been handed. The exception is a question
// whose own first line is the frame, which is framed as one turn so that a tier
// never has to guess what it is reading.
func askDoc(prior []string, question string) string {
	if len(prior) == 0 && !framed(question) {
		return question
	}
	var b strings.Builder
	b.WriteString(askFrame)
	b.WriteByte('\n')
	for i, text := range prior {
		// Asked, answered, asked, answered. The caller's arity check is what
		// makes the last one before the question an answer.
		who := askWhoPerson
		if i%2 == 1 {
			who = askWhoTier
		}
		writeTurn(&b, who, text)
	}
	writeTurn(&b, askWhoPerson, question)
	return b.String()
}

// overKiB is n in KiB, rounded up. Up rather than down, because the only place
// this number is printed is beside the bound it has just broken: 65537 bytes
// truncates to 64, the bound is 64, and "this conversation has reached 64 KiB,
// and one ask carries 64" is a refusal arguing against itself in the sentence
// that delivers it. Rounded up, anything past the bound reads as past it - the
// smallest thing that can be refused is one byte over, and one byte over is 65.
func overKiB(n int) int {
	return (n + 1<<10 - 1) >> 10
}

// framed is whether a question's own first line is the frame marker. Only then,
// and not merely starting with it, because "zde-ask 1 is what?" is a question
// and not a document.
func framed(question string) bool {
	line, _, _ := strings.Cut(question, "\n")
	return line == askFrame
}

// writeTurn is one line of the document.
//
// Through an encoder with HTML escaping turned off rather than json.Marshal,
// and that is a decision about what a conversation costs rather than about how
// it looks. Marshal writes "<", ">" and "&" as six-byte unicode escapes, so
// that its output is safe to drop inside a script tag - which is nowhere any of
// this goes: a tier reads it on stdin. Left on, a
// question with a patch or a page of markup pasted into it is weighed at up to
// six times what it is against askContextMax, so a panel that offers ctrl+v
// paste would refuse a fraction of the budget it advertises, and the person
// pasting has no way to see why.
//
// Nothing about the frame relies on it. The encoder still escapes the quote,
// the backslash and every control character including the newline, so no turn's
// text can end that turn early or start one of its own, which is the whole of
// what askDoc rests on.
//
// The error is discarded because marshalling two strings into a strings.Builder
// has none: bytes that are not a character become a replacement mark rather
// than a failure, which is what the same text already met on its way out to the
// surface that has just sent it back, so nothing is lost here that was not lost
// already. Discarded rather than skipping the turn, too - a turn quietly
// missing from the middle would move every role after it along by one.
func writeTurn(b *strings.Builder, who, text string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	// Encode writes the newline that ends the turn itself.
	_ = enc.Encode(askTurn{Who: who, Text: text})
}

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
// surface, and closing it is what forgetting is. The turns before a question
// arrive with it, from the surface that is showing them, so that stays true
// once a panel is a conversation: the daemon reads them, hands them to one
// process, and has forgotten them by the time the answer ends.
func (s *Server) askRun(k *sink, args []string) {
	// A tier, a question, and then the conversation before it in pairs: what
	// was asked, what came back, oldest first. Pairs rather than a role on each
	// one, because a role that travels as data is a role something in an answer
	// can claim (see askDoc).
	if len(args) < 2 || len(args)%2 != 0 {
		k.reply(Response{Error: MethodAskRun + " takes a tier, a question, and the turns before it in pairs: what was asked, what came back"})
		return
	}
	tier, question := args[0], strings.TrimSpace(args[1])
	if question == "" {
		k.reply(Response{Error: "nothing to ask: say what the question is"})
		return
	}
	prior := args[2:]
	for _, turn := range prior {
		if strings.TrimSpace(turn) == "" {
			// An empty turn would tell a tier that somebody said nothing, or
			// that it answered with nothing, and both are things it would be
			// entitled to act on. A conversation with a hole in it is not one.
			k.reply(Response{Error: "one of the turns before that question is empty: a question nothing answered is not a turn to carry"})
			return
		}
	}
	argv, err := askTier(tier)
	if err != nil {
		// Before anything runs, and as the reply rather than as a chunk: a
		// machine with no tier configured has to hear which option to set, and
		// hear it from the CLI and the surface alike.
		k.reply(Response{Error: err.Error()})
		return
	}
	doc := askDoc(prior, question)
	if len(doc) > askContextMax {
		// Before anything runs and before anything is spent, and as a refusal
		// rather than a truncation: dropping the oldest turns to fit would
		// leave a panel showing a conversation the tier can no longer see, and
		// a person reading a screen that is a lie about what was asked. So it
		// is said, and starting again is somebody's decision to make.
		if len(prior) == 0 {
			k.reply(Response{Error: fmt.Sprintf("that question is %d KiB, and one ask carries %d: ask it in fewer words",
				overKiB(len(doc)), askContextMax>>10)})
			return
		}
		k.reply(Response{Error: fmt.Sprintf("this conversation has reached %d KiB, and one ask carries %d: start a fresh one and ask it there",
			overKiB(len(doc)), askContextMax>>10)})
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

	// Under the daemon's own context rather than Background, so that a session
	// ending is one of the things that ends a run. The tier is in a process
	// group of its own (below), which is what makes it survivable in the first
	// place: the group signal that stops everything else in the session does not
	// reach it, so the only thing that can is this cancel, through cmd.Cancel.
	ctx, cancel := context.WithTimeout(s.runCtx, askTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// The question, and what came before it, and nothing else. A tier is a
	// program that reads on stdin and answers on stdout: no arguments to quote,
	// and nothing about any of it on a command line that `ps` prints for the
	// whole machine to read. Bounded by askContextMax above, which is also
	// about a pipe's worth, so an ordinary run hands this over in one write
	// rather than waiting on a tier that reads late.
	cmd.Stdin = strings.NewReader(doc)

	// Both pipes are zde's own rather than exec's, because EOF on one needs
	// every holder of the write end to let go - and a tier that forks leaves one
	// behind. `zcr run <app> --exec` and a shell script are the two shapes this
	// option exists for, and both fork, so this is the ordinary case rather than
	// an exotic one.
	//
	// With exec's pipe for stdout, a tier whose child outlived it parked this
	// goroutine in a read for the life of the session, past the timeout, because
	// the timeout is enforced by Wait and Wait comes after the read. With exec's
	// pipe for stderr, that same child held the copying goroutine Wait does wait
	// for: measured against a shell tier that backgrounds one thing, five
	// seconds of nothing and then a failure reported for an answer that had
	// arrived perfectly.
	pr, pw, err := os.Pipe()
	if err != nil {
		askDone(k, err.Error())
		return
	}
	er, ew, err := os.Pipe()
	if err != nil {
		pr.Close()
		pw.Close()
		askDone(k, err.Error())
		return
	}
	cmd.Stdout = pw
	// Whatever it complains about, kept for the end. A tier that fails says why
	// there - the wrong model name, no credentials in the container, no network
	// where it wanted one - and without it a surface can report an exit status
	// and nothing else.
	cmd.Stderr = ew
	// Its own process group, so that stopping the tier stops what the tier
	// started. Killing the one pid zde knows about leaves exactly the children
	// that were the problem still running, still holding the pipe.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killGroup(cmd)
		return nil
	}
	// The last guard, for a process that outlives its own group being killed -
	// one stopped, or one in a state the signal cannot reach. With both pipes
	// held here, this is all WaitDelay has left to do.
	cmd.WaitDelay = askGrace

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		er.Close()
		ew.Close()
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
	// zded's own copies of the write ends, closed now: while this process holds
	// one, nothing the tier does can produce an EOF on the read end.
	pw.Close()
	ew.Close()
	// And the read ends, when the run is over one way or the other. That close
	// is what unblocks the two readers below: the deadline fires, or something
	// cancels, or this function returns and its deferred cancel runs.
	go func() {
		<-ctx.Done()
		pr.Close()
		er.Close()
	}()
	// Emptied while the answer arrives, not after it: a pipe nobody reads fills
	// up, and a tier blocked writing to stderr is a tier that has stopped
	// answering because zde stopped listening.
	complaint := make(chan []byte, 1)
	go func() { complaint <- drain(er, complaintMax) }()

	// Waiting alongside the reading rather than after it, which is the other
	// half of the same lesson: an answer is over when the tier is, and not when
	// the last thing the tier forked lets go of the pipe. Once the process has
	// gone, both readers get a deadline - long enough to drain what is already
	// in the pipe, short enough that a forked child holding it is not somebody's
	// whole session. The pipes are zde's, so reading them while Wait runs is
	// safe in a way it never is with exec's own.
	waited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pr.SetReadDeadline(time.Now().Add(askDrain))
		er.SetReadDeadline(time.Now().Add(askDrain))
		waited <- err
	}()

	said := pump(k, pr, cancel)
	werr := <-waited
	// Whatever it left behind. Cancel is not called once Wait has returned, so
	// without this a tier that forked leaves its children running for as long as
	// they feel like: the run is over, and a run that is over should not still
	// be producing anything.
	killGroup(cmd)
	complained := <-complaint

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
		askDone(k, fmt.Sprintf("the %s tier failed: %s", tier, complaintOf(complained, werr)))
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

// drain reads everything a tier writes to stderr and keeps the first max bytes.
//
// Everything, because a pipe nobody empties fills up and stops the tier writing
// to it, and a tier that says a lot before it answers would then be one zde had
// quietly stopped. The first max bytes, because what is wanted is the reason it
// failed and not its whole startup log kept in a daemon's memory.
func drain(r io.Reader, max int) []byte {
	var kept []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 && len(kept) < max {
			kept = append(kept, buf[:min(n, max-len(kept))]...)
		}
		if err != nil {
			return kept
		}
	}
}

// complaintOf is what to say a tier failed with: its own words where it had
// any, and the exit status where it said nothing.
func complaintOf(complaint []byte, err error) string {
	msg := strings.TrimSpace(string(complaint))
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
