package zded

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/manifest"
)

// A real SIGKILL checks kernel parent-death handling for both tiers and launches.
// This covers direct children, not descendants: Pdeathsig is cleared on fork.
func TestNothingTheDaemonStartedOutlivesItBeingKilled(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "tier")
	writeTiers(t, map[string][]string{TierProvider: fakeTier("linger", pidFile)})
	launched := fakeRunner(t)
	desks := t.TempDir()
	declared := "name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n  - { app: nvim, instance: vshop }\n"
	if err := os.WriteFile(filepath.Join(desks, "vshop.yaml"), []byte(declared), 0o644); err != nil {
		t.Fatal(err)
	}
	socket := socketPath(t)

	daemon := exec.Command(os.Args[0], "-test.run=^TestSacrificialDaemon$", "--", sacrificeMark, socket, desks)
	daemon.Stderr = os.Stderr
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { daemon.Process.Kill(); daemon.Wait() }() //nolint:errcheck // it is already dead by here

	var c *Client
	for i := 0; i < 500; i++ {
		var err error
		if c, err = DialPath(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c == nil {
		t.Fatal("the daemon never answered its socket")
	}
	defer c.Close()
	if err := c.Call(MethodAskRun, nil, TierProvider, "something that takes a while"); err != nil {
		t.Fatalf("ask.run: %v", err)
	}
	d, err := DialPath(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Call("desk.switch", nil, "vshop"); err != nil {
		t.Fatalf("desk.switch: %v", err)
	}

	pids := []int{tierPid(t, pidFile)}
	waitFor(t, "the launch starting", func() bool { return len(runnerPids(t, launched)) > 0 })
	pids = append(pids, runnerPids(t, launched)[0])
	for _, p := range pids {
		if syscall.Kill(p, 0) != nil {
			t.Fatalf("pid %d was not running before the daemon was killed", p)
		}
	}

	if err := daemon.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	daemon.Wait() //nolint:errcheck // the error is the signal, which is the point
	start := time.Now()
	gone := func() int {
		n := 0
		for _, p := range pids {
			if syscall.Kill(p, 0) == nil {
				n++
			}
		}
		return n
	}
	for i := 0; i < 500 && gone() > 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := gone(); n > 0 {
		for _, p := range pids {
			syscall.Kill(p, syscall.SIGKILL) //nolint:errcheck // tidying up after a failure
		}
		t.Fatalf("the daemon was killed and %d of the %d processes it started (%v) were still running %v later",
			n, len(pids), pids, time.Since(start))
	}
}

const sacrificeMark = "zde-sacrificial-daemon"

// Only marked subprocesses serve. The sleep bounds cleanup if the killing test fails.
func TestSacrificialDaemon(t *testing.T) {
	args := flag.Args()
	if len(args) < 3 || args[0] != sacrificeMark {
		return
	}
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.db", output: "DP-1"}, manifest.Dir(args[2]))
	if err := s.Listen(args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go s.Serve()
	time.Sleep(60 * time.Second)
	os.Exit(0)
}
