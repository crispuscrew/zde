package zded

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Children retaining either output pipe must not delay completion or survive the run.
func TestATierThatForksDoesNotWedgeTheRun(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild")
	writeTiers(t, map[string][]string{TierProvider: fakeTier("fork", pidFile)})
	path := askServer(t)

	start := time.Now()
	text, failure := askAll(t, path, TierProvider, "answer and fork")
	took := time.Since(start)
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if text != "answered and forked" {
		t.Errorf("the tier answered %q", text)
	}
	if took > 2*time.Second {
		t.Errorf("the run took %v for a tier that answered and exited at once", took)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the forked child never said where it was: %v", err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	gone := func() bool { return syscall.Kill(pid, 0) != nil }
	for i := 0; i < 200 && !gone(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !gone() {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the tier forked a child and the run left it running")
	}
}

// Close must finish bounded reaping before returning and refuse later runs without leaked claims.
func TestATierDoesNotOutliveTheDaemonThatStartedIt(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "tier")
	writeTiers(t, map[string][]string{TierProvider: fakeTier("linger", pidFile)})
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	path := serve(t, s)

	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodAskRun, nil, TierProvider, "something that takes a while"); err != nil {
		t.Fatalf("ask.run: %v", err)
	}
	pid := tierPid(t, pidFile)
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("the tier was not running before the daemon was stopped")
	}

	start := time.Now()
	s.Close()
	took := time.Since(start)
	if took > runStopWait {
		t.Errorf("stopping the daemon took %v, above its own ceiling of %v", took, runStopWait)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the daemon stopped and its tier (pid %d) is still running", pid)
	}

	rec := &recorder{}
	s.askOn(&sink{w: rec}, []string{TierProvider, "one more"}, "")
	if !strings.Contains(rec.String(), "stopping") {
		t.Errorf("an ask that arrived after the daemon stopped was answered with %q", rec.String())
	}
	if n := s.asking(); n != 0 {
		t.Errorf("the refused ask left %d of the daemon's %d places claimed", n, asksMax)
	}
}

// Disconnect cancels even a silent tier and releases its daemon-wide claim.
func TestClosingTheConnectionStopsTheTier(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "tier")
	writeTiers(t, map[string][]string{TierLocal: fakeTier("linger", pidFile)})
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	path := serve(t, s)

	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Call(MethodAskRun, nil, TierLocal, "something quiet and long"); err != nil {
		t.Fatalf("ask.run: %v", err)
	}
	pid := tierPid(t, pidFile)
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("the tier was not running before the connection was closed")
	}

	start := time.Now()
	c.Close()
	gone := func() bool { return syscall.Kill(pid, 0) != nil }
	for i := 0; i < 500 && !gone(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	took := time.Since(start)
	if !gone() {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the connection closed and the tier (pid %d) was still running %v later; without this it goes at askTimeout, which is %v",
			pid, took, askTimeout)
	}
	if took > 2*time.Second {
		t.Errorf("the tier took %v to notice its connection had gone", took)
	}

	for i := 0; i < 200 && s.asking() != 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.asking(); n != 0 {
		t.Errorf("the run ended and the daemon still counts %d tiers running", n)
	}
}
