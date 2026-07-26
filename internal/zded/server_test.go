package zded

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
)

type fakeCompositor struct {
	m   *desk.Map
	err error
}

func (f fakeCompositor) DeskMap() (*desk.Map, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.m, nil
}

func twoDesks() *desk.Map {
	return desk.Rebuild([]desk.Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "haven.DP-1.db", Output: "DP-1"},
	}, []string{"DP-1"})
}

// A short socket path: the address is capped near 108 bytes and t.TempDir is
// long enough to matter.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "zded")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

func serve(t *testing.T, s *Server) string {
	t.Helper()
	path := socketPath(t)
	if err := s.Listen(path); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return path
}

func TestStatus(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetLastDesk("vshop")

	s := New("test", jrn, fakeCompositor{m: twoDesks()})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var st Status
	if err := c.Call("status", &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != "test" || st.Compositor != "connected" || st.Desks != 2 || st.LastDesk != "vshop" {
		t.Errorf("status = %+v", st)
	}
}

// The case someone runs `zde status` to understand: zded is up, niri is not.
// It has to answer, and say so, rather than fail.
func TestStatusWithoutCompositor(t *testing.T) {
	s := New("test", nil, fakeCompositor{err: errors.New("NIRI_SOCKET is not set")})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var st Status
	if err := c.Call("status", &st); err != nil {
		t.Fatalf("status failed while niri was down: %v", err)
	}
	if !strings.Contains(st.Compositor, "NIRI_SOCKET") {
		t.Errorf("compositor = %q, want the reason it is not connected", st.Compositor)
	}
	if st.Desks != 0 {
		t.Errorf("desks = %d, want none claimed while niri is unreachable", st.Desks)
	}
}

// A method that does need the compositor fails, with niri's reason, rather
// than reporting an empty desktop.
func TestDeskListWithoutCompositor(t *testing.T) {
	s := New("test", nil, fakeCompositor{err: errors.New("connection refused")})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var desks []string
	err = c.Call("desk.list", &desks)
	if err == nil {
		t.Fatal("desk.list answered while niri was unreachable")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %v, want the reason", err)
	}
}

func TestDeskList(t *testing.T) {
	s := New("test", nil, fakeCompositor{m: twoDesks()})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var desks []string
	if err := c.Call("desk.list", &desks); err != nil {
		t.Fatal(err)
	}
	if len(desks) != 2 || desks[0] != "haven" || desks[1] != "vshop" {
		t.Errorf("desk.list = %v", desks)
	}
}

func TestUnknownMethod(t *testing.T) {
	s := New("test", nil, fakeCompositor{m: twoDesks()})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.explode", nil); err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Errorf("got %v, want an unknown-method error", err)
	}
}

// One connection, several requests: the shell will hold one open.
func TestManyRequestsOnOneConnection(t *testing.T) {
	s := New("test", nil, fakeCompositor{m: twoDesks()})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 5; i++ {
		var st Status
		if err := c.Call("status", &st); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
}

// Garbage on the socket is answered, not fatal: one bad client must not take
// the daemon down with it.
func TestGarbageRequest(t *testing.T) {
	s := New("test", nil, fakeCompositor{m: twoDesks()})
	path := serve(t, s)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("this is not json\n"))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("reply to garbage is not a reply: %q", buf[:n])
	}
	if resp.Error == "" {
		t.Error("garbage was accepted")
	}
	// And the daemon is still there.
	c, err := DialPath(path)
	if err != nil {
		t.Fatalf("daemon died on a bad request: %v", err)
	}
	defer c.Close()
	if err := c.Call("status", &Status{}); err != nil {
		t.Errorf("daemon stopped answering: %v", err)
	}
}

// The socket is the zde boundary, so it is not world-reachable even before the
// peer check runs.
func TestSocketIsPrivate(t *testing.T) {
	s := New("test", nil, fakeCompositor{m: twoDesks()})
	path := serve(t, s)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("socket mode is %o, want nothing for group or other", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("socket directory mode is %o, want nothing for group or other", perm)
	}
}

// Two daemons on one socket is worse than one that refuses to start.
func TestSecondDaemonRefuses(t *testing.T) {
	first := New("test", nil, fakeCompositor{m: twoDesks()})
	path := serve(t, first)

	second := New("test", nil, fakeCompositor{m: twoDesks()})
	err := second.Listen(path)
	if err == nil {
		second.Close()
		t.Fatal("a second zded bound the same socket")
	}
	if !strings.Contains(err.Error(), "already listening") {
		t.Errorf("error = %v, want it to say why", err)
	}
}

// A socket left behind by a crashed zded must not stop the next one.
func TestStaleSocketIsReplaced(t *testing.T) {
	path := socketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // the file stays; nobody is listening

	// Recreate the file, since closing the listener removes it: this is what a
	// SIGKILLed daemon leaves behind.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := New("test", nil, fakeCompositor{m: twoDesks()})
	if err := s.Listen(path); err != nil {
		t.Fatalf("a stale socket stopped the daemon: %v", err)
	}
	defer s.Close()
}

func TestDefaultSocketNeedsRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := DefaultSocket(); err == nil {
		t.Error("DefaultSocket invented a path without XDG_RUNTIME_DIR")
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	got, err := DefaultSocket()
	if err != nil || got != "/run/user/1000/zde/zded.sock" {
		t.Errorf("DefaultSocket() = %q, %v", got, err)
	}
}
