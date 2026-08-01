package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crispuscrew/zde/internal/keymap"
	"github.com/crispuscrew/zde/internal/link"
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
