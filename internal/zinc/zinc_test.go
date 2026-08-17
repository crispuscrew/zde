package zinc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAddress(t *testing.T) {
	if got := Address("browser", "work"); got != "browser@work" {
		t.Fatalf("Address(browser, work) = %q", got)
	}
	// The app that has always existed keeps the name it has always had. A
	// "browser@" here would be a new name for a running container, which is
	// the one thing instance addressing promised not to do.
	if got := Address("browser", ""); got != "browser" {
		t.Fatalf("Address(browser, no instance) = %q", got)
	}
}

func TestParseWhere(t *testing.T) {
	// The shape zinc documents as the contract (zinc CHANGELOG, 0.8.1).
	out := "state: /home/u/.local/state/zinc/firefox/work\ncontainer: firefox.work\n"
	loc, err := parseWhere(out)
	if err != nil {
		t.Fatal(err)
	}
	if loc.State != "/home/u/.local/state/zinc/firefox/work" {
		t.Fatalf("state = %q", loc.State)
	}
	// The runtime name is the other half and is not derivable from the address
	// by anyone who does not already know the `@` becomes a `.`.
	if loc.Container != "firefox.work" {
		t.Fatalf("container = %q", loc.Container)
	}
}

func TestParseWhereRefusesHalfAnAnswer(t *testing.T) {
	// A path with a colon in it is ordinary, so the value is everything after
	// the first one rather than everything before the second.
	loc, err := parseWhere("state: /srv/odd:name/zinc/a/b\ncontainer: a.b\n")
	if err != nil {
		t.Fatal(err)
	}
	if loc.State != "/srv/odd:name/zinc/a/b" {
		t.Fatalf("state = %q", loc.State)
	}
	// Missing either line is a zcr that has changed its mind about the format.
	// Filling in the gap is how a confident wrong path gets printed, so this
	// says it cannot rather than guessing.
	for _, out := range []string{
		"state: /home/u/.local/state/zinc/firefox/work\n",
		"container: firefox.work\n",
		"firefox\n",
		"",
	} {
		if _, err := parseWhere(out); err == nil {
			t.Fatalf("parseWhere(%q) accepted half an answer", out)
		}
	}
}

// The wordings the refusal has to be recognised in.
//
// A table because this is a text match and text matches rot: zcr exits 1 for
// this and for an image that would not build alike (v0.9.1,
// container/runner/main.go), so the sentence is the whole of the evidence and
// the only defence is being explicit about which sentences count. Every entry
// here is either what zinc 0.9.1 prints or a rewording of it that keeps the
// phrase both of zinc's runners share.
func TestZcrsRefusalToStartASecondCopyIsNotAFailure(t *testing.T) {
	refusals := []string{
		// v0.9.1 whole, as CombinedOutput hands it back: the sentence from
		// container/runner/app/service.go, with the "zcr: " main puts in front.
		"zcr: browser is already running; stop it first, or run another instance with browser@<instance>",
		// An instance. The name in the sentence is the runtime one, because the
		// instance rides on AppNameID from loadApp down, so a manifest's
		// browser@work is browser.work by the time it is refused.
		"zcr: browser.work is already running; stop it first, or run another instance with browser.work@<instance>",
		// The advice reworded or dropped, which is the change this has to
		// survive: it is a sentence for a person and nothing promises it.
		"zcr: notes is already running",
		"zcr: notes is already running; use `zcr restart notes` to replace it",
		// zinc's VM runner says it with the pid instead of the advice, and an
		// app's own name is not always lowercase - what is matched is the
		// phrase after it, so neither matters.
		"Error: NOTES is already running (pid 4131)",
		// Not the first line and not the last: CombinedOutput merges whatever
		// the run printed on the way, and a dependency starts before the app
		// whose second launch is refused.
		"starting db for browser\nzcr: browser is already running; stop it first, or run another instance with browser@<instance>\n",
	}
	for _, out := range refusals {
		if !alreadyRunning(out) {
			t.Errorf("read as a failed launch: %q", out)
		}
	}
	// And the failures, which must stay failures: this is the direction that
	// costs somebody the notification the whole feature exists to send.
	failures := []string{
		"",
		"zcr: no app \"nvim\" defined (try: zc list)",
		"zcr: invalid config browser:\n  Volumes[0]: no such directory",
		"zcr: launch browser (run browser): Error: preparing container: image not known",
		// The near misses, which are the ones that matter: both say "already"
		// about something that is not the app, and podman's is a real failure
		// with a stale object behind it - a launch this swallowed would leave
		// somebody with no window and nothing said about it.
		"zcr: launch browser (run browser): Error: creating container storage: the container name \"browser\" is already in use by 9c1f0a",
		"zcr: browser: the derived image already exists; zcr build --force browser to rebuild it",
		// And a container that is running is not one that was already running
		// when this launch asked.
		"zcr: browser: the container is running an image older than the pin; zcr build browser first",
	}
	for _, out := range failures {
		if alreadyRunning(out) {
			t.Errorf("read as an app that was already up: %q", out)
		}
	}
}

// And the whole way through Run, because what the caller matches on is a
// sentinel that has to survive the wrapping: %s over the runner's output is
// what this package used to do to every error, and it is one character away
// from doing it again.
func TestRunHandsBackTheRefusalAsSomethingACallerCanRecognise(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo \"zcr: $2 is already running; stop it first, or run another instance with $2@<instance>\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, Runner), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	err := Run(context.Background(), "browser@work")
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run said %v, want something errors.Is can tell is the refusal", err)
	}
	// A failure from the same zcr is still a failure, with the runner's own
	// words in it - the refusal is the exception, not the new rule.
	broken := "#!/bin/sh\necho 'zcr: no app \"browser\" defined (try: zc list)' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, Runner), []byte(broken), 0o700); err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), "browser@work")
	if errors.Is(err, ErrAlreadyRunning) {
		t.Fatal("an app nobody has defined was reported as one that is already up")
	}
	if err == nil || !strings.Contains(err.Error(), "no app") {
		t.Fatalf("Run said %v, want what the runner said about it", err)
	}
}

// A launch is a subprocess, and the daemon that started it has to be able to end
// it. Before the context this took no cancel, no timeout, no process group and
// no WaitDelay: a session that ended left the launch running and it survived the
// daemon.
//
// Two things are asserted and the second is the one a plain Kill would fail: the
// call comes back, and what the runner forked is gone with it. A launch forks by
// definition - it is a client for a container runtime - so a kill that reached
// only the pid zde knows about would leave exactly the process that mattered.
func TestRunIsEndedByItsContext(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	// Its own pid and one it forked, then it holds.
	script := "#!/bin/sh\nsleep 60 &\necho \"$$ $!\" >> " + pids + "\nwait\n"
	if err := os.WriteFile(filepath.Join(dir, Runner), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// In front of the machine's own PATH rather than instead of it: the script
	// needs a `sleep`, and this Runner is the first one found either way.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, "browser@work") }()

	// Waited for on the fork rather than on the call: a cancel that arrived
	// before exec had forked would prove nothing about reaching a fork.
	deadline := time.Now().Add(10 * time.Second)
	var up []int
	for time.Now().Before(deadline) {
		up = alivePids(t, pids)
		if len(up) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(up) != 2 {
		t.Fatalf("the runner started %d processes, want the client and what it forked", len(up))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not come back when its context was cancelled")
	}
	// Waited for rather than read once: a killed process is a zombie until
	// whoever inherits it reaps it, and signal 0 succeeds against a zombie. What
	// is being asserted is that the kill reached them, not how fast init got
	// round to it.
	gone := time.Now().Add(5 * time.Second)
	for {
		left := 0
		for _, p := range up {
			if syscall.Kill(p, 0) == nil {
				left++
			}
		}
		if left == 0 {
			return
		}
		if time.Now().After(gone) {
			for _, p := range up {
				syscall.Kill(p, syscall.SIGKILL) //nolint:errcheck // tidying up after a failure
			}
			t.Fatalf("%d of the launch's %d processes were still running after it was cancelled", left, len(up))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// alivePids reads the pids the fake runner wrote down.
func alivePids(t *testing.T, path string) []int {
	t.Helper()
	raw, _ := os.ReadFile(path)
	var out []int
	for _, f := range strings.Fields(string(raw)) {
		n, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("the runner wrote %q where a pid belongs", f)
		}
		out = append(out, n)
	}
	return out
}
