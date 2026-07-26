package niri

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeNiri speaks the protocol niri documents: one JSON request per line, one
// JSON reply per line. Replies are given in order, so a test says what the
// compositor answers and nothing else.
func fakeNiri(t *testing.T, replies ...string) string {
	t.Helper()
	// A short path: a unix socket address is capped near 108 bytes and
	// t.TempDir is long enough to matter.
	dir, err := os.MkdirTemp("", "niri")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); os.RemoveAll(dir) })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		for _, rep := range replies {
			if _, err := r.ReadBytes('\n'); err != nil {
				return
			}
			if _, err := conn.Write(append(oneLine(rep), '\n')); err != nil {
				return
			}
		}
	}()
	return path
}

// niri puts one reply on one line. The fixtures above are written across
// several for reading, so they get compacted the way the compositor would send
// them - except the ones that are deliberately not JSON, which go as they are.
func oneLine(s string) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return []byte(s)
	}
	return buf.Bytes()
}

func dial(t *testing.T, path string) *Client {
	t.Helper()
	c, err := DialPath(path)
	if err != nil {
		t.Fatalf("DialPath: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestWorkspaces(t *testing.T) {
	path := fakeNiri(t, `{"Ok":{"Workspaces":[
		{"id":1,"idx":1,"name":"vshop.DP-1.code","output":"DP-1","is_active":true,"is_focused":true},
		{"id":2,"idx":2,"name":null,"output":"DP-1","is_active":false,"is_focused":false}
	]}}`)
	got, err := dial(t, path).Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d workspaces, want 2", len(got))
	}
	if got[0].ID != 1 || *got[0].Name != "vshop.DP-1.code" || *got[0].Output != "DP-1" || !got[0].Active {
		t.Errorf("first workspace = %+v", got[0])
	}
	// niri's own unnamed workspace: a null name is not an empty name.
	if got[1].Name != nil {
		t.Errorf("unnamed workspace has name %q, want nil", *got[1].Name)
	}
}

func TestOutputs(t *testing.T) {
	path := fakeNiri(t, `{"Ok":{"Outputs":{
		"DP-1":{"name":"DP-1","make":"Dell","model":"U2515H"},
		"HDMI-A-1":{"name":"HDMI-A-1","make":"x","model":"y"}
	}}}`)
	got, err := dial(t, path).Outputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Outputs = %v, want two", got)
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "DP-1") || !strings.Contains(joined, "HDMI-A-1") {
		t.Errorf("Outputs = %v", got)
	}
}

// The join this package exists for: niri's answers become the desk map.
func TestDeskMap(t *testing.T) {
	path := fakeNiri(t,
		`{"Ok":{"Workspaces":[
			{"id":1,"idx":1,"name":"vshop.DP-1.code","output":"DP-1","is_active":true},
			{"id":2,"idx":1,"name":"vshop.DP-1.agent","output":"HDMI-A-1","is_active":false},
			{"id":3,"idx":2,"name":null,"output":"DP-1","is_active":false}
		]}}`,
		`{"Ok":{"Outputs":{"DP-1":{"name":"DP-1"},"HDMI-A-1":{"name":"HDMI-A-1"}}}}`,
	)
	m, err := dial(t, path).DeskMap()
	if err != nil {
		t.Fatal(err)
	}
	if got := m.DeskNames(); len(got) != 1 || got[0] != "vshop" {
		t.Errorf("DeskNames = %v, want vshop", got)
	}
	// Both monitors are connected, so the one niri moved is a move: renamed.
	if got := m.Renames(); len(got) != 1 || got[0].To.String() != "vshop.HDMI-A-1.agent" {
		t.Errorf("Renames = %v, want the moved workspace corrected", got)
	}
	// niri's unnamed workspace is nobody's until adoption claims it.
	if got := m.Foreign(); len(got) != 1 || got[0].Output != "DP-1" {
		t.Errorf("Foreign = %v, want the unnamed workspace with its output", got)
	}
}

// A monitor niri no longer has is what makes a displaced workspace, so the
// output list has to reach the desk model intact.
func TestDeskMapUnpluggedMonitor(t *testing.T) {
	path := fakeNiri(t,
		`{"Ok":{"Workspaces":[{"id":1,"idx":1,"name":"vshop.DP-1.code","output":"eDP-1"}]}}`,
		`{"Ok":{"Outputs":{"eDP-1":{"name":"eDP-1"}}}}`,
	)
	m, err := dial(t, path).DeskMap()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Renames()) != 0 {
		t.Errorf("Renames = %v, want none: DP-1 is gone, not wrong", m.Renames())
	}
	if got := m.Displaced(); len(got) != 1 || got[0].Monitor != "DP-1" {
		t.Errorf("Displaced = %v, want the workspace with its home intact", got)
	}
}

// niri answers a request it did not like with Err, and that is not our error
// to swallow.
func TestErrReply(t *testing.T) {
	path := fakeNiri(t, `{"Err":"unknown request"}`)
	_, err := dial(t, path).Workspaces()
	if err == nil || !strings.Contains(err.Error(), "unknown request") {
		t.Errorf("got %v, want niri's message", err)
	}
}

// Anything that is not the protocol has to fail loudly rather than read as an
// empty desktop: no workspaces at all is a state zded would act on.
func TestGarbageReplies(t *testing.T) {
	for _, rep := range []string{
		`not json`,
		`{}`,                           // neither Ok nor Err
		`{"Ok":{"Windows":[]}}`,        // the wrong variant
		`{"Ok":{"Workspaces":"nope"}}`, // right variant, wrong shape
	} {
		t.Run(rep, func(t *testing.T) {
			path := fakeNiri(t, rep)
			if _, err := dial(t, path).Workspaces(); err == nil {
				t.Errorf("reply %q was accepted", rep)
			}
		})
	}
}

// A compositor that hangs up mid-conversation is an error, not an empty
// desktop.
func TestCompositorHangsUp(t *testing.T) {
	path := fakeNiri(t) // accepts, then closes without replying
	_, err := dial(t, path).Workspaces()
	if err == nil {
		t.Fatal("a compositor that hung up did not produce an error")
	}
	if !strings.Contains(err.Error(), "Workspaces") {
		t.Errorf("error does not say what was being asked: %v", err)
	}
}

// A compositor that holds the connection open and says nothing must not wedge
// the caller. zded is the process that gets asked why the desktop is frozen;
// it cannot be the one that is frozen.
func TestSilentCompositorTimesOut(t *testing.T) {
	dir, err := os.MkdirTemp("", "niri")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	held := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		<-held // hold it open, answer nothing
		conn.Close()
	}()
	t.Cleanup(func() { close(held) })

	defer func(d time.Duration) { requestTimeout = d }(requestTimeout)
	requestTimeout = 150 * time.Millisecond

	start := time.Now()
	if _, err := dial(t, path).Workspaces(); err == nil {
		t.Fatal("a silent compositor did not produce an error")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("waited %v on a silent compositor", waited)
	}
}

func TestDialWithoutEnv(t *testing.T) {
	t.Setenv(SocketEnv, "")
	if _, err := Dial(); err == nil || !strings.Contains(err.Error(), SocketEnv) {
		t.Errorf("got %v, want an error naming %s", err, SocketEnv)
	}
}

func TestDialUsesEnv(t *testing.T) {
	path := fakeNiri(t, `{"Ok":{"Outputs":{"DP-1":{"name":"DP-1"}}}}`)
	t.Setenv(SocketEnv, path)
	c, err := Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, err := c.Outputs(); err != nil || len(got) != 1 {
		t.Errorf("Outputs = %v, %v", got, err)
	}
}
