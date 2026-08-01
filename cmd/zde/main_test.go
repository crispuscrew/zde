package main

import (
	"io"
	"os"
	"strings"
	"testing"

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
