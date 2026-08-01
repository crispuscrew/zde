package zded

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func fakeTier(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestFakeTier$", "--", fakeTierMark, mode}
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
	}
	os.Exit(code)
}

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
	path := serve(t, s)

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
		if d.IsDir() {
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
