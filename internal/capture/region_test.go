package capture

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tools puts a fake slurp and a fake grim on PATH and nothing else, so that a
// machine which has the real ones is not the machine being tested and a machine
// which has neither can still run this.
//
// Each is a shell script, because what is being checked is the argv that
// reaches it and what it does with the path - not anything a Go fake could
// stand in for, since the whole point is that these are other programs.
func tools(t *testing.T, slurp, grim string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{Selector: slurp, Grabber: grim} {
		if body == "" {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A log both fakes can write what they were asked into.
	t.Setenv("ZDE_TEST_LOG", filepath.Join(dir, "asked"))
	t.Setenv("PATH", dir)
}

func asked(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("ZDE_TEST_LOG"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The ordinary region: slurp says which rectangle and grim writes what is
// inside it. The rectangle has to be the one slurp chose - a grab of the whole
// screen under a name that says region would be silently wrong.
func TestARegionIsTheRectangleTheSelectorChose(t *testing.T) {
	tools(t,
		`echo "$@" >> "$ZDE_TEST_LOG"; echo "100,200 640x480"`,
		`echo "$@" >> "$ZDE_TEST_LOG"; `+writePNG)
	dir := t.TempDir()
	path, err := regionShot(context.Background(), dir, noonNow)
	if err != nil {
		t.Fatalf("RegionShot: %v", err)
	}
	if want := filepath.Join(dir, noonName); path != want {
		t.Errorf("RegionShot = %q, want %q", path, want)
	}
	if !completePNG(path) {
		t.Errorf("the capture is not a complete PNG")
	}
	log := asked(t)
	if !strings.Contains(log, "-g 100,200 640x480") {
		t.Errorf("the grab was asked for %q, want the rectangle the selector chose", log)
	}
	if !strings.Contains(log, "-f "+geometry) {
		t.Errorf("the selector was not asked for the format the grab reads: %q", log)
	}
}

// A capture is a picture of whatever was on the screen, whichever path took it.
func TestARegionIsNotReadableByTheRestOfTheMachine(t *testing.T) {
	tools(t, `echo "0,0 10x10"`, writePNG)
	dir := t.TempDir()

	path, err := regionShot(context.Background(), dir, noonNow)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("the region capture is %04o, want 0600", perm)
	}
}

// Escape, or a right-click: slurp exits non-zero and says so. Nothing was
// captured, so nothing is claimed - a nought-byte file under a name that reads
// as a capture is what the next send-to would pick up.
func TestARegionNobodyChoseLeavesNothingBehind(t *testing.T) {
	tools(t,
		`echo "selection cancelled" >&2; exit 1`,
		`echo "$@" >> "$ZDE_TEST_LOG"; `+writePNG)
	dir := t.TempDir()

	_, err := regionShot(context.Background(), dir, noonNow)
	if err == nil {
		t.Fatal("a region nobody chose reported success")
	}
	if !strings.Contains(err.Error(), "selection cancelled") {
		t.Errorf("the selector's own words did not travel: %v", err)
	}
	if got := namesIn(t, dir); len(got) != 0 {
		t.Errorf("the cancelled capture left %v behind", got)
	}
	if log := asked(t); log != "" {
		t.Errorf("the grab ran anyway: %q", log)
	}
}

// The grab said it worked and wrote nothing. Taken at its word this is an empty
// file under a name that reads as a capture, which is the failure this whole
// package exists to stop.
func TestAGrabThatWroteNothingIsARefusal(t *testing.T) {
	tools(t, `echo "0,0 10x10"`, `exit 0`)
	dir := t.TempDir()

	_, err := regionShot(context.Background(), dir, noonNow)
	if err == nil {
		t.Fatal("a grab that wrote nothing reported a capture")
	}
	if got := namesIn(t, dir); len(got) != 0 {
		t.Errorf("it left %v behind", got)
	}
}

// And a grab that failed out loud carries what it said, because the reason is
// the fixable half - a rectangle off the edge of every output, a compositor
// that refused the protocol.
func TestAGrabThatFailedCarriesItsOwnWords(t *testing.T) {
	tools(t, `echo "0,0 10x10"`, `echo "failed to find output" >&2; exit 1`)
	dir := t.TempDir()

	_, err := regionShot(context.Background(), dir, noonNow)
	if err == nil {
		t.Fatal("a grab that failed reported a capture")
	}
	if !strings.Contains(err.Error(), "failed to find output") {
		t.Errorf("the grabber's own words did not travel: %v", err)
	}
	if got := namesIn(t, dir); len(got) != 0 {
		t.Errorf("it left %v behind", got)
	}
}
