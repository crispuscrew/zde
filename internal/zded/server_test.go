package zded

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
)

type fakeCompositor struct {
	m       *desk.Map
	err     error
	focused string
	apps    map[uint64]string

	mu      sync.Mutex
	calls   []string // what was asked to be focused, in order
	renames []string
	adopted []string
	failOn  string
}

func (f *fakeCompositor) DeskMap() (*desk.Map, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.m, nil
}

func (f *fakeCompositor) FocusedName() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.focused, nil
}

func (f *fakeCompositor) FocusWorkspace(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == f.failOn {
		return errors.New("no such workspace")
	}
	f.calls = append(f.calls, name)
	return nil
}

func (f *fakeCompositor) FirstApps() (map[uint64]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.apps, nil
}

func (f *fakeCompositor) RenameWorkspace(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renames = append(f.renames, from+" -> "+to)
	return nil
}

func (f *fakeCompositor) SetWorkspaceNameByID(id uint64, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adopted = append(f.adopted, name)
	return nil
}

func (f *fakeCompositor) renameCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.renames...)
}

func (f *fakeCompositor) adoptCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.adopted...)
}

func (f *fakeCompositor) focusCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
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

	s := New("test", jrn, &fakeCompositor{m: twoDesks()})
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
	s := New("test", nil, &fakeCompositor{err: errors.New("NIRI_SOCKET is not set")})
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
	s := New("test", nil, &fakeCompositor{err: errors.New("connection refused")})
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()})
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()})
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()})
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()})
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()})
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
	first := New("test", nil, &fakeCompositor{m: twoDesks()})
	path := serve(t, first)

	second := New("test", nil, &fakeCompositor{m: twoDesks()})
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()})
	if err := s.Listen(path); err != nil {
		t.Fatalf("a stale socket stopped the daemon: %v", err)
	}
	defer s.Close()
}

// The switch focuses one workspace per monitor the desk owns, and remembers
// where it came from so desk.last can return.
func TestDeskSwitch(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "vshop.HDMI-A-1.aux", Output: "HDMI-A-1"},
			{Name: "haven.DP-1.db", Output: "DP-1"},
		}, []string{"DP-1", "HDMI-A-1"}),
		focused: "haven.DP-1.db", // we are on haven right now
	}
	s := New("test", jrn, niri)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var plan []desk.Name
	if err := c.Call("desk.switch", &plan, "vshop"); err != nil {
		t.Fatal(err)
	}
	if got := niri.focusCalls(); len(got) != 2 || got[0] != "vshop.DP-1.code" || got[1] != "vshop.HDMI-A-1.aux" {
		t.Errorf("focused %v, want one workspace per monitor", got)
	}
	st := jrn.State()
	if st.LastDesk != "haven" {
		t.Errorf("LastDesk = %q, want the desk we came from", st.LastDesk)
	}
	if st.LastActive["vshop"]["DP-1"] != "code" {
		t.Errorf("did not record where vshop was left: %+v", st.LastActive)
	}
}

// desk.last returns to where the previous switch came from.
func TestDeskLast(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetLastDesk("haven")

	niri := &fakeCompositor{m: twoDesks()}
	s := New("test", jrn, niri)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Call("desk.last", nil); err != nil {
		t.Fatal(err)
	}
	if got := niri.focusCalls(); len(got) != 1 || got[0] != "haven.DP-1.db" {
		t.Errorf("focused %v, want haven's workspace", got)
	}
}

// Switching to a desk with nothing in it is not a switch, and saying so beats
// reporting success while every monitor stayed where it was.
func TestDeskSwitchEmpty(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call("desk.switch", nil, "never-launched")
	if err == nil || !strings.Contains(err.Error(), "no workspaces") {
		t.Errorf("got %v, want a refusal that says why", err)
	}
}

// A switch that fails halfway has already moved some monitors. That is worth
// reporting, not carrying on through.
func TestDeskSwitchStopsOnFailure(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "vshop.HDMI-A-1.aux", Output: "HDMI-A-1"},
		}, []string{"DP-1", "HDMI-A-1"}),
		failOn: "vshop.DP-1.code",
	}
	s := New("test", nil, niri)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.switch", nil, "vshop"); err == nil {
		t.Fatal("a failed focus was reported as a switch")
	}
	if got := niri.focusCalls(); len(got) != 0 {
		t.Errorf("kept going after a failure: %v", got)
	}
}

func TestDeskSwitchNeedsAName(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.switch", nil); err == nil {
		t.Error("desk.switch with no name was accepted")
	}
}

// Reconcile is what keeps the naming model true: it corrects the names a
// monitor move left lying, and claims what is nobody's into the active desk.
func TestReconcile(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetActive(mustName(t, "vshop.DP-1.code"))

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.DP-1.code", Output: "HDMI-A-1"}, // moved
			{ID: 2, Name: "", Output: "DP-1"},                    // niri's own
		}, []string{"DP-1", "HDMI-A-1"}),
		focused: "vshop.DP-1.code",
		apps:    map[uint64]string{2: "org.mozilla.firefox"},
	}
	s := New("test", jrn, niri)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var r Reconciled
	if err := c.Call("desk.reconcile", &r); err != nil {
		t.Fatal(err)
	}
	if got := niri.renameCalls(); len(got) != 1 || got[0] != "vshop.DP-1.code -> vshop.HDMI-A-1.code" {
		t.Errorf("renames = %v, want the moved workspace corrected", got)
	}
	if got := niri.adoptCalls(); len(got) != 1 || got[0] != "vshop.DP-1.firefox" {
		t.Errorf("adopted = %v, want the workspace named after its first app", got)
	}
	// The journal follows the rename, or it points at a workspace that is gone.
	if st := jrn.State(); st.LastActive["vshop"]["HDMI-A-1"] != "code" {
		t.Errorf("journal did not follow the rename: %+v", st.LastActive)
	}
}

// With nothing focused there is no active desk, and adopting into a guess puts
// windows on a desk the user never chose.
func TestReconcileWithoutActiveDeskDoesNotAdopt(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "", // nothing focused, or focused on a foreign workspace
		apps:    map[uint64]string{1: "firefox"},
	}
	s := New("test", nil, niri)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var r Reconciled
	if err := c.Call("desk.reconcile", &r); err != nil {
		t.Fatal(err)
	}
	if got := niri.adoptCalls(); len(got) != 0 {
		t.Errorf("adopted %v with no active desk", got)
	}
}

// A tidy world reports nothing to do, rather than inventing work.
func TestReconcileQuietWhenNothingIsWrong(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}
	s := New("test", nil, niri)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var r Reconciled
	if err := c.Call("desk.reconcile", &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Renamed) != 0 || len(r.Adopted) != 0 || len(r.Conflict) != 0 {
		t.Errorf("reconcile invented work: %+v", r)
	}
}

func mustName(t *testing.T, s string) desk.Name {
	t.Helper()
	n, err := desk.ParseName(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
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
