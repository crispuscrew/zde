package capture

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/apps"
)

// The ordinary send-to: a name this machine has an argv for, and the file on
// the end of it. The names come from zde.apps, which is the same seam `zde app
// launch` resolves through, so "viewer" means one thing on this machine and not
// two.
func TestSendToPutsThePathOnWhatTheMachineCallsThat(t *testing.T) {
	all := apps.Apps{"viewer": {"imv", "-f"}}

	argv, err := Argv(all, "viewer", "/home/you/Pictures/Screenshots/zde-x.png")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"imv", "-f", "/home/you/Pictures/Screenshots/zde-x.png"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("Argv = %v, want %v", argv, want)
	}
	// And the configured argv is not the one that got a path appended to it: a
	// map handed back with an extra element in it sends the next launch a file.
	if got := all["viewer"]; len(got) != 2 {
		t.Errorf("zde.apps now holds %v", got)
	}
}

// The boundary this verb actually meets. A zinc app sees the mounts its YAML
// declares and nothing else of this filesystem, and zinc 0.10.1 has no generic
// handoff: `zcr run -v` can mount a file while creating a container but takes no
// app argument, there is no `zcr exec`, and a mount cannot be added later. So a
// path appended to the runner is rejected rather than handed to the app - and
// the honest answer is the refusal that says which channel does cross, not a
// launch that comes back "unexpected argument" after the keypress is spent.
func TestSendToRefusesAContainerItCannotReach(t *testing.T) {
	for _, runner := range []string{"zcr", "/nix/store/example-zinc/bin/zcr"} {
		all := apps.Apps{"discord": {runner, "run", "discord", "--exec"}}

		argv, err := Argv(all, "discord", "/home/you/Pictures/Screenshots/zde-x.png")
		if err == nil {
			t.Fatalf("%s: a capture was handed to a container as %v", runner, argv)
		}
		if !strings.Contains(err.Error(), Clipboard) {
			t.Errorf("%s: the refusal does not name the channel that does cross: %v", runner, err)
		}
	}
}

// A target this machine has never heard of. The error is apps' own, which is
// the one that lists what there is - the alternative is somebody guessing
// whether they typed the name wrong or the machine has none.
func TestSendToSaysWhatThereIsWhenTheTargetIsNotThere(t *testing.T) {
	all := apps.Apps{"viewer": {"imv"}, "terminal": {"foot"}}

	_, err := Argv(all, "discord", "/tmp/x.png")
	if err == nil {
		t.Fatal("a target nothing is configured for resolved to something")
	}
	if !strings.Contains(err.Error(), "viewer") || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("the refusal does not list what this machine can send to: %v", err)
	}
}

// The clipboard reads the file, and everything zde reads goes through
// plainfile: a FIFO at that path parks the open in the kernel waiting for a
// writer that never comes. This path can arrive on a command line, so it is
// exactly the shape that check exists for.
func TestTheClipboardWillNotOpenSomethingThatIsNotAFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "zde-20260820T120000Z.png")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFO on this filesystem: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- Clip(fifo) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a named pipe was read onto the clipboard")
		}
		if !strings.Contains(err.Error(), "named pipe") {
			t.Errorf("the refusal does not say what it found: %v", err)
		}
	case <-timeout():
		t.Fatal("Clip is parked in the kernel on a FIFO nobody is writing to")
	}
}

func TestTheClipboardSaysSoWhenThereIsNoFile(t *testing.T) {
	err := Clip(filepath.Join(t.TempDir(), "gone.png"))
	if err == nil {
		t.Fatal("a capture that is not there went onto the clipboard")
	}
	if !os.IsNotExist(err) {
		t.Errorf("got %v, want the file not being there", err)
	}
}

// timeout is a wait a test can lose rather than hang on. The one thing a
// blocked open cannot be told apart from is a slow one, so the difference has
// to be a clock.
func timeout() <-chan time.Time { return time.After(5 * time.Second) }
