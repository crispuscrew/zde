package zded

import (
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
)

// Events invalidate shell state; the daemon's reply remains the source of truth.
func TestZenTellsTheShell(t *testing.T) {
	server, _, _ := zenServer(t)
	path := serve(t, server)
	listener, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.Call(MethodEvents, nil); err != nil {
		t.Fatal(err)
	}
	asker, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer asker.Close()
	var state Zen
	if err := asker.Call("desk.zen", &state, "on"); err != nil {
		t.Fatal(err)
	}
	if !state.Zen {
		t.Error("desk.zen on came back saying off")
	}
	event, err := listener.NextEventBefore(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatalf("nothing told the shell that chrome changed: %v", err)
	}
	if event.Kind != EventZen {
		t.Errorf("shell received %q, want %q", event.Kind, EventZen)
	}
}

// Hiding chrome must not silence notifications or change the attention mode.
func TestZenChangesNoDisplayPolicy(t *testing.T) {
	server, _, _ := zenServer(t)
	if response := server.Dispatch(Request{Method: "attn.mode", Args: []string{"focus"}}); response.Error != "" {
		t.Fatal(response.Error)
	}
	before := server.mode()
	server.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if got := server.mode(); got != before {
		t.Errorf("zen changed attn mode from %q to %q", before, got)
	}
	listener, err := DialPath(serve(t, server))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.Call(MethodEvents, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Arrived(attn.Notification{From: "somebody", Text: "the build fell over", Urgent: true}); err != nil {
		t.Fatal(err)
	}
	event, err := listener.NextEventBefore(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatalf("zen is on and an urgent arrival drew nothing: %v", err)
	}
	if event.Kind != EventAttnPopup {
		t.Errorf("zen is on and arrival came through as %q, want %q", event.Kind, EventAttnPopup)
	}
}
