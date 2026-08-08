package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
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
