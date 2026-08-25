package capture

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type definiteRefusal string

func (failure definiteRefusal) Error() string { return string(failure) }

func (definiteRefusal) Refused() bool { return true }

func TestNiriSayingItWroteNothingIsARefusal(t *testing.T) {
	quick(t)
	dir := t.TempDir()
	_, err := Shot(newFake(refusedShot), Full, dir, noon)
	if err == nil || !strings.Contains(err.Error(), noonName) {
		t.Fatalf("failed save = %v", err)
	}
	if got := captureNamesIn(t, dir); len(got) != 0 {
		t.Errorf("the failed capture published %v", got)
	}
}

func TestAnotherScreenshotIsNotThisOnesAnswer(t *testing.T) {
	dir := t.TempDir()
	fake := newFake(nil)
	fake.do = func(path string) (string, bool) {
		go func() {
			fake.events <- ""
			fake.events <- "/home/somebody/Pictures/Screenshots/theirs.png"
			time.Sleep(20 * time.Millisecond)
			os.WriteFile(path, png, 0o600) //nolint:errcheck // read below
			fake.events <- path
		}()
		return "", false
	}
	path, err := Shot(fake, Full, dir, noon)
	if err != nil {
		t.Fatalf("a stranger's screenshot ended this one's wait: %v", err)
	}
	if filepath.Base(path) != noonName {
		t.Errorf("Shot = %q", path)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, png) {
		t.Errorf("the capture holds %q, %v", got, err)
	}
}

func TestEachKindAsksItsOwnAction(t *testing.T) {
	for _, test := range []struct {
		kind Kind
		want string
	}{{Window, "window"}, {Full, "screen"}} {
		dir := t.TempDir()
		fake := newFake(wrote)
		if _, err := Shot(fake, test.kind, dir, noon); err != nil {
			t.Fatalf("%v: %v", test.want, err)
		}
		asked := fake.all()
		if len(asked) != 1 || !strings.HasPrefix(asked[0], test.want+" "+filepath.Join(dir, ".zde-capture-")) || !strings.HasSuffix(asked[0], ".pending") {
			t.Errorf("%v asked %v, want one shot into a hidden staging file", test.want, asked)
		}
	}
}

func TestAnActionNiriRefusedLeavesNoName(t *testing.T) {
	dir := t.TempDir()
	fake := newFake(wrote)
	fake.actionErr = definiteRefusal("error parsing request")
	if _, err := Shot(fake, Full, dir, noon); err == nil {
		t.Fatal("a refused action reported a capture")
	}
	if got := namesIn(t, dir); len(got) != 0 {
		t.Errorf("the refused action left %v", got)
	}
}

// A timeout keeps the private path so a late asynchronous open cannot recreate
// a public capture using the session umask.
func TestATimedOutWriterCannotPublishALateCapture(t *testing.T) {
	quick(t)
	dir := t.TempDir()
	done := make(chan string, 1)
	fake := newFake(func(path string) (string, bool) {
		go func() {
			time.Sleep(promptWait + 20*time.Millisecond)
			os.WriteFile(path, png, 0o666) //nolint:errcheck // inspected below
			done <- path
		}()
		return "", false
	})
	if _, err := Shot(fake, Full, dir, noon); err == nil {
		t.Fatal("an unconfirmed capture reported success")
	}
	pending := <-done
	if got := captureNamesIn(t, dir); len(got) != 0 {
		t.Errorf("the late writer published %v", got)
	}
	info, err := os.Stat(pending)
	if err != nil {
		t.Fatalf("the private writer path is gone: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the late writer path is %04o, want 0600", perm)
	}
	if _, err := Latest(dir); err == nil {
		t.Fatal("send-to found the hidden, unconfirmed capture")
	}
}

func TestNoStreamMeansNoShot(t *testing.T) {
	dir := t.TempDir()
	fake := newFake(wrote)
	fake.subscribeErr = errors.New("no")
	if _, err := Shot(fake, Full, dir, noon); err == nil {
		t.Fatal("a compositor that cannot say what it did reported a capture")
	}
	if asked := fake.all(); len(asked) != 0 {
		t.Errorf("it asked for the shot anyway: %v", asked)
	}
	if got := namesIn(t, dir); len(got) != 0 {
		t.Errorf("it claimed %v anyway", got)
	}
}
