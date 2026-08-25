package capture

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAShotLandsInAFileZdeNamed(t *testing.T) {
	dir := t.TempDir()
	fake := newFake(wrote)
	path, err := Shot(fake, Full, dir, noon)
	if err != nil {
		t.Fatalf("Shot: %v", err)
	}
	if want := filepath.Join(dir, noonName); path != want {
		t.Errorf("Shot = %q, want %q", path, want)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, png) {
		t.Errorf("the file holds %q, %v", got, err)
	}
	asked := fake.all()
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "screen "+filepath.Join(dir, ".zde-capture-")) || !strings.HasSuffix(asked[0], ".pending") {
		t.Errorf("niri was asked %v, want one screen shot into a hidden staging file", asked)
	}
}

// niri does not create the parent of an explicit screenshot path.
func TestTheDirectoryIsMadeBecauseNiriDoesNotMakeOne(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Pictures", "Screenshots")
	if _, err := Shot(newFake(wrote), Full, dir, noon); err != nil {
		t.Fatalf("Shot into a directory that is not there: %v", err)
	}
	if got := namesIn(t, dir); len(got) != 1 {
		t.Errorf("the directory holds %v", got)
	}
}

func TestACaptureIsNotReadableByTheRestOfTheMachine(t *testing.T) {
	path, err := Shot(newFake(wrote), Full, t.TempDir(), noon)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the capture is %04o, want 0600", perm)
	}
}

func TestASecondShotInTheSameSecondDoesNotOverwriteTheFirst(t *testing.T) {
	dir := t.TempDir()
	first, err := Shot(newFake(wrote), Full, dir, noon)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Shot(newFake(func(path string) (string, bool) {
		os.WriteFile(path, append([]byte("the second one"), pngEnd...), 0o600) //nolint:errcheck // read below
		return path, true
	}), Full, dir, noon)
	if err != nil {
		t.Fatalf("the second shot of the second: %v", err)
	}
	if first == second {
		t.Fatalf("both shots got %s", first)
	}
	if want := filepath.Join(dir, "zde-20260820T120000Z-2.png"); second != want {
		t.Errorf("the second capture is %q, want %q", second, want)
	}
	if got, err := os.ReadFile(first); err != nil || !bytes.Equal(got, png) {
		t.Errorf("the first capture reads %q, %v: the second one landed on it", got, err)
	}
}

func TestASymlinkAtTheNameIsNotWrittenThrough(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "theirs")
	if err := os.WriteFile(elsewhere, []byte("not a capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, noonName)); err != nil {
		t.Fatal(err)
	}
	path, err := Shot(newFake(wrote), Full, dir, noon)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) == noonName {
		t.Errorf("the capture took the planted name %s", noonName)
	}
	if got, _ := os.ReadFile(elsewhere); !bytes.Equal(got, []byte("not a capture")) {
		t.Errorf("the file the symlink pointed at now holds %q", got)
	}
}

func TestADirectoryThisSessionCannotWriteIsARefusal(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write into a directory with no write bit")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) }) //nolint:errcheck // t.TempDir cleans up
	fake := newFake(wrote)
	_, err := Shot(fake, Full, dir, noon)
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("capture into an unwritable directory = %v", err)
	}
	if asked := fake.all(); len(asked) != 0 {
		t.Errorf("niri was asked for a shot with nowhere to put it: %v", asked)
	}
}
