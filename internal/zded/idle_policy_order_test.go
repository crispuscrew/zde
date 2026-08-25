package zded

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDisplayOnWaitsForAnEarlierDelayedOff(t *testing.T) {
	server, _, _ := powerServer(t)
	t.Cleanup(func() { server.Close() })
	server.laptopPath = filepath.Join(t.TempDir(), "laptop")
	if err := os.WriteFile(server.laptopPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	compositor := server.niri.(*fakeCompositor)
	compositor.blockPerform = "power-off-monitors"
	compositor.performStarted = make(chan struct{})
	compositor.performRelease = make(chan struct{})

	offDone := make(chan Response, 1)
	go func() { offDone <- server.idleAction("display-off") }()
	select {
	case <-compositor.performStarted:
	case <-time.After(time.Second):
		t.Fatal("display-off never reached the delayed compositor call")
	}

	onDone := make(chan Response, 1)
	onInvoked := make(chan struct{})
	go func() {
		close(onInvoked)
		onDone <- server.idleAction("display-on")
	}()
	<-onInvoked
	select {
	case resp := <-onDone:
		close(compositor.performRelease)
		t.Fatalf("display-on completed before the older off: %+v", resp)
	case <-time.After(50 * time.Millisecond):
	}
	close(compositor.performRelease)

	for name, done := range map[string]<-chan Response{"off": offDone, "on": onDone} {
		select {
		case resp := <-done:
			if resp.Error != "" {
				t.Fatalf("display-%s: %s", name, resp.Error)
			}
		case <-time.After(time.Second):
			t.Fatalf("display-%s did not finish", name)
		}
	}
	if got := compositor.performCalls(); len(got) != 2 || got[0] != "power-off-monitors" || got[1] != "power-on-monitors" {
		t.Fatalf("overlapping display events completed as %v, want off then on", got)
	}
}
