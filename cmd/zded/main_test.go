package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		done <- run(ctx, socket, filepath.Join(dir, "j.jsonl"), filepath.Join(dir, "desks"),
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
