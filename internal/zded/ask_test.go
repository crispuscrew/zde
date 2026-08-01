package zded

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/crispuscrew/zde/internal/journal"
)

// The fake tier is this test binary, run again with a mode after --.
//
// Something has to be on the other side of exec for any of this to be tested,
// and every candidate on PATH is a bet about what a build sandbox contains. The
// one binary that is certainly there is the one already running. os/exec's own
// tests do exactly this.
const fakeTierMark = "zde-fake-tier"

// fakeTierGap is how long the streaming tier waits between its two pieces. Long
// enough that "the first piece arrived before the tier exited" is a measurement
// and not a coin toss.
const fakeTierGap = 400 * time.Millisecond

func fakeTier(mode string, extra ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestFakeTier$", "--", fakeTierMark, mode}, extra...)
}

// TestFakeTier is a tier when it is run as one, and nothing at all in an
// ordinary test run. It exits before the testing framework prints anything,
// because "PASS" on stdout would otherwise be part of every answer.
func TestFakeTier(t *testing.T) {
	args := flag.Args()
	if len(args) < 2 || args[0] != fakeTierMark {
		return
	}
	code := 0
	switch args[1] {
	case "echo":
		// The question comes in on stdin and nowhere else, which is the seam
		// this whole component is: read it, answer it.
		q, _ := io.ReadAll(os.Stdin)
		fmt.Fprintf(os.Stdout, "answered: %s", q)
	case "stream":
		fmt.Fprint(os.Stdout, "one")
		time.Sleep(fakeTierGap)
		fmt.Fprint(os.Stdout, "two")
	case "silent":
		// Exits happily, says nothing: what a mis-typed command does.
	case "slow":
		time.Sleep(2 * time.Second)
		fmt.Fprint(os.Stdout, "eventually")
	case "angry":
		fmt.Fprintln(os.Stderr, "no credentials in this container")
		code = 1
	case "cyrillic":
		// Past the read buffer, in characters that do not fit in one byte: the
		// carry that holds back half a character is invisible below 4096 bytes.
		fmt.Fprint(os.Stdout, strings.Repeat(cyrillicWord, cyrillicTimes))
	case "loop":
		// A tier that has stopped answering and started repeating itself, which
		// is the shape the cap exists for. Unbounded on purpose.
		for {
			if _, err := fmt.Fprint(os.Stdout, strings.Repeat("x", 4096)); err != nil {
				break
			}
		}
	case "fork":
		// A tier that leaves a child holding its stdout and exits, which is what
		// a shell script and a runner both do. The child says where it is, so a
		// test can ask afterwards whether it was left running.
		grand := exec.Command(os.Args[0], "-test.run=^TestFakeTier$", "--", fakeTierMark, "linger", args[2])
		grand.Stdout = os.Stdout
		// And stderr, which is what a shell gives a backgrounded child and what
		// makes this the reviewer's case rather than a tidier one: that pipe was
		// exec's, with a copying goroutine Wait waits for, so holding it cost
		// five seconds and a failure reported for an answer that was fine.
		grand.Stderr = os.Stderr
		if err := grand.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
			break
		}
		fmt.Fprint(os.Stdout, "answered and forked")
	case "linger":
		os.WriteFile(args[2], []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(30 * time.Second)
	}
	os.Exit(code)
}

// A word that is two bytes per character, and enough of them to cross the read
// buffer several times.
const (
	cyrillicWord  = "привет "
	cyrillicTimes = 3000
)

// writeTiers puts a tier file where zded reads one, in a config directory of
// this test's own.
func writeTiers(t *testing.T, tiers map[string][]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, "config")
	t.Setenv("XDG_CONFIG_HOME", config)
	if err := os.MkdirAll(filepath.Join(config, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tiers)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "zde", askFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// askAll runs one ask over a real socket and gives back what a client would
// have printed, and how it ended.
//
// Over a socket rather than through Dispatch, because ask.run is answered by the
// connection and not by the dispatcher (see handle) - a test that went round
// that would be testing something nothing calls.
func askAll(t *testing.T, path, tier, question string) (text, failure string) {
	t.Helper()
	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodAskRun, nil, tier, question); err != nil {
		return "", err.Error()
	}
	var b strings.Builder
	for {
		ev, err := c.NextEventBefore(time.Now().Add(20 * time.Second))
		if err != nil {
			t.Fatalf("waiting for the answer: %v", err)
		}
		if ev.Kind != EventAskText {
			continue
		}
		b.WriteString(ev.Text)
		if ev.Done {
			return b.String(), ev.Error
		}
	}
}

func askServer(t *testing.T) string {
	t.Helper()
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	return serve(t, s)
}

// The seam itself: the tier is run with the question on its stdin, and what it
// says comes back. If this regresses, ask is a window with a text field and
// nothing behind it.
func TestAskRunsTheTierWithTheQuestionOnItsStdin(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("echo")})
	text, failure := askAll(t, askServer(t), TierProvider, "what is the capital of peru")
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if text != "answered: what is the capital of peru" {
		t.Errorf("the tier answered %q", text)
	}
}

// And the pieces arrive as they are produced rather than all at the end. A
// popup that shows nothing until the last token is the failure this component
// is arranged to avoid, and it looks identical from outside unless the timing
// is measured.
func TestAskStreamsTheAnswerAsItArrives(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("stream")})
	path := askServer(t)

	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodAskRun, nil, TierProvider, "say two things"); err != nil {
		t.Fatalf("ask.run: %v", err)
	}

	start := time.Now()
	var first, end time.Duration
	var b strings.Builder
	for {
		ev, err := c.NextEventBefore(time.Now().Add(20 * time.Second))
		if err != nil {
			t.Fatalf("waiting for the answer: %v", err)
		}
		if ev.Kind != EventAskText {
			continue
		}
		if ev.Text != "" && first == 0 {
			first = time.Since(start)
		}
		b.WriteString(ev.Text)
		if ev.Done {
			end = time.Since(start)
			if ev.Error != "" {
				t.Fatalf("the ask failed: %s", ev.Error)
			}
			break
		}
	}
	if b.String() != "onetwo" {
		t.Errorf("the answer is %q, want both pieces of it", b.String())
	}
	// Half the gap: the first piece was written straight away and the second
	// only after it, so anything that collects the answer and sends it once at
	// the end lands both at the same moment.
	if end-first < fakeTierGap/2 {
		t.Errorf("the first piece arrived %v in and the answer ended %v in: nothing was streamed", first, end)
	}
}

// A tier nobody configured has to say which option would configure it. The
// alternative is a window that comes up empty and never answers, which is
// indistinguishable from every other kind of broken.
func TestAskWithNoTierSaysWhichOptionToSet(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("echo")})
	_, failure := askAll(t, askServer(t), TierLocal, "something private")
	if !strings.Contains(failure, "zde.ask.tiers.local") {
		t.Errorf("the refusal is %q, and does not say which option to set", failure)
	}
	// And what the machine does have, because the usual way to be here is a
	// tier that was set under another name.
	if !strings.Contains(failure, TierProvider) {
		t.Errorf("the refusal is %q, and does not say what this machine has", failure)
	}
}

// A tier that exits happily having said nothing is a window that never answers,
// so it ends in words instead. This is the same failure as no tier at all, one
// layer further in - a command name that resolves and does nothing.
func TestATierThatSaysNothingIsAFailure(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("silent")})
	text, failure := askAll(t, askServer(t), TierProvider, "anything")
	if text != "" {
		t.Errorf("the silent tier answered %q", text)
	}
	if !strings.Contains(failure, "said nothing") {
		t.Errorf("a tier that said nothing ended with %q", failure)
	}
}

// A tier that leaves a child holding its stdout must not hold the run open.
// This is the ordinary shape of the two things the option is for - a runner and
// a script - and it used to park the answer in a read that nothing would end:
// past the timeout, because the timeout is enforced by a Wait that came after
// the read, for the life of the session, with the tier still running.
func TestATierThatForksDoesNotWedgeTheRun(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild")
	writeTiers(t, map[string][]string{TierProvider: fakeTier("fork", pidFile)})
	path := askServer(t)

	start := time.Now()
	text, failure := askAll(t, path, TierProvider, "answer and fork")
	took := time.Since(start)
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if text != "answered and forked" {
		t.Errorf("the tier answered %q", text)
	}
	// Far above the drain and well below anything a person would sit through:
	// what is being pinned is that the run ends with the tier rather than with
	// whatever the tier left holding a pipe. Two seconds also fails the version
	// of this where stderr is exec's, which cost the whole of WaitDelay.
	if took > 2*time.Second {
		t.Errorf("the run took %v for a tier that answered and exited at once", took)
	}

	// And what it forked is not still running. A run that is over should not
	// still be producing anything, and the child is in the tier's process group
	// precisely so that it can be stopped with it.
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the forked child never said where it was: %v", err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	gone := func() bool { return syscall.Kill(pid, 0) != nil }
	for i := 0; i < 200 && !gone(); i++ {
		// Signals are not instant, and this is the only thing in the test that
		// happens after the answer rather than before it.
		time.Sleep(10 * time.Millisecond)
	}
	if !gone() {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the tier forked a child and the run left it running")
	}
}

// An answer with no end to it is stopped, and said to have been. zded streams
// and forgets, so the reason is the window: it holds the whole answer in one
// text item on the thread that draws the bar.
func TestAnEndlessAnswerIsCappedAndSaysSo(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("loop")})
	text, failure := askAll(t, askServer(t), TierProvider, "go on for ever")
	if len(text) > askMax+64<<10 {
		t.Errorf("the answer ran to %d bytes with a cap of %d", len(text), askMax)
	}
	if len(text) < askMax {
		t.Errorf("the answer stopped at %d bytes, short of the cap of %d", len(text), askMax)
	}
	if !strings.Contains(failure, "was stopped") {
		t.Errorf("the answer was capped and the client was told %q", failure)
	}
}

// A tier that cannot start names the tier, what it tried to run and the option
// to fix. The path form is the one that matters: nix writes an absolute store
// path into that option, and exec only consults PATH for a bare name - so the
// friendly message used to be on the one path a zde machine never takes.
func TestATierThatCannotStartNamesTheOption(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-tier")
	writeTiers(t, map[string][]string{TierProvider: {missing}})
	_, failure := askAll(t, askServer(t), TierProvider, "anything")
	for _, want := range []string{missing, "zde.ask.tiers.provider", "no such file"} {
		if !strings.Contains(failure, want) {
			t.Errorf("a tier that cannot start reported %q, which does not mention %q", failure, want)
		}
	}
}

// One answer at a time down one connection. An ask.text line carries no id, so
// two answers on one connection would be indistinguishable and the first end
// would end both.
func TestASecondAskOnOneConnectionIsRefused(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("slow")})
	c, err := DialPath(askServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodAskRun, nil, TierProvider, "the first question"); err != nil {
		t.Fatalf("the first ask: %v", err)
	}
	err = c.Call(MethodAskRun, nil, TierProvider, "the second question")
	if err == nil {
		t.Fatal("a second answer was started on a connection that was already carrying one")
	}
	if !strings.Contains(err.Error(), "still answering") {
		t.Errorf("the refusal is %q, and does not say why", err)
	}
}

// A connection that is written to and can say whether it was closed. Half of
// what it stands in for is a client that has stopped reading; the other half is
// a write that stops partway, which a deadline tripping mid-line produces on a
// real socket.
type brokenConn struct {
	mu     sync.Mutex
	short  bool
	closed bool
	wrote  int
}

func (b *brokenConn) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.short {
		// Half a line and no error at all, which is what a socket does when its
		// buffer fills and the deadline trips in the middle of the write.
		b.wrote += len(p) / 2
		return len(p) / 2, nil
	}
	return 0, errClosed
}

func (b *brokenConn) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *brokenConn) shut() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// A client that cannot be written to has to be closed rather than written at
// again. Two reasons, and the second is the one that was wrong: a client that
// has stopped reading needs an EOF to act on, because the answer it is waiting
// for has stopped coming and the end of it will fail the same way. And a write
// that stopped halfway through a line leaves a connection whose next bytes read
// as the tail of a message nobody can parse.
func TestASinkThatCannotTakeAWholeLineIsClosed(t *testing.T) {
	for _, c := range []struct {
		name string
		conn *brokenConn
	}{
		{"a write that fails", &brokenConn{}},
		{"a write that stops halfway", &brokenConn{short: true}},
	} {
		k := &sink{w: c.conn}
		if err := k.sendWithin(Event{Kind: EventAskText, Text: "half an answer"}, time.Second); err == nil {
			t.Errorf("%s: sendWithin said the line went out", c.name)
		}
		if !c.conn.shut() {
			t.Errorf("%s: the connection was left open for the next line", c.name)
		}
	}
}

// And the short write is reported as one rather than as success. Without the
// count, a tripped deadline mid-line is a write that "worked" and a stream that
// carries on into a connection carrying half a message.
func TestAShortWriteIsAFailure(t *testing.T) {
	conn := &brokenConn{short: true}
	k := &sink{w: conn}
	err := k.sendWithin(Event{Kind: EventAskText, Text: "half an answer"}, time.Second)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("a half-written line came back as %v", err)
	}
}

// A tier that fails says why in its own words. Without them a surface can
// report an exit status, which tells nobody whether the container has no
// credentials or the model name is wrong.
func TestAFailedTierReportsWhatItComplainedAbout(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("angry")})
	_, failure := askAll(t, askServer(t), TierProvider, "anything")
	if !strings.Contains(failure, "no credentials in this container") {
		t.Errorf("the tier failed with %q, and its own words are not in it", failure)
	}
}

// The answer goes to the connection that asked and to nobody else. This is the
// security claim the component is built on - a question asked on the local tier
// because it is nobody else's business must not arrive at every surface that
// happens to be subscribed - and it is one line away from not being true, since
// zded has a broadcast that reaches every listener.
func TestAnAnswerGoesOnlyToTheConnectionThatAsked(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("echo")})
	path := askServer(t)

	// A shell: subscribed to everything zded pushes, which is how the picker
	// arrives, and asking nothing itself.
	watcher, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	var ack string
	if err := watcher.Call(MethodEvents, &ack); err != nil {
		t.Fatalf("subscribing: %v", err)
	}

	text, failure := askAll(t, path, TierProvider, "the sort of question nobody wants repeated")
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if !strings.Contains(text, "nobody wants repeated") {
		t.Fatalf("the tier did not answer: %q", text)
	}

	// With a deadline rather than none: the answer is already over, so anything
	// broadcast would be sitting on this connection waiting to be read.
	if ev, err := watcher.NextEventBefore(time.Now().Add(250 * time.Millisecond)); err == nil {
		t.Errorf("a subscriber that asked nothing was sent a %q event carrying %q", ev.Kind, ev.Text)
	}
}

// The carry that holds back a character a read cut in half. Below the read
// buffer nothing exercises it, so this is the arithmetic on its own: what is
// held is only ever the start of a character that is not all here.
func TestPartialRuneHoldsBackTheEndOfACharacter(t *testing.T) {
	two := []byte("да")  // two bytes per character
	three := []byte("日") // three
	four := []byte("🙂")  // four
	for _, c := range []struct {
		name string
		in   []byte
		want int
	}{
		{"nothing at all", nil, 0},
		{"plain ascii", []byte("hello"), 0},
		{"whole characters", two, 0},
		{"one byte of two", two[:len(two)-1], 1},
		{"two bytes of three", three[:2], 2},
		{"one byte of three", three[:1], 1},
		{"three bytes of four", four[:3], 3},
		// A byte that starts no character at all waits too, and is flushed when
		// the answer ends: dropping it would be zde editing what a tier said.
		{"a byte that is not a character", []byte{'a', 0xff}, 1},
	} {
		if got := partialRune(c.in); got != c.want {
			t.Errorf("%s: held %d bytes back, want %d", c.name, got, c.want)
		}
	}
}

// And the same thing through a whole run: an answer in characters that do not
// fit in one byte, longer than the read buffer, arrives as it was written. A
// question asked in Russian is answered in Russian, and a replacement mark
// every four thousand bytes is what this looked like before the carry.
func TestAMultibyteAnswerSurvivesTheReadBuffer(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("cyrillic")})
	text, failure := askAll(t, askServer(t), TierProvider, "say it in russian")
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if want := strings.Repeat(cyrillicWord, cyrillicTimes); text != want {
		t.Errorf("the answer came back %d bytes and %d characters, want %d and %d",
			len(text), utf8.RuneCountInString(text), len(want), utf8.RuneCountInString(want))
	}
	if strings.ContainsRune(text, utf8.RuneError) {
		t.Errorf("the answer carries %d replacement marks", strings.Count(text, string(utf8.RuneError)))
	}
}

// zded answers every keybind, so an answer that takes twenty seconds may not be
// what the next key waits for - on the connection that asked (the shell
// acknowledges pickers on the one it asks on) or on any other (every keybind is
// a fresh zde).
func TestASlowAskDoesNotHoldUpTheNextKey(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("slow")})
	path := askServer(t)

	asking, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer asking.Close()
	if err := asking.Call(MethodAskRun, nil, TierProvider, "how long is a piece of string"); err != nil {
		t.Fatalf("ask.run: %v", err)
	}

	// The same connection: this is the shell, which asks and acknowledges on
	// one socket.
	start := time.Now()
	var st Status
	if err := asking.Call("status", &st); err != nil {
		t.Fatalf("the asking connection stopped answering: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a question on the asking connection waited %v for a running ask", took)
	}

	// And another connection, which is what a keybind is: a spawned zde. A
	// switch is the one worth measuring, since it is what Mod+Tab ends in.
	other, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start = time.Now()
	var focused []string
	if err := other.Call("desk.switch", &focused, "vshop"); err != nil {
		t.Fatalf("desk.switch during an ask: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a desk switch waited %v for a running ask", took)
	}
}

// No history by default (docs/vision.md, section 2) is a security property, and
// the way to have none is to write none. Nothing about a question or an answer
// may reach the disk: not the journal, not a cache, not a transcript.
func TestAskWritesNothingToDisk(t *testing.T) {
	home := writeTiers(t, map[string][]string{TierProvider: fakeTier("echo")})
	// Every directory a stray write would plausibly aim at, pointed inside the
	// tree this test watches - so a write anywhere near zde's usual places is
	// caught rather than landing in the real home directory unnoticed.
	state := filepath.Join(home, "state")
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	jrn, err := journal.Open(filepath.Join(state, "zde", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	s := New("test", jrn, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	// Before the temporary directory is moved, so that the socket is not one of
	// the things being watched: what is being watched is what an answer writes.
	path := serve(t, s)
	// And the temporary directory too, which is where anything writing a
	// scratch file goes without being told - and where the question turned up
	// when this test only watched the home directory.
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_RUNTIME_DIR", tmp)

	before := tree(t, home)
	text, failure := askAll(t, path, TierProvider, "the sort of question nobody wants written down")
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if !strings.Contains(text, "nobody wants written down") {
		t.Fatalf("the tier did not answer: %q", text)
	}
	after := tree(t, home)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("asking changed what is on disk:\nbefore %v\nafter  %v", before, after)
	}
}

// tree is every file under root and what is in it. Contents rather than names:
// an answer appended to the journal would leave the same set of files behind.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Directories and anything that is not a file to read: a socket bound
		// under here is not something that was written, and reading one is not
		// something that answers.
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The popup is a surface somebody else draws, so the verb's job is to tell them
// and to find out whether anything came of it. False here is what makes the CLI
// say how to ask from a terminal instead of pressing a key into silence.
func TestAskOneshotTellsAListenerAndSaysWhetherItDrew(t *testing.T) {
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)

	var shown bool
	if err := json.Unmarshal(s.Dispatch(Request{Method: "ask.oneshot"}).Ok, &shown); err != nil {
		t.Fatal(err)
	}
	if shown {
		t.Error("nothing is listening and the answer claims a window was drawn")
	}

	rec := &recorder{}
	s.listen(&sink{w: rec})
	resp := s.Dispatch(Request{Method: "ask.panel"})
	if resp.Error != "" {
		t.Fatalf("ask.panel: %s", resp.Error)
	}
	if err := json.Unmarshal(resp.Ok, &shown); err != nil {
		t.Fatal(err)
	}
	if shown {
		t.Error("the listener acknowledged nothing and the answer claims a window was drawn")
	}
	var got struct{ Event Event }
	line := strings.TrimSpace(rec.String())
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the event is not one line of json: %q", line)
	}
	// The panel and the popup are two kinds, so a shell that knows only one
	// ignores the other rather than drawing the wrong window.
	if got.Event.Kind != EventAskPanel {
		t.Errorf("kind = %q, want %q", got.Event.Kind, EventAskPanel)
	}
	if got.Event.Output != "DP-1" {
		t.Errorf("output = %q, want the screen being looked at", got.Event.Output)
	}
	if got.Event.Token == "" {
		t.Error("the event carries no token, so nothing can say it drew the window")
	}
}
