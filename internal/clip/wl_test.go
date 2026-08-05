package clip

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// helperEnv turns this test binary into the daemon half of the test below. The
// re-exec is the only way to get a process that can be killed outright: a test
// cannot SIGKILL itself and then report what happened.
const helperEnv = "ZDE_CLIP_ORPHAN_HELPER"

// A process zded started must not outlive zded being killed outright.
//
// SIGTERM was never the problem: the context cancellation wins that race and the
// child goes with the daemon. SIGKILL, an OOM kill and the session going down
// underneath the daemon are the problem, because cmd.Cancel only runs while
// there is a process left to run it. What that used to leave was
// `wl-paste --watch` reparented to pid 1, holding a connection to the real
// compositor and watching the real clipboard until logout - found alive on a
// developer's machine two hours after the run that started it.
//
// For a history whose whole argument is that it lives in memory and dies with
// the daemon, that is the feature contradicting itself, which is why this is
// worth a test that forks and kills rather than one that reads SysProcAttr back.
//
// If this regresses, a badly stopped zde leaves something reading the clipboard
// that nothing in the session will ever show you.
func TestASpawnedToolDoesNotOutliveAKilledDaemon(t *testing.T) {
	if os.Getenv(helperEnv) == "1" {
		orphanHelper()
		return
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep on this machine to stand in for wl-paste")
	}

	helper := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	helper.Env = append(os.Environ(), helperEnv+"=1")
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}

	// The pid of what the helper started, which is the thing this test is about.
	var pid int
	if _, err := fmt.Fscan(out, &pid); err != nil || pid <= 0 {
		helper.Process.Kill() //nolint:errcheck // it is on its way out either way
		helper.Wait()         //nolint:errcheck // the status is not the point
		t.Fatalf("the helper never said what it started (pid %d): %v", pid, err)
	}

	// Killed outright and given no chance to tidy up, which is the whole point:
	// anything that depends on this process running again to clean up has
	// already failed by here.
	if err := helper.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	helper.Wait() //nolint:errcheck // it was killed, so of course it failed

	// Reparented to pid 1 and reaped there rather than here, so this is a poll
	// against signal 0 and not a Wait. Five seconds is far past the moment the
	// kernel delivers a parent-death signal and well short of the sleep's own
	// life, so a pass cannot be the stand-in exiting on its own.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL) //nolint:errcheck // never leave the test's own orphan behind
	t.Fatalf("pid %d outlived the process that started it: a killed zded leaves a clipboard watcher "+
		"reading the session's clipboard until logout", pid)
}

// orphanHelper is the daemon half: it starts a long-lived child through the same
// guard the watcher uses, says which pid it is, and then waits to be killed.
//
// `sleep` stands in for `wl-paste --watch` on purpose. The mechanism under test
// is entirely in guard, and a test that attached a second real watcher to
// whatever compositor the developer is sitting in front of is how the bug being
// fixed here was found in the first place.
func orphanHelper() {
	// CommandContext and not Command: guard sets Cancel, and exec refuses a
	// Cancel with no context to fire it.
	cmd := exec.CommandContext(context.Background(), "sleep", "300")
	guard(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Println(0)
		return
	}
	fmt.Println(cmd.Process.Pid)
	os.Stdout.Sync() //nolint:errcheck // the parent is reading this pipe
	// Long enough that the child is still alive whether or not the fix works,
	// so the test measures the fix and not this timer.
	time.Sleep(2 * time.Minute)
}
