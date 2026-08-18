package plainfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// answerWithin runs f and fails the test if it has not returned in time.
//
// Every test below that involves a FIFO needs this, and needs it rather than a
// plain call: the failure being tested for is a call that never returns, so a
// regression without a deadline is not a red test, it is a CI job that sits
// there until something else kills it. The goroutine is leaked on purpose when
// that happens - it is blocked in the kernel and there is nothing to unblock it
// with, and the test binary is about to be over anyway.
func answerWithin(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("no answer in %v, which is the hang this package exists to prevent", d)
	}
}

// A FIFO is refused, and refused promptly.
//
// This is the one that matters. A plain os.Open here waits in the kernel for a
// writer that is never coming, and zded did that at startup with the whole of
// the daemon still ahead of it.
func TestAFifoIsRefusedRatherThanWaitedOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	answerWithin(t, 5*time.Second, func() {
		var f *os.File
		f, err = Open(path)
		if f != nil {
			f.Close()
		}
	})
	if err == nil {
		t.Fatal("opened a FIFO as though it were a file")
	}
	if !strings.Contains(err.Error(), "named pipe") {
		t.Errorf("error is %q, and it should say what is actually at that path", err)
	}
}

// The same for Read, which is the shape most callers use.
func TestReadOfAFifoIsRefusedRatherThanWaitedOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	answerWithin(t, 5*time.Second, func() { _, err = Read(path, 1<<20) })
	if err == nil {
		t.Fatal("read a FIFO as though it were a file")
	}
}

// And a FIFO that has a writer, which is the case a bare O_NONBLOCK open would
// let through: the open succeeds, and then the daemon reads whatever is being
// fed to it for as long as somebody keeps feeding it.
func TestAFifoWithAWriterIsStillRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	// Held open for the length of the test so the read side has somebody on it.
	// O_RDWR rather than O_WRONLY: opening the write end of a FIFO nobody is
	// reading is ENXIO under O_NONBLOCK and a wait without it, which is the
	// deadlock this whole package is about - and O_RDWR is the one open that
	// neither waits nor fails.
	w, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var openErr error
	answerWithin(t, 5*time.Second, func() {
		var f *os.File
		f, openErr = Open(path)
		if f != nil {
			f.Close()
		}
	})
	if openErr == nil {
		t.Fatal("opened a FIFO with a writer on it: the kind of file is the check, not whether the open blocked")
	}
}

// A directory, a device and a socket go the same way.
func TestOnlyAPlainFileIsOpened(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err == nil {
		t.Error("opened a directory")
	}
	if _, err := Open("/dev/zero"); err == nil {
		t.Error("opened a device")
	}
}

// The ordinary case still works, which is the half of this that a defence can
// quietly break.
func TestAPlainFileIsRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps.json")
	if err := os.WriteFile(path, []byte(`{"terminal":["foot"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := Read(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"terminal":["foot"]}` {
		t.Errorf("read %q", data)
	}
}

// A symlink to a plain file is followed by Open, because that is how every
// supported install writes zde's config: home-manager links it into the nix
// store. A defence that refused this would refuse to read apps.json on every
// real machine and pass every test that only used files it had written itself.
func TestASymlinkToAPlainFileIsFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "store-apps.json")
	if err := os.WriteFile(target, []byte("{}"), 0o444); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "apps.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link, 1<<20); err != nil {
		t.Fatalf("a config symlinked into the store is the ordinary case, and it was refused: %v", err)
	}
}

// A symlink to a FIFO is not, which is what makes the sentence above safe to
// write: what is followed is the link, what is checked is where it lands.
func TestASymlinkToAFifoIsRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(target, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "apps.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var err error
	answerWithin(t, 5*time.Second, func() { _, err = Read(link, 1<<20) })
	if err == nil {
		t.Fatal("followed a link into a FIFO")
	}
}

// OpenNoFollow refuses the link itself, for the files zde writes.
func TestOpenNoFollowRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "somebody-elses.jsonl")
	if err := os.WriteFile(target, []byte("theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "journal.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenNoFollow(link); !errors.Is(err, syscall.ELOOP) {
		t.Errorf("error is %v, want ELOOP: a symlink at a file zde writes is refused, not followed", err)
	}
	if _, err := OpenNoFollow(target); err != nil {
		t.Errorf("the file itself was refused: %v", err)
	}
}

// The ceiling, and the byte that trips it.
func TestReadRefusesWhatIsPastTheCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(path, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 100); err != nil {
		t.Errorf("a file exactly at the ceiling was refused: %v", err)
	}
	if _, err := Read(path, 99); err == nil {
		t.Error("a file past the ceiling came back whole")
	}
}

// Whose file it is, decided rather than guessed - and the decision is the thing
// under test, because a unit test cannot make a file belonging to a third
// account or to root (internal/journal makes the same argument about
// chmodRefused).
//
// The cases that matter are the last three. "root owns it" was the whole rule
// once, and the reason written beside it was the nix store - so a symlink at
// apps.json pointing at /etc/os-release was read as an app map, on the strength
// of a sentence about a store that file has never been in. What separates the
// two is the property the store has and /etc does not: the nix daemon
// canonicalises everything it puts there to root-owned with no write bit set at
// all.
func TestOnlyOursAndTheStoresAreTrusted(t *testing.T) {
	for _, c := range []struct {
		name  string
		owner int
		us    int
		mode  os.FileMode
		want  bool
	}{
		{"ours", 1000, 1000, 0o600, true},
		{"ours, however we left its mode", 1000, 1000, 0o666, true},
		{"ours when we are root", 0, 0, 0o644, true},
		{"somebody else's", 1001, 1000, 0o444, false},
		{"a store path, which is where home-manager puts every config it writes", 0, 1000, 0o444, true},
		{"a store path that is executable", 0, 1000, 0o555, true},
		{"root's and writable by root, which is /etc/os-release", 0, 1000, 0o644, false},
		{"root's and writable by the world", 0, 1000, 0o666, false},
	} {
		if got := trusted(c.owner, c.us, c.mode); got != c.want {
			t.Errorf("%s: trusted(%d, %d, %04o) = %v, want %v", c.name, c.owner, c.us, c.mode.Perm(), got, c.want)
		}
	}
}

// And the same decision reached through a real open, so that the mode being
// asked for is the one on the descriptor rather than one nobody reads.
//
// Our own file either way here - a test cannot make root's - so what this pins
// is the half a narrowing could break by accident: a config of ours is read
// whatever mode it has, because the mode only decides anything for a file
// somebody else owns.
func TestOurOwnFileIsReadWhateverItsMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o400, 0o444, 0o600, 0o644, 0o666} {
		path := filepath.Join(t.TempDir(), "apps.json")
		if err := os.WriteFile(path, []byte("{}"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path, 1<<20); err != nil {
			t.Errorf("our own %04o file was refused: %v", mode, err)
		}
	}
}
