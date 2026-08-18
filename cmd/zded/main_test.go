package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/zded"
)

// A session bus that will not answer must not cost the session its keybinds.
//
// This is what a sick bus did: attn.Serve blocked in D-Bus authentication, and
// srv.Serve came after it. The listener was already bound by then, so every
// `zde` call connected into the backlog and got nothing at all until the client
// gave up five seconds later - every key in the keymap failing, for as long as
// the bus stayed sick, because of the notification server.
//
// Break the order and this test hangs on the first call rather than failing on
// an assertion, which is exactly what the session did.
func TestKeybindsAreAnsweredWhileTheSessionBusIsStuck(t *testing.T) {
	dir := shortDir(t)
	socket := filepath.Join(dir, "s")

	stuck := make(chan struct{})
	taking := make(chan struct{})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, socket, filepath.Join(dir, "j.jsonl"), filepath.Join(dir, "desks"), filepath.Join(dir, "history.json"),
			func(attn.Sink, string) (*attn.Server, error) {
				close(taking)
				<-stuck
				return nil, errors.New("session bus: no answer")
			},
			// No clipboard. The real one spawns wl-paste against whatever
			// Wayland session is running, so a test of the startup order would
			// otherwise put whatever the person running it had copied into a
			// daemon - and leave a watcher behind holding this process's stderr.
			nil)
	}()
	<-taking // inside the bus, and staying there

	c, err := dialWithin(t, socket, 2*time.Second)
	if err != nil {
		close(stuck)
		t.Fatal(err)
	}
	defer c.Close()
	var st zded.Status
	if err := c.Call("status", &st); err != nil {
		close(stuck)
		t.Fatalf("zded did not answer while the session bus was stuck: %v", err)
	}
	if st.Notifications {
		t.Error("status claims the notification name while nothing has taken it")
	}

	// And it stops when it is told to, rather than waiting for the bus first.
	stop()
	close(stuck)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not stop")
	}
	// The socket file goes with it, or the next zded refuses to start.
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket file outlived the daemon: %v", err)
	}
}

// fakeTierMark is how this test binary knows it is being run as a tier. The one
// program certainly present in a build sandbox is the one already running, which
// is the same trick internal/zded plays for the same reason.
const fakeTierMark = "zded-fake-tier"

// TestFakeTier is a tier when it is run as one, and nothing at all otherwise. It
// says where it is and then stays: what is being tested is what happens to it
// when the daemon goes.
func TestFakeTier(t *testing.T) {
	args := flag.Args()
	if len(args) < 2 || args[0] != fakeTierMark {
		return
	}
	os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0o600)
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

// The daemon does not leave a tier running behind it.
//
// This is the process-level half of the same thing internal/zded tests at
// Close: the tier is started in a process group of its own, so the signal that
// stops the session stops the daemon and nothing the daemon started. What made
// it a bug at this level as well was the order here - closing the listener is
// what makes Serve return, so run could be back in main with the stopping still
// going on another goroutine, and the process would exit out from under it.
// Measured before both halves: zded gone 105ms after SIGTERM, the tier and its
// child still running under pid 1 eighteen seconds later.
//
// The assertion is made the moment run returns, with no polling, because
// "eventually" is exactly what a process that has already exited cannot promise.
func TestATierIsStoppedBeforeTheDaemonProcessGoes(t *testing.T) {
	dir := shortDir(t)
	socket := filepath.Join(dir, "s")
	pidFile := filepath.Join(dir, "tier")

	config := filepath.Join(dir, "c")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", config)
	if err := os.MkdirAll(filepath.Join(config, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	tiers, err := json.Marshal(map[string][]string{
		"provider": {os.Args[0], "-test.run=^TestFakeTier$", "--", fakeTierMark, pidFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "zde", "ask.json"), tiers, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, socket, filepath.Join(dir, "j.jsonl"), filepath.Join(dir, "desks"),
			filepath.Join(dir, "history.json"),
			func(attn.Sink, string) (*attn.Server, error) {
				return nil, errors.New("no bus in a test")
			},
			// No clipboard, for the reason the other one gives: the real tool
			// spawns wl-paste against whatever Wayland session is running.
			nil)
	}()

	c, err := dialWithin(t, socket, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("ask.run", nil, "provider", "something that takes a while"); err != nil {
		t.Fatalf("ask.run: %v", err)
	}
	pid := tierPid(t, pidFile)

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not stop")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the daemon returned and the tier it started (pid %d) is still running", pid)
	}
}

// A daemon that is stuck before it serves still answers a signal.
//
// This is the general defect behind the FIFO one. signal.NotifyContext turns
// SIGTERM into a cancelled context, and a cancelled context is a stop only
// where somebody is reading it - and run reads it for the first time after the
// journal, the notification history, the desk manifests and niri's placement
// rules have all been opened. A signal arriving during any of that used to be
// swallowed: the process stayed, `systemctl --user stop zded` hung with it, and
// SIGKILL was the only thing left.
//
// Run rather than start is what is faked here, because the whole point is that
// the stuck thing is something no cancel can reach: a real one is an open on a
// path that is not a plain file, or a disk that has stopped answering.
func TestASignalStopsADaemonThatIsStuckBeforeItServes(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stuck := make(chan error) // nothing is ever sent: run never returns

	stop()
	// Answered on a deadline of its own, because the failure being tested for
	// is a wait with no end to it: without the grace this call does not return
	// a wrong answer, it returns no answer, and a test that only asserted on
	// the answer would be a CI job sitting there rather than a red one.
	type outcome struct {
		err     error
		stopped bool
	}
	got := make(chan outcome, 1)
	go func() {
		err, stopped := awaitStop(ctx, stuck, 100*time.Millisecond)
		got <- outcome{err, stopped}
	}()
	select {
	case o := <-got:
		if o.stopped {
			t.Fatalf("awaitStop claims the daemon left, and it is still in its own startup (err %v)", o.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("awaitStop did not give up on a daemon that is never going to answer, which is the hang zded shipped with")
	}
}

// And the stop that is only slow is waited for.
//
// The grace exists because the tidy exit takes real time - the tiers are
// killed and waited on, then the last notification snapshot is written - and a
// watchdog that fired through that would trade a daemon that will not stop for
// one that loses the last arrivals of every session.
func TestATidyStopIsWaitedForRatherThanCutShort(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond) // the tiers going, the snapshot written
		done <- nil
	}()

	stop()
	if err, stopped := awaitStop(ctx, done, 10*time.Second); !stopped || err != nil {
		t.Fatalf("awaitStop = (%v, %v), want a clean finish", err, stopped)
	}
}

// A failure that ends the daemon on its own is still the caller's to report,
// signal or no signal.
func TestAnErrorFromRunComesBack(t *testing.T) {
	want := errors.New("the listener would not bind")
	done := make(chan error, 1)
	done <- want
	err, stopped := awaitStop(context.Background(), done, time.Second)
	if !stopped || !errors.Is(err, want) {
		t.Fatalf("awaitStop = (%v, %v), want the error run returned", err, stopped)
	}
}

// tierPid is where the tier said it was, once it has said it.
func tierPid(t *testing.T, path string) int {
	t.Helper()
	for i := 0; i < 500; i++ {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatalf("the tier wrote %q where a pid was expected", raw)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the tier never said where it was (%s)", path)
	return 0
}

// dialWithin keeps trying until the daemon is listening or the time is up. The
// listener is bound inside run, so a test that dialled once would be racing the
// goroutine it just started rather than testing anything.
func dialWithin(t *testing.T, socket string, d time.Duration) (*zded.Client, error) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		c, err := zded.DialPath(socket)
		if err == nil {
			return c, nil
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// shortDir is a temporary directory with a short path: a unix socket address is
// capped near 108 bytes and t.TempDir spells out the test's name.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "zded")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// forged is what a workspace name can do to the terminal zded was started from,
// in one string: retitle its window (an OSC-0 sequence, ended with a BEL), and
// put a line of its own in the log that reads as something zded said.
//
// niri's, because a workspace name is a person's or an application's and it
// comes back inside niri's own sentence when something goes wrong with it. The
// same shape arrives from zcr, whose whole output from a launch that would not
// start is printed by the same daemon (internal/zded, launchApps) - a container
// build log, several lines of it, written by whatever the image builds.
const forged = "no workspace called \"code\x1b]0;OWNED\x07\nzded: everything is fine and nothing failed\" on DP-1"

// zdedMark is how this test binary knows it is being asked to be the daemon.
// The one program certainly present in a build sandbox is the one already
// running, which is the trick internal/zded plays for the same reason.
const zdedMark = "zded-under-test"

// TestZdedItself is the daemon when it is run as one, and nothing at all
// otherwise. Its stderr is the file the test that started it reads back, which
// is why this is a process and not a goroutine: what is asserted on is the
// bytes that reach a terminal, and the honest way to have them is to let a real
// zded write them to a real file descriptor.
func TestZdedItself(t *testing.T) {
	args := flag.Args()
	if len(args) < 5 || args[0] != zdedMark {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, args[1], args[2], args[3], args[4],
		func(attn.Sink, string) (*attn.Server, error) { return nil, errors.New("no bus in a test") },
		// No clipboard: the real one spawns wl-paste against whatever Wayland
		// session the machine has, and a test must not read the clipboard of
		// whoever is running it.
		nil); err != nil {
		fatal(err)
	}
	os.Exit(0)
}

// niri's words reach a terminal, and they used to reach it as niri wrote them.
// The reconcile loop printed the compositor's reply with fmt.Fprintln straight
// at stderr, so a workspace name with an escape sequence in it was an
// instruction to the terminal zded was started from - which is where this
// daemon is debugged, and the one reader journalctl's "blob data" does not
// cover. The same error was already filtered ten lines away for `zde status`.
//
// End to end over the real path, and a process rather than a goroutine: a fake
// niri answers the reconcile with the sentence above, a real zded logs it to a
// real file, and what this reads is the bytes a terminal would have acted on.
//
// If this regresses, the fix is not another call to a filter at whichever call
// site lost it. It is that the daemon has one door out and the door filters
// (main.go, errOut), so that a line added next year is covered by being written
// at all.
func TestNiriCannotDriveTheTerminalTheDaemonLogsTo(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("NIRI_SOCKET", fakeNiri(t, forged))
	// niri's placement rules are written under XDG_CONFIG_HOME, and this test
	// does not write into the niri config of whoever is running it.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))

	logPath := filepath.Join(dir, "stderr")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	daemon := exec.Command(os.Args[0], "-test.run=^TestZdedItself$", "--", zdedMark,
		filepath.Join(dir, "s"), filepath.Join(dir, "j.jsonl"), filepath.Join(dir, "desks"),
		filepath.Join(dir, "history.json"))
	daemon.Stderr = logFile
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		daemon.Process.Signal(syscall.SIGTERM) //nolint:errcheck // it is on its way out either way
		daemon.Wait()                          //nolint:errcheck // the exit status is not the assertion
	}()

	said := waitForFile(t, logPath, "reconcile:")

	for _, bad := range []string{"\x1b", "\x07"} {
		if strings.Contains(said, bad) {
			t.Errorf("the daemon's log still carries %q, which the terminal printing it acts on:\n%q", bad, said)
		}
	}
	// And no line of somebody else's starts in column one, where a line of
	// zded's own starts. A message with several lines in it - a build log, a
	// parser's caret under the column it did not like - is indented under the
	// line that introduces it, so it reads as the rest of that message rather
	// than as a second one the daemon never printed.
	for _, line := range strings.Split(said, "\n") {
		if strings.HasPrefix(line, "zded: everything is fine") {
			t.Errorf("a line niri wrote starts in column one, so the log holds a message zded never printed:\n%q", said)
		}
	}
	// The filter is not a refusal: what niri said is still there to be read, or
	// the daemon would be debugged from a log that says nothing.
	if !strings.Contains(said, "no workspace called") || !strings.Contains(said, "code") {
		t.Errorf("the log lost what niri said, which is what it is for:\n%q", said)
	}
}

// And the shape the same door gives a message with several lines in it, which
// is what a launch that would not start puts in this log: zcr hands back the
// whole of a container build (internal/zded, launchApps).
func TestAFailedLaunchsBuildLogIsIndentedUnderTheLineThatIntroducesIt(t *testing.T) {
	got := throughTheDoor(t, func() {
		fmt.Fprintf(errOut, "zded: starting nvim@vshop: zcr run nvim: %s\n",
			"STEP 3: RUN make\x1b[2Jmake: *** no rule to make target\nzded: nothing failed")
	})

	if strings.Contains(got, "\x1b") {
		t.Errorf("zcr's build log still carries an escape:\n%q", got)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("the message came out as %d lines, want the two it was written as:\n%q", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "zded: starting") {
		t.Errorf("the daemon's own first line is not in column one: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  ") {
		t.Errorf("zcr's second line is not indented, so it reads as a message zded printed: %q", lines[1])
	}
}

// throughTheDoor runs f with this process's stderr pointed at a file, and
// returns what came out of it.
func throughTheDoor(t *testing.T, f func()) string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = tmp
	f()
	os.Stderr = old
	said, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(said)
}

// fakeNiri is a compositor that answers every request with one sentence, and
// the event stream that makes the daemon ask. Its address, for $NIRI_SOCKET.
func fakeNiri(t *testing.T, said string) string {
	t.Helper()
	path := filepath.Join(shortDir(t), "niri")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	refusal, err := json.Marshal(said)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serveFakeNiri(c, string(refusal))
		}
	}()
	return path
}

// serveFakeNiri speaks niri's line protocol: one JSON request a line, one reply
// a line, and after the event stream is asked for, events.
func serveFakeNiri(c net.Conn, refusal string) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if strings.Contains(line, "EventStream") {
			// Taken, and then one event, which is what makes the daemon
			// reconcile. The connection stays open the way niri's does: this
			// loop goes back to reading, and unblocks when the daemon closes it.
			io.WriteString(c, "{\"Ok\":{\"Handled\":null}}\n")                 //nolint:errcheck // a dead client ends the loop
			io.WriteString(c, "{\"WorkspacesChanged\":{\"workspaces\":[]}}\n") //nolint:errcheck // same
			continue
		}
		io.WriteString(c, "{\"Err\":"+refusal+"}\n") //nolint:errcheck // same
	}
}

// waitForFile waits for what the daemon writes from a goroutine of its own,
// which is after the process that started it has come up.
func waitForFile(t *testing.T, path, want string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var raw []byte
	for {
		raw, _ = os.ReadFile(path)
		if strings.Contains(string(raw), want) {
			return string(raw)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the daemon never logged %q, and it is where this failure is written down:\n%s", want, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
