package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crispuscrew/zde/internal/keymap"
	"github.com/crispuscrew/zde/internal/link"
	"github.com/crispuscrew/zde/internal/zded"
)

// quiet points os.Stderr at nothing for the length of a test: run() prints the
// whole usage there when it does not recognise a command, and that is the case
// under test here.
func quiet(t *testing.T) {
	t.Helper()
	old := os.Stderr
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = devnull
	t.Cleanup(func() {
		os.Stderr = old
		devnull.Close()
	})
}

// A process's arguments are readable by anybody with an account on the machine
// for as long as it lives (/proc/<pid>/cmdline), so there must be no spelling
// of this command that puts a wifi password there. If this regresses, the leak
// is a keystroke away and nothing about it looks wrong.
func TestAPasswordIsNeverACommandLineArgument(t *testing.T) {
	quiet(t)
	err := run([]string{"net", "connect", "vshop", "hunter2"})
	if err == nil {
		t.Fatal("zde net connect took a password on the command line")
	}
	// Refused for being a command that does not exist, and not by accident -
	// a form that tried to reach zded and failed because nothing was listening
	// would pass a test that only asked for an error.
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("zde net connect SSID SECRET got as far as %q", err)
	}
}

// The password is typed at a prompt, and the prompt must not put it back on
// the screen: what is on the screen is in the scrollback, in tmux's buffer,
// and in whatever is recording the terminal. If this regresses, the one
// command whose job is to handle a secret carefully is the one printing it.
func TestASecretIsNotPrintedBackByTheThingThatReadsIt(t *testing.T) {
	const secret = "correct-horse"

	in, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteString(secret + "\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	said, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}

	oldIn, oldErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = in, said
	got, readErr := readSecret("password for vshop: ")
	os.Stdin, os.Stderr = oldIn, oldErr
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got != secret {
		t.Fatalf("read %q, so this test is not reading the secret at all", got)
	}
	written, err := os.ReadFile(said.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "password for vshop") {
		t.Errorf("nothing asked for the password: %q", written)
	}
	if strings.Contains(string(written), secret) {
		t.Errorf("the prompt printed the password back: %q", written)
	}
}

// A machine with no NetworkManager gets one legible line rather than an error,
// because that is a fact about the machine and not a fault. If this regresses,
// Mod+Shift+c on every desktop that is not a laptop reads as a broken key -
// and the smoke test, which boots a VM with no NetworkManager, is looking for
// exactly these words.
func TestAMachineWithNoManagerSaysSoInOneLine(t *testing.T) {
	got := linkLine(link.Status{Kind: link.KindAbsent})
	if !strings.Contains(got, "no NetworkManager") {
		t.Errorf("linkLine(absent) = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("linkLine(absent) is more than one line: %q", got)
	}
	// And the other three say what they are, in the words the bar uses.
	for kind, want := range map[string]string{
		link.KindWired: "wired",
		link.KindNone:  "not connected",
		link.KindWifi:  "wifi vshop 71%",
	} {
		if got := linkLine(link.Status{Kind: kind, SSID: "vshop", Signal: 71}); got != want {
			t.Errorf("linkLine(%s) = %q, want %q", kind, got, want)
		}
	}
}

// The registry says which actions have a command written behind them, and the
// palette shows every other row as one that will do nothing. That claim is only
// worth making if it is true, and the only thing that decides it is this file's
// own dispatch: a verb `zde` does not know prints usage to a stderr no keypress
// has and exits, which is the silent key the whole mark exists to explain.
//
// So both directions. A row marked live that `zde` cannot answer is the palette
// lying, which is worse than the silent key; a row marked silent that `zde` can
// answer is a working action the palette tells people not to press.
//
// Nothing here does anything: the environment points at empty directories, so
// every verb that reaches zded fails to dial and every verb that reaches the
// apps table finds none. What is being read is which of the two answers came
// back, not whether it worked.
func TestLiveActionsAreTheOnesZdeKnows(t *testing.T) {
	quiet(t)
	empty := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", empty)
	t.Setenv("XDG_CONFIG_HOME", empty)
	t.Setenv("HOME", empty)

	// The keymap a machine gets, rendered as the file it gets: the actions that
	// carry an argument exist only as binds, and `app.launch-at editor` is one
	// of the rows this is most worth being right about.
	km, err := keymap.Load(filepath.Join("..", "..", "common", "keymap", "keymap.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	actions := keymap.Actions([]byte(keymap.EmitText(km)))
	if len(actions) < 20 {
		t.Fatalf("only %d actions to check, which is not the shipped keymap", len(actions))
	}
	checked := 0
	for _, a := range actions {
		if len(a.Spawn) == 0 || a.Spawn[0] != "zde" {
			continue // niri's own, or somebody else's program
		}
		// The doctor is the one live verb that does its work in this process
		// rather than over the socket, so running it here would go and ask
		// systemd and podman about a machine no test is about.
		//
		// Skipped by the argv and not by the action's name, which is the whole
		// point of the difference: keyed on the name, this exempted whatever
		// that row spawned, on the one row whose reason for existing is being
		// reachable only by name. Change the argv and it is checked like the
		// rest.
		if strings.Join(a.Spawn, " ") == "zde doctor" {
			continue
		}
		checked++
		err := run(a.Spawn[1:])
		unknown := err != nil && strings.Contains(err.Error(), "unknown command")
		switch {
		case a.Live && unknown:
			t.Errorf("%s is marked live and `zde %s` is a command nothing answers: the palette would offer a row that does nothing",
				a.Name, strings.Join(a.Spawn[1:], " "))
		case !a.Live && !unknown:
			t.Errorf("%s is marked silent and `zde %s` works: the palette tells people not to press a key that does something",
				a.Name, strings.Join(a.Spawn[1:], " "))
		}
	}
	if checked < 10 {
		t.Errorf("only %d actions spawn zde, which is not this keymap", checked)
	}
}

// Eight of the palette's own names have a space in them - `window.focus left`,
// `monitor.move-window right` - and the list prints the name in column one, so
// the obvious thing to do with a row is to type what it says. Taking only two
// arguments made that `zde: unknown command`, which blames the person for
// mistyping a name they copied.
func TestPaletteTakesANameWithSpacesInIt(t *testing.T) {
	quiet(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	err := run([]string{"palette", "window.focus", "left"})
	if err == nil {
		t.Fatal("there is no zded here, so this cannot have worked")
	}
	// It got as far as trying to reach the daemon, which is where every other
	// verb gets to here. Anything else means the name was never assembled.
	if strings.Contains(err.Error(), "unknown command") {
		t.Errorf("`zde palette window.focus left` got as far as %q", err)
	}
}

// fakeZded is a daemon only far enough along to say which request arrived and
// to answer it. The defect these tests cover is the CLI sending the wrong
// request, so a test that could not see the request would be a test of nothing
// - and the real dispatcher needs a compositor these tests are not about.
type fakeZded struct {
	mu   sync.Mutex
	got  []zded.Request
	says func(zded.Request) []string
}

// fakeDaemon puts one on the socket `zde` dials, and points the environment at
// it. says answers a request with the lines to write back, whole lines, in
// order: a reply, and for an ask, the events after it.
func fakeDaemon(t *testing.T, says func(zded.Request) []string) *fakeZded {
	t.Helper()
	// Short, and not t.TempDir: a unix address is capped near 108 bytes and a
	// test's own temp directory carries the test's name in it.
	dir, err := os.MkdirTemp("", "zde")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if err := os.Mkdir(filepath.Join(dir, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "zde", "zded.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeZded{says: says}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeZded) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		raw, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req zded.Request
		if err := json.Unmarshal(raw, &req); err != nil {
			return
		}
		f.mu.Lock()
		f.got = append(f.got, req)
		f.mu.Unlock()
		for _, line := range f.says(req) {
			if _, err := conn.Write([]byte(line + "\n")); err != nil {
				return
			}
		}
	}
}

// asked is every request that arrived, in order. Copied under the lock: the
// connection is served on a goroutine of its own and the test reads this after
// the command returns, which is not the same as after the goroutine has.
func (f *fakeZded) asked() []zded.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]zded.Request(nil), f.got...)
}

// tierAnswers is a whole answer to an ask.run: the reply, a piece of it, and
// the end. Every fake here gives one, including in the tests where an ask.run
// is the bug being looked for - the CLI waits for an event with no deadline of
// its own, so a regression that was left unanswered would time this file out
// rather than fail it, and a test that hangs says nothing about what broke.
func tierAnswers(text string) []string {
	said, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return []string{
		`{"ok":"asking"}`,
		`{"event":{"kind":"ask.text","text":` + string(said) + `}}`,
		`{"event":{"kind":"ask.text","done":true}}`,
	}
}

// onStdout runs it with stdout pointed at a file, and answers what landed
// there. fmt.Print reads os.Stdout when it prints, so swapping it is enough -
// and these are tests about where an answer arrives, which means where it
// arrives has to be readable.
func onStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = tmp
	runErr := run()
	os.Stdout = old
	said, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(said), runErr
}

// The word "panel" names a surface, and for a while it named nothing at all:
// `zde ask panel <question>` ignored it and ran a provider oneshot, printing an
// answer to the terminal. Harmless while nobody relied on it, which is exactly
// why it would still have been there when somebody did.
//
// If this regresses, a question meant for the window that keeps a conversation
// is answered once, on a terminal, with nowhere to ask the next one - and the
// only sign is that the panel never opened.
func TestAskPanelWithAQuestionOpensThePanelAndRunsNoTier(t *testing.T) {
	f := fakeDaemon(t, func(req zded.Request) []string {
		if req.Method == "ask.panel" {
			// A shell drew it. What happens when nothing does is the test
			// below.
			return []string{`{"ok":true}`}
		}
		return tierAnswers("Lima\n")
	})

	said, err := onStdout(t, func() error {
		return run([]string{"ask", "panel", "what", "is", "the", "capital", "of", "peru"})
	})
	if err != nil {
		t.Fatalf("zde ask panel: %v", err)
	}
	got := f.asked()
	if len(got) != 1 || got[0].Method != "ask.panel" {
		t.Fatalf("zde ask panel sent %+v, want one ask.panel: the verb names the surface", got)
	}
	if len(got[0].Args) != 1 || got[0].Args[0] != "what is the capital of peru" {
		t.Errorf("the question did not reach the panel whole: %q", got[0].Args)
	}
	// And nothing on the terminal: the answer arrives in the window, so a
	// terminal that printed one would mean the question was asked twice.
	if said != "" {
		t.Errorf("zde ask panel printed %q, and the answer belongs in the window", said)
	}
}

// The other verb, unchanged, because it is somebody's script: a question
// written after `oneshot` is answered on the terminal it was typed at, on the
// provider tier, streamed as it comes. This is the guarantee the panel change
// is not allowed to cost.
func TestAskOneshotWithAQuestionStillAnswersOnTheTerminal(t *testing.T) {
	f := fakeDaemon(t, func(req zded.Request) []string {
		if req.Method != zded.MethodAskRun {
			return []string{`{"error":"` + req.Method + ` is not how a oneshot asks"}`}
		}
		return tierAnswers("Lima\n")
	})

	said, err := onStdout(t, func() error {
		return run([]string{"ask", "oneshot", "what", "is", "the", "capital", "of", "peru"})
	})
	if err != nil {
		t.Fatalf("zde ask oneshot: %v", err)
	}
	if said != "Lima\n" {
		t.Errorf("the answer landed as %q, and a oneshot answers where it was typed", said)
	}
	got := f.asked()
	if len(got) != 1 || got[0].Method != zded.MethodAskRun {
		t.Fatalf("zde ask oneshot sent %+v, want one %s", got, zded.MethodAskRun)
	}
	want := []string{zded.TierProvider, "what is the capital of peru"}
	if strings.Join(got[0].Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args = %q, want %q", got[0].Args, want)
	}
}

// A tier is a program somebody else wrote, and what it says is forwarded to
// this terminal as it arrives. So an answer is the one thing this command
// prints that is written by another program while a person watches, and a
// terminal reads some of what a program can write as instructions rather than
// as text.
//
// The answer still has to arrive whole and in its own shape - paragraphs,
// indented code - which is why this is not the filter the notification path
// uses. Only what a terminal acts on goes.
func TestATiersAnswerCannotDriveTheTerminalItIsPrintedOn(t *testing.T) {
	fakeDaemon(t, func(req zded.Request) []string {
		return tierAnswers("Lima\x1b[2J\x1b]0;you have mail\x07\n\tprint(\"hi\")\n")
	})

	said, err := onStdout(t, func() error {
		return run([]string{"ask", "oneshot", "what", "is", "the", "capital", "of", "peru"})
	})
	if err != nil {
		t.Fatalf("zde ask oneshot: %v", err)
	}
	for _, bad := range []string{"\x1b", "\x07"} {
		if strings.Contains(said, bad) {
			t.Errorf("the answer printed %q, which still carries %q", said, bad)
		}
	}
	// The shape of the answer is the tier's: the newline it ended a line with,
	// and the tab that indents a line of code, are what it wrote.
	if !strings.Contains(said, "Lima") || !strings.Contains(said, "\n\tprint(\"hi\")\n") {
		t.Errorf("the answer printed %q, want what the tier said with only the instructions gone", said)
	}
}

// The queue promises one printable line an item, and until now it promised it
// only on the way in: `zde queue add` refuses a control character, and every
// notification is cleaned before it is queued, but the queue is replayed from a
// file of JSON lines and the replay asks only for an id and some text.
//
// A hand-written line is this user's own doing, so this is not a way in. It is
// the difference between a promise the code keeps and one it only makes - and
// this is the reader of the queue that is a terminal.
func TestTheQueuePrintsOneLineAnItemWhateverTheJournalHolds(t *testing.T) {
	fakeDaemon(t, func(req zded.Request) []string {
		if req.Method != "queue.list" {
			return []string{`{"error":"` + req.Method + ` is not what a queue listing asks"}`}
		}
		item, err := json.Marshal([]map[string]any{{
			"id":   4,
			"text": "call the bank\x1b[2J\n5\t.\t-\t-\tnothing is waiting",
			"from": "mail\tmail",
		}})
		if err != nil {
			t.Fatal(err)
		}
		return []string{`{"ok":` + string(item) + `}`}
	})

	said, err := onStdout(t, func() error { return run([]string{"queue"}) })
	if err != nil {
		t.Fatalf("zde queue: %v", err)
	}
	if lines := strings.Count(strings.TrimSuffix(said, "\n"), "\n"); lines != 0 {
		t.Errorf("one item printed %d lines:\n%q", lines+1, said)
	}
	if fields := strings.Count(said, "\t"); fields != 4 {
		t.Errorf("one item printed %d tabs, want the four between its five columns:\n%q", fields, said)
	}
	if strings.Contains(said, "\x1b") {
		t.Errorf("the queue printed %q, which still carries an escape", said)
	}
	if !strings.Contains(said, "call the bank") {
		t.Errorf("the queue printed %q, and the item is still what it says", said)
	}
}

// No shell means no panel, and a question that reached no panel was not asked.
// It has to say so: the one thing it must not do is quietly run the tier
// instead, because then the verb answers on a terminal or in a window depending
// on what is running, and nothing can be written against it.
//
// The same shape the rest of the CLI uses for a surface that did not appear,
// with the difference that there is no list to fall back to printing - so what
// is left to say is what did not happen and which verb does work here.
func TestAskPanelWithNoShellSaysTheQuestionWasNotAsked(t *testing.T) {
	f := fakeDaemon(t, func(req zded.Request) []string {
		if req.Method == "ask.panel" {
			// Nothing acknowledged the event, which is the answer a session
			// with no shell gets (internal/zded, askSurface).
			return []string{`{"ok":false}`}
		}
		return tierAnswers("Lima\n")
	})

	said, err := onStdout(t, func() error {
		return run([]string{"ask", "panel", "what", "is", "the", "capital", "of", "peru"})
	})
	if err == nil {
		t.Fatal("nothing drew the panel and the question was reported as asked")
	}
	if !strings.Contains(err.Error(), "not asked") {
		t.Errorf("the refusal does not say the question went nowhere: %q", err)
	}
	if !strings.Contains(err.Error(), "oneshot") {
		t.Errorf("the refusal does not name the verb that answers here: %q", err)
	}
	if said != "" {
		t.Errorf("it refused and printed %q as well", said)
	}
	for _, req := range f.asked() {
		if req.Method == zded.MethodAskRun {
			t.Error("no shell, so it ran the tier instead: the verb does one thing or the other by what is running")
		}
	}
}

// printed runs one command with os.Stdout pointed at a pipe, and answers with
// what it wrote. The power menu's whole no-shell bargain is what it prints, and
// a test that never reads that is a test of nothing but the argument parser.
func printed(t *testing.T, args []string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := run(args)
	os.Stdout = old
	w.Close()
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("zde %s: %v", strings.Join(args, " "), runErr)
	}
	return string(out)
}

// shortDir is a temporary directory whose path is short enough to put a unix
// socket in.
//
// t.TempDir spells the test's own name into the path, and a unix address is
// capped at 108 bytes including the terminator, so a test whose name is long
// enough will not bind on a machine whose temp root is long enough. That is not
// hypothetical: this test's name is thirty-nine characters, a CI runner's temp
// root was forty-one, and the socket under it came to exactly 108 - one over
// what fits. It passed on a developer machine, where the temp root is /tmp.
//
// So a socket path must never be built from t.TempDir, and the fix is not a
// shorter test name: a name chosen to fit a filesystem limit is a name that
// stops saying what the test proves. This is the fourth copy of this helper -
// cmd/zded, internal/zded (socketPath) and internal/niri have the same one, in
// their own packages because a test helper cannot cross one. A fifth is cheaper
// than a package that exists to hold six lines.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "zde")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// firstWord is the column something starts in, counted from zero.
func firstWord(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' {
			return i
		}
	}
	return 0
}

// The power menu prints its five rows when no shell is up, with the name in
// column one, so the obvious thing to do with a row is to type what it says.
// A CLI that only knew the bare verb would answer a name copied off the row
// with "unknown command", which blames the person - and the row it happens to
// is the one that ends the session.
//
// So the names here are read off the rows rather than written down again: a
// list spelled twice is a list that can disagree with itself, which is the one
// way this can break without anybody noticing.
//
// Nothing is performed. The daemon it prints from is pointed at a system bus
// that is not there, so there is no logind to ask and every row carries the
// reason instead - and the names are then typed at a runtime directory with no
// daemon in it, which is as far as a name needs to get to prove it was
// dispatched.
func TestPowerTakesTheNameOffTheRowItPrinted(t *testing.T) {
	quiet(t)
	// Before the daemon: power.Open reads this to find the system bus, and a
	// path nothing is listening on is a machine with no logind. Without it this
	// test would open the real one on the machine it is running on.
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(shortDir(t), "no-bus"))
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))
	socket, err := zded.DefaultSocket()
	if err != nil {
		t.Fatal(err)
	}
	srv := zded.New("test", nil, nil, nil)
	if err := srv.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go srv.Serve() //nolint:errcheck // it ends with the listener below
	t.Cleanup(func() { srv.Close() })

	var names []string
	// Where the description begins on the last row printed, and where the lines
	// under it begin. They have to be the same column: a continuation that
	// starts anywhere else reads as another choice, which on this list is a
	// choice that ends the session.
	desc, under := 0, 0
	for _, line := range strings.Split(printed(t, []string{"system", "power"}), "\n") {
		if line == "" {
			continue
		}
		// The rows start in column one and everything under them is indented,
		// which is the shape the whole readout is arranged around.
		if strings.HasPrefix(line, " ") {
			under = firstWord(line)
			continue
		}
		name := strings.Fields(line)[0]
		names = append(names, name)
		// Past the name, past the padding, past the one-character mark, and
		// past the space after it: worked out from the shape of the row rather
		// than from the format string, so that a change to either is caught.
		i := len(name)
		for i < len(line) && line[i] == ' ' {
			i++
		}
		i++
		desc = firstWord(line[i:]) + i
	}
	if len(names) != 5 {
		t.Fatalf("the menu printed %d rows, want the five: %v", len(names), names)
	}
	if under == 0 {
		t.Fatal("nothing was printed under any row, so there is no indent to check")
	}
	if under != desc {
		t.Errorf("what a row costs starts at column %d and the description at column %d", under+1, desc+1)
	}

	// Somewhere with no daemon, so that typing one of those names goes no
	// further than the dial.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	for _, name := range names {
		err := run([]string{"system", "power", name})
		if err == nil {
			t.Fatalf("there is no zded here, so `zde system power %s` cannot have worked", name)
		}
		// It got as far as trying to reach the daemon, which is where every
		// verb gets to here. Anything else means the form was never dispatched.
		if strings.Contains(err.Error(), "unknown command") {
			t.Errorf("`zde system power %s` got as far as %q, and that name is on a row it printed", name, err)
		}
	}
}

// openPty is a terminal to type a password into, since that is the only place
// echo means anything: a pipe echoes nothing whatever the code does, which is
// why the test above cannot see this and this one exists.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty to test a password prompt on: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return m, s
}

func echoing(t *testing.T, f *os.File) bool {
	t.Helper()
	term, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	return term.Lflag&unix.ECHO != 0
}

// A password typed at a terminal must not be on the terminal: what is on the
// screen is in the scrollback, in tmux's buffer, and in whatever is recording
// the session. And the terminal has to be handed back the way it was found -
// a shell left echoless is one somebody has to know `stty sane` to escape.
//
// If this regresses, either every wifi password is typed in the clear in front
// of whoever is in the room, or the terminal it was typed at stops showing what
// anybody types into it afterwards.
func TestThePasswordPromptTurnsEchoOffAndPutsItBack(t *testing.T) {
	master, slave := openPty(t)
	if !echoing(t, slave) {
		t.Fatal("a fresh pty is not echoing, so this test cannot see the difference")
	}

	oldIn, oldErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = slave, slave
	defer func() { os.Stdin, os.Stderr = oldIn, oldErr }()

	got, err := "", error(nil)
	done := make(chan struct{})
	go func() {
		got, err = readSecret("password for vshop: ")
		close(done)
	}()

	// The prompt arriving is the sync point: it is written before the read, so
	// once it is on the terminal the terminal is in the state the read set up.
	seen := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(master).ReadString(':')
		seen <- line
	}()
	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt never appeared on the terminal")
	}
	if echoing(t, slave) {
		t.Error("the terminal is echoing while a password is being typed into it")
	}

	if _, err := master.WriteString("hunter2\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt never finished reading")
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Fatalf("read %q from the terminal", got)
	}
	if !echoing(t, slave) {
		t.Error("the terminal was left echoless after the prompt")
	}
}
