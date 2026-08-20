package pass

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The pepper is the one secret pass keeps at rest, so every way of getting it
// wrong has to be a refusal with a reason rather than a password derived from
// something else. Each row here is a mutation: delete the check and this is the
// test that says which one went.
func TestEveryReasonZdeWillNotUseAPepper(t *testing.T) {
	for _, c := range []struct {
		what  string
		mode  os.FileMode
		owner int
		size  int64
		says  string
	}{
		{"a pepper another account owns", 0o600, 4242, PepperLen, "another account"},
		{"a pepper the group can read", 0o640, os.Getuid(), PepperLen, "mode 0640"},
		{"a pepper anybody can read", 0o604, os.Getuid(), PepperLen, "mode 0604"},
		{"a pepper anybody can write", 0o602, os.Getuid(), PepperLen, "mode 0602"},
		{"a truncated pepper", 0o600, os.Getuid(), 16, "16 bytes"},
		{"a pepper somebody appended to", 0o600, os.Getuid(), 64, "64 bytes"},
		{"a directory", os.ModeDir | 0o700, os.Getuid(), PepperLen, "not a plain file"},
		{"a named pipe", os.ModeNamedPipe | 0o600, os.Getuid(), PepperLen, "not a plain file"},
	} {
		err := refuse("/x/pepper", c.mode, c.owner, os.Getuid(), c.size)
		if err == nil {
			t.Errorf("%s was accepted", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s was refused with %q, which does not say %q", c.what, err, c.says)
		}
	}
	if err := refuse("/x/pepper", 0o600, os.Getuid(), os.Getuid(), PepperLen); err != nil {
		t.Errorf("the pepper zde writes was refused: %v", err)
	}
}

// A missing pepper is the ordinary case on a machine that has never run this,
// and the answer has to be the command that fixes it - not an ENOENT.
func TestAMissingPepperSaysHowToMakeOne(t *testing.T) {
	_, err := ReadPepper(filepath.Join(t.TempDir(), "pepper"))
	if err == nil {
		t.Fatal("a pepper that is not there was read")
	}
	if !strings.Contains(err.Error(), "zde pass init") {
		t.Errorf("a missing pepper answered %q, which does not say how to make one", err)
	}
}

// A FIFO where a file is expected is what internal/plainfile exists for: an
// open without O_NONBLOCK blocks in the kernel until a writer arrives, which is
// never, and this is a command with a person waiting at a keyboard.
func TestAPipeAtThePepperIsAnswerAndNotAWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pepper")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("no fifo on this filesystem: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadPepper(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a named pipe was read as a pepper")
		}
		if !strings.Contains(err.Error(), "not a plain file") {
			t.Errorf("a named pipe answered %q", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reading a named pipe as the pepper blocked, which is a pass that never answers")
	}
}

// The pepper is a file zde writes, so a symlink at the name is a redirection to
// somebody else's file and not a setup anybody has.
func TestASymlinkAtThePepperIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(real, make([]byte, PepperLen), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pepper")
	if err := os.Symlink(real, path); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if _, err := ReadPepper(path); err == nil {
		t.Fatal("a symlink at the pepper was followed")
	}
}

// What init writes, and what it does the second time. A pepper written over is
// every password on the machine changed at once, so O_EXCL is the whole
// mechanism: no prompt, because a prompt is a thing people answer yes to.
func TestInitWritesOnePepperAndNeverASecond(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pass")
	path := filepath.Join(dir, "pepper")
	if err := CreatePepper(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("the pepper is mode %04o and has to be 0600", fi.Mode().Perm())
	}
	if fi.Size() != PepperLen {
		t.Errorf("the pepper is %d bytes and has to be %d", fi.Size(), PepperLen)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("the directory holding the pepper is mode %04o and has to be 0700", di.Mode().Perm())
	}
	first, err := ReadPepper(path)
	if err != nil {
		t.Fatal(err)
	}
	err = CreatePepper(path)
	if err == nil {
		t.Fatal("a second pepper was written over the first, so every password on that machine just changed")
	}
	if !strings.Contains(err.Error(), "every password") {
		t.Errorf("writing over a pepper was refused with %q, which does not say what it would have cost", err)
	}
	again, err := ReadPepper(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(again) {
		t.Error("the pepper changed under a refusal")
	}
	// And it is not a constant. Nothing else in this file would notice a
	// CreatePepper that wrote 32 zeroes.
	if string(first) == string(make([]byte, PepperLen)) {
		t.Error("the pepper is 32 zero bytes")
	}
	other := filepath.Join(dir, "second")
	if err := CreatePepper(other); err != nil {
		t.Fatal(err)
	}
	twice, err := ReadPepper(other)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(twice) {
		t.Error("two peppers came out the same, so it is not random")
	}
}

// Dir is state and not config, and the reason is in the comment there: a pepper
// under ~/.config is a pepper somebody generates from their nix config into a
// world-readable store path.
func TestThePepperLivesInStateAndNotInConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/somewhere/state")
	if got, want := PepperPath(), "/somewhere/state/zde/pass/pepper"; got != want {
		t.Errorf("the pepper is at %s and should be at %s", got, want)
	}
	if got, want := StorePath(), "/somewhere/state/zde/pass/sites.json"; got != want {
		t.Errorf("the store is at %s and should be at %s", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/somebody")
	if got, want := PepperPath(), "/home/somebody/.local/state/zde/pass/pepper"; got != want {
		t.Errorf("the pepper is at %s and should be at %s", got, want)
	}
}
