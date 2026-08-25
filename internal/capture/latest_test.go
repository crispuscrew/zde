package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLatestIsTheNewestZdeWrote(t *testing.T) {
	dir := t.TempDir()
	plant(t, dir, noonName, noon)
	plant(t, dir, "zde-20260820T120000Z-2.png", noon.Add(time.Second))
	plant(t, dir, "Screenshot from 2026-08-20 12-00-30.png", noon.Add(time.Minute))
	plant(t, dir, "holiday.png", noon.Add(2*time.Minute))
	got, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "zde-20260820T120000Z-2.png"); got != want {
		t.Errorf("Latest = %q, want %q", got, want)
	}
}

func TestLatestUsesTheNumericSequenceWhenTimesTie(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		noonName,
		"zde-20260820T120000Z-9.png",
		"zde-20260820T120000Z-10.png",
	} {
		plant(t, dir, name, noon)
	}
	got, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "zde-20260820T120000Z-10.png"); got != want {
		t.Errorf("Latest = %q, want %q", got, want)
	}
}

func TestLatestSkipsAnIncompleteCapture(t *testing.T) {
	dir := t.TempDir()
	plant(t, dir, noonName, noon)
	partial := filepath.Join(dir, "zde-20260820T120001Z.png")
	if err := os.WriteFile(partial, []byte("part of a PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(partial, noon.Add(time.Second), noon.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, noonName); got != want {
		t.Errorf("Latest = %q, want complete %q", got, want)
	}
}

func TestLatestUsesItsNameAndDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	oldName := "zde-20260820T115959Z.png"
	newName := "zde-20260820T120001Z.png"
	plant(t, dir, oldName, noon.Add(time.Hour))
	plant(t, dir, newName, noon)

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, png, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "zde-20260820T120002Z.png")); err != nil {
		t.Fatal(err)
	}

	got, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, newName); got != want {
		t.Errorf("Latest = %q, want newest real capture %q", got, want)
	}
}

func TestLatestSaysSoWhenNothingWasCaptured(t *testing.T) {
	dir := t.TempDir()
	plant(t, dir, "Screenshot from 2026-08-20 12-00-30.png", noon)
	_, err := Latest(dir)
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("Latest without a zde capture = %v", err)
	}
}

func TestASecondThatIsFullStops(t *testing.T) {
	dir := t.TempDir()
	for sequence := 1; sequence <= tries; sequence++ {
		name := noonName
		if sequence > 1 {
			name = fmt.Sprintf("zde-20260820T120000Z-%d.png", sequence)
		}
		plant(t, dir, name, noon)
	}
	if _, err := claim(dir, noon); err == nil {
		t.Fatal("a second with every name taken produced one anyway")
	}
}
