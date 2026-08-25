package capture

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Missing tools must refuse instead of falling back to niri's picker, which
// saves the monitor frame and can include a window blocked from capture.
func TestNoToolsIsARefusalAndNotAFallBackToNirisPicker(t *testing.T) {
	for _, test := range []struct {
		name        string
		slurp, grim string
		wantNamed   string
	}{
		{"no selector", "", writePNG, Selector},
		{"no grabber", `echo "0,0 10x10"`, "", Grabber},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools(t, test.slurp, test.grim)
			dir := t.TempDir()
			_, err := regionShot(context.Background(), dir, noonNow)
			if err == nil {
				t.Fatal("a region was captured with the tools missing")
			}
			if !strings.Contains(err.Error(), test.wantNamed) {
				t.Errorf("the refusal does not name what is missing: %v", err)
			}
			if !strings.Contains(err.Error(), "blocked from capture") {
				t.Errorf("the refusal does not say why niri's picker is not used: %v", err)
			}
			if got := namesIn(t, dir); len(got) != 0 {
				t.Errorf("it left %v behind", got)
			}
		})
	}
}

func TestASelectorNobodyAnswersIsEnded(t *testing.T) {
	quick(t)
	sleeper, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("nothing on this machine to wait with: %v", err)
	}
	tools(t, sleeper+" 60", writePNG)
	dir := t.TempDir()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := regionShot(context.Background(), dir, noonNow)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a selector that never answered produced a capture")
		}
		if !strings.Contains(err.Error(), regionWait.String()) {
			t.Errorf("the refusal does not say how long it waited: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("still waiting on the selector after %v", time.Since(start))
	}
	if got := namesIn(t, dir); len(got) != 0 {
		t.Errorf("it left %v behind", got)
	}
}

func TestTwoRegionsInOneSecondAreTwoFiles(t *testing.T) {
	tools(t, `echo "0,0 10x10"`, writePNG)
	dir := t.TempDir()
	first, err := regionShot(context.Background(), dir, noonNow)
	if err != nil {
		t.Fatal(err)
	}
	second, err := regionShot(context.Background(), dir, noonNow)
	if err != nil {
		t.Fatalf("the second region of the second: %v", err)
	}
	if first == second {
		t.Fatalf("both regions got %s", first)
	}
	if !completePNG(first) || !completePNG(second) {
		t.Errorf("one of the two region captures is incomplete")
	}
}

func TestANonemptyPartialPNGIsARefusal(t *testing.T) {
	tools(t, `echo "0,0 10x10"`, `printf partial > "$3"`)
	dir := t.TempDir()

	_, err := regionShot(context.Background(), dir, noonNow)
	if err == nil || !strings.Contains(err.Error(), "complete PNG") {
		t.Fatalf("partial PNG = %v", err)
	}
	if got := namesIn(t, dir); len(got) != 0 {
		t.Errorf("the partial capture left %v behind", got)
	}
}

func TestARegionIsNamedAfterSelectionFinishes(t *testing.T) {
	tools(t, `echo "$@" >> "$ZDE_TEST_LOG"; echo "0,0 10x10"`, writePNG)
	dir := t.TempDir()
	namedAfterSelection := false
	clock := func() time.Time {
		namedAfterSelection = strings.Contains(asked(t), "-f "+geometry)
		return noon
	}

	if _, err := regionShot(context.Background(), dir, clock); err != nil {
		t.Fatal(err)
	}
	if !namedAfterSelection {
		t.Error("the region filename was claimed before selection finished")
	}
}
