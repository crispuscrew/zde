package clip

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// And that the guard is actually reached by the code that runs wl-clipboard,
// which the test above cannot say: it calls guard itself, so deleting the call
// from a spawn site would leave it green. This one goes through read, the
// function Types and Read are both built on, and read takes its argv - so the
// path under test is the production one and the program is a stand-in.
//
// Its own process group is the half of the guard that is visible from outside
// without killing anything. If the child shares this process's group then
// nothing was applied to it, and the parent-death signal set beside it is not
// there either.
//
// If this regresses, the fix above is still written down and no longer runs.
func TestAToolThisPackageRunsIsInItsOwnProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on this machine to stand in for wl-clipboard")
	}
	// Field 5 of /proc/self/stat is the process group. cut is a child of the sh
	// this package spawned and inherits its group, so what it prints is the
	// group the guard was supposed to create.
	out, more, err := read(context.Background(), 64, "sh", "-c", "cut -d' ' -f5 /proc/self/stat")
	if err != nil {
		t.Fatalf("running a stand-in through read: %v", err)
	}
	if more {
		t.Fatal("a process group id does not run to 64 bytes")
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("the stand-in printed %q, which is not a process group", out)
	}
	mine, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if got == mine {
		t.Errorf("the spawned process is in this process's group (%d), so nothing put it in its own "+
			"and cancelling one reaches only the pid zde knows about", mine)
	}
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

// The watcher itself is started through the guard, and that is a claim about a
// call site rather than about a helper.
//
// The two tests above prove the mechanism and neither can see this. The killed
// daemon one calls guard by hand in its helper, so it would pass if no caller in
// the package ever reached it. The process group one goes through read, which
// Types and Read are built on - a real call site, and not this one. So
// `spawn(ctx, Paste, "--watch", ...)` could become a plain exec.CommandContext
// and the whole package stays green.
//
// That is not a hypothetical line to have got wrong. `wl-paste --watch` is the
// exact process the guard was written for: it is the one thing here meant to
// last the session, it is what was found alive against a developer's real
// clipboard two hours after the run that started it, and it is the only spawn
// in this package whose child is not expected to exit in milliseconds by
// itself. If any call site is worth pinning it is this one.
//
// What is asserted is the process group, because that is the half of the guard
// visible from outside without killing anything: a watcher sharing this
// process's group is one nothing was applied to, so the parent-death signal set
// beside it is not there either. Which is the same reasoning the read-side test
// makes, asked of the call that matters most.
//
// The stand-in is a script named wl-paste on a PATH of this test's own. It has
// to be a stand-in: attaching a second real watcher to whatever compositor the
// developer is sitting in front of is how the bug being fixed here was found in
// the first place, and this one prints a line and sleeps rather than reading
// anybody's clipboard.
func TestTheClipboardWatcherIsStartedThroughTheGuardToo(t *testing.T) {
	for _, prog := range []string{"sh", "sleep", "cut"} {
		if _, err := exec.LookPath(prog); err != nil {
			t.Skipf("no %s on this machine to stand in for wl-clipboard", prog)
		}
	}
	dir := t.TempDir()
	where := filepath.Join(dir, "group")
	// Field 5 of /proc/self/stat is the process group. The cut runs in a
	// subshell of the stand-in and inherits its group, so what it writes is the
	// group the guard was supposed to put the watcher in.
	shim := "#!/bin/sh\n" +
		"cut -d' ' -f5 /proc/self/stat > " + where + "\n" +
		// The bell. Watch scans this process's stdout and rings its channel per
		// line, so one line here is what tells the test the production path ran
		// rather than something that merely started.
		"echo text/plain\n" +
		// Long enough that it is still there to be asked about, and killed by
		// the cancel below either way.
		"exec sleep 300\n"
	if err := os.WriteFile(filepath.Join(dir, Paste), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	changes, err := Tool{}.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch with a stand-in on PATH: %v", err)
	}
	select {
	case <-changes:
	case <-time.After(30 * time.Second):
		t.Fatal("the watcher never rang, so nothing below is about a watcher that ran")
	}

	var group int
	for i := 0; i < 500; i++ {
		raw, err := os.ReadFile(where)
		if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			group, err = strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatalf("the stand-in wrote %q where a process group was expected", raw)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if group == 0 {
		t.Fatal("the stand-in never said which process group it was in")
	}
	mine, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	// Fatal and not an error to carry on from, which is a fact about what
	// follows rather than about how bad this is: everything below signals the
	// group, and a group that turned out to be this test's own is one where
	// doing that kills the run. A test that fails by taking the suite with it
	// says nothing about what went wrong.
	if group == mine {
		stop()
		t.Fatalf("the clipboard watcher is in this process's group (%d), so it was not started "+
			"through spawn: cancelling it reaches only the pid zde knows about, and the "+
			"parent-death signal that outlives a killed daemon is not set on it either", mine)
	}

	// And it goes when the daemon lets go of it. The tidy half rather than the
	// discriminating one - a plain exec.CommandContext would also kill this -
	// but a watcher left running by this test is the very thing the test is
	// about, so it is worth saying out loud that it went.
	stop()
	gone := false
	for i := 0; i < 500; i++ {
		if err := syscall.Kill(group, 0); err != nil {
			gone = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !gone {
		syscall.Kill(-group, syscall.SIGKILL) //nolint:errcheck // never leave this test's own orphan behind
		t.Errorf("the watcher in group %d was still running after the context ended", group)
	}
}
