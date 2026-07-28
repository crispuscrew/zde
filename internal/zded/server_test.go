package zded

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

type fakeCompositor struct {
	m       *desk.Map
	err     error
	focused string
	apps    map[uint64]string
	empty   map[string][]uint64 // output -> empty workspace ids

	output        string // the monitor the focused workspace is on
	followFocus   bool   // move the focus with a carried window, as niri can
	focusedWindow uint64
	// nextInStack is what a vertical window move lands on, 0 for the end of
	// the stack - niri's own answer to whether there is a window that way.
	nextInStack uint64

	mu      sync.Mutex
	calls   []string         // what was asked to be focused, in order
	base    []desk.Workspace // what exists before any naming
	named   []desk.Workspace
	nextID  uint64
	reads   int
	renames []string
	adopted []string
	moves   []bool   // vertical window moves asked for, down is true
	carried []string // workspaces the focused window was moved to
	failOn  string
}

func (f *fakeCompositor) DeskMap() (*desk.Map, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.m, nil
}

func (f *fakeCompositor) mapReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
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

func (f *fakeCompositor) FocusedWindow() (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	return f.focusedWindow, nil
}

// FocusWindowVertically behaves like niri: it moves focus along the stack, and
// at the end of one it does nothing at all.
func (f *fakeCompositor) FocusWindowVertically(down bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.moves = append(f.moves, down)
	if f.nextInStack != 0 {
		f.focusedWindow = f.nextInStack
	}
	return nil
}

func (f *fakeCompositor) FocusedOutput() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.output, nil
}

func (f *fakeCompositor) MoveWindowToWorkspace(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.carried = append(f.carried, name)
	if f.followFocus {
		f.focused = name
	}
	return nil
}

func (f *fakeCompositor) carryCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.carried...)
}

func (f *fakeCompositor) windowMoves() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.moves...)
}

func (f *fakeCompositor) EmptyByOutput() (map[string][]uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := map[string][]uint64{}
	for k, v := range f.empty {
		out[k] = append([]uint64(nil), v...)
	}
	return out, nil
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
	// Behave like niri: the workspace is named, and a fresh empty one appears
	// at the end of that strip.
	for output, ids := range f.empty {
		for i, got := range ids {
			if got != id {
				continue
			}
			f.named = append(f.named, desk.Workspace{ID: id, Name: name, Output: output})
			f.nextID++
			f.empty[output] = append(append([]uint64{}, ids[:i]...), ids[i+1:]...)
			f.empty[output] = append(f.empty[output], f.nextID)
			f.remap()
			return nil
		}
	}
	return nil
}

// remap rebuilds what DeskMap returns, so a test sees the effect of naming.
func (f *fakeCompositor) remap() {
	all := append([]desk.Workspace(nil), f.base...)
	all = append(all, f.named...)
	var outputs []string
	for o := range f.empty {
		outputs = append(outputs, o)
		for _, id := range f.empty[o] {
			all = append(all, desk.Workspace{ID: id, Output: o})
		}
	}
	f.m = desk.Rebuild(all, outputs)
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

	s := New("test", jrn, &fakeCompositor{m: twoDesks()}, nil)
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
	s := New("test", nil, &fakeCompositor{err: errors.New("NIRI_SOCKET is not set")}, nil)
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
	s := New("test", nil, &fakeCompositor{err: errors.New("connection refused")}, nil)
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
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
	first := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	path := serve(t, first)

	second := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
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
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Decoded the way the CLI decodes it: a switch that worked but could not
	// be read back is a switch the user is told failed.
	var plan []string
	if err := c.Call("desk.switch", &plan, "vshop"); err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || plan[0] != "vshop.DP-1.code" {
		t.Errorf("desk.switch returned %v, want the workspace names it focused", plan)
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
	s := New("test", jrn, niri, nil)
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
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
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
	s := New("test", nil, niri, nil)
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

// Down is down: inside a stack it is the window below, and the desk is not
// disturbed by a key that never left the workspace.
func TestNavDownTakesTheWindowBelowFirst(t *testing.T) {
	niri := &fakeCompositor{
		m:             twoDesks(),
		focused:       "haven.DP-1.db",
		focusedWindow: 1,
		nextInStack:   2, // there is a window below
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("nav.down", &focused); err != nil {
		t.Fatal(err)
	}
	if len(focused) != 0 {
		t.Errorf("nav.down answered %v, want nothing: the desk did not change", focused)
	}
	if got := niri.windowMoves(); !slices.Equal(got, []bool{true}) {
		t.Errorf("window moves = %v, want one downward", got)
	}
	if got := niri.focusCalls(); len(got) != 0 {
		t.Errorf("rotated to %v with a window still below", got)
	}
}

// The end of the stack is where the axis stops being about windows. niri
// having moved nothing is the whole signal - zde does not model the layout to
// work it out.
func TestNavDownRotatesAtTheEndOfTheStack(t *testing.T) {
	niri := &fakeCompositor{
		m:             twoDesks(),
		focused:       "haven.DP-1.db",
		focusedWindow: 1,
		nextInStack:   0, // nothing below: niri moves nothing
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("nav.down", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.code"}) {
		t.Errorf("nav.down left %v focused, want the desk after haven", focused)
	}
}

// Up is the mirror, and rotates backwards rather than forwards.
func TestNavUpRotatesBackwards(t *testing.T) {
	niri := &fakeCompositor{
		m:             twoDesks(),
		focused:       "vshop.DP-1.code",
		focusedWindow: 1,
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("nav.up", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"haven.DP-1.db"}) {
		t.Errorf("nav.up left %v focused, want the desk before vshop", focused)
	}
	if got := niri.windowMoves(); !slices.Equal(got, []bool{false}) {
		t.Errorf("window moves = %v, want one upward", got)
	}
}

// An empty workspace has no stack to walk, so the axis is about desks from the
// first keypress. Nothing focused before and nothing after is not "focus
// moved".
func TestNavOnAnEmptyWorkspaceRotates(t *testing.T) {
	niri := &fakeCompositor{
		m:             twoDesks(),
		focused:       "haven.DP-1.db",
		focusedWindow: 0, // nothing is focused
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("nav.down", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.code"}) {
		t.Errorf("nav.down left %v focused, want the next desk", focused)
	}
}

// twoBands is one desk with a band of three on one screen, and another desk's
// workspace sitting right next to it in niri's strip - which is what a scroll
// must not wander into.
func twoBands() *desk.Map {
	return desk.Rebuild([]desk.Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "vshop.DP-1.logs", Output: "DP-1"},
		{Name: "vshop.DP-1.notes", Output: "DP-1"},
		{Name: "haven.DP-1.db", Output: "DP-1"},
	}, []string{"DP-1"})
}

func TestWorkspaceNextWalksTheBand(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()

	niri := &fakeCompositor{m: twoBands(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("workspace.next", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.logs"}) {
		t.Errorf("next went to %v, want the next of the band", focused)
	}
	if got := niri.focusCalls(); !slices.Equal(got, []string{"vshop.DP-1.logs"}) {
		t.Errorf("focused %v", got)
	}
	// Invariant 5: the desk remembers where it was left.
	if got := jrn.State().LastActive["vshop"]["DP-1"]; got != "logs" {
		t.Errorf("last active = %q, want where the scroll left the desk", got)
	}
}

// The end of the band is the end of the scroll. haven.DP-1.db is right there
// in niri's strip, and belongs to a desk you did not ask to leave.
func TestWorkspaceNextStopsAtTheBandEnd(t *testing.T) {
	niri := &fakeCompositor{m: twoBands(), focused: "vshop.DP-1.notes", output: "DP-1"}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("workspace.next", &focused); err != nil {
		t.Fatal(err)
	}
	if len(focused) != 0 {
		t.Errorf("answered %v, want nothing: the band ends there", focused)
	}
	if got := niri.focusCalls(); len(got) != 0 {
		t.Errorf("focused %v, which is another desk's workspace", got)
	}
}

// On a workspace nothing has named, the desk is the journal's answer and the
// near end of its band is the way back in.
func TestWorkspaceNextFromAWorkspaceWithNoName(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetOnDesk("vshop")

	niri := &fakeCompositor{m: twoBands(), focused: "", output: "DP-1"}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("workspace.prev", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.notes"}) {
		t.Errorf("prev went to %v, want the last of the band", focused)
	}
}

// A band on another screen is not this one. Scrolling walks the screen you are
// looking at, because the strip does (docs/model.md, section 1).
func TestWorkspaceNextStaysOnItsScreen(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "vshop.HDMI-A-1.aux", Output: "HDMI-A-1"},
		}, []string{"DP-1", "HDMI-A-1"}),
		focused: "vshop.DP-1.code",
		output:  "DP-1",
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("workspace.next", &focused); err != nil {
		t.Fatal(err)
	}
	if len(focused) != 0 {
		t.Errorf("answered %v, want nothing: DP-1's band is one workspace long", focused)
	}
}

// Undocking parks a desk's workspaces on the screen that is left, with home
// still in their names so they can go back. Scrolling has to follow them
// there, or these keys go dead at exactly the moment somebody unplugs a
// monitor - silently, while every other desk verb keeps working.
func TestWorkspaceNextAfterAMonitorGoesAway(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Idx: 1, Name: "vshop.DP-1.code", Output: "eDP-1"},
			{Idx: 2, Name: "vshop.DP-1.notes", Output: "eDP-1"},
		}, []string{"eDP-1"}), // DP-1 is gone; both are parked on the laptop
		focused: "vshop.DP-1.code",
		output:  "eDP-1",
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("workspace.next", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.notes"}) {
		t.Errorf("next went to %v, want the band where the workspaces actually are", focused)
	}
}

// A scroll niri refused is a failure, not a move. Writing it down would leave
// the desk remembering a workspace it was never on.
func TestWorkspaceNextWhenNiriRefuses(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()

	niri := &fakeCompositor{
		m:       twoBands(),
		focused: "vshop.DP-1.code",
		output:  "DP-1",
		failOn:  "vshop.DP-1.logs",
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("workspace.next", nil); err == nil {
		t.Fatal("a refused scroll was reported as a move")
	}
	if got := jrn.State().LastActive["vshop"]["DP-1"]; got != "" {
		t.Errorf("last active = %q, want nothing written for a move that did not happen", got)
	}
}

// Nothing here belongs to a desk yet, so there is no band to be inside of.
func TestWorkspaceNextWithNoDesk(t *testing.T) {
	niri := &fakeCompositor{
		m:       desk.Rebuild([]desk.Workspace{{ID: 1, Idx: 1, Output: "DP-1"}}, []string{"DP-1"}),
		focused: "",
		output:  "DP-1",
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call("workspace.next", nil)
	if err == nil || !strings.Contains(err.Error(), "no desk to scroll inside") {
		t.Errorf("got %v, want a refusal that says why there is nowhere to go", err)
	}
}

// The desk is real and this screen has none of it. That is not the quiet end
// of a band, it is nothing to walk at all, and saying so beats a key that
// looks broken.
func TestWorkspaceNextWithNothingOnThisScreen(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetOnDesk("vshop")

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Idx: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
		}, []string{"DP-1", "HDMI-A-1"}),
		focused: "", // an unnamed workspace, on the other screen
		output:  "HDMI-A-1",
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call("workspace.next", nil)
	if err == nil || !strings.Contains(err.Error(), "on this screen") {
		t.Errorf("got %v, want a refusal about this screen", err)
	}
}

// The regulars are reachable from any desk by their own key, and coming back
// is what makes reaching for them cheap: desk.last has to know where you were.
func TestDeskRegularsGoesThereAndRemembers(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "regulars.DP-1.comms", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "vshop.DP-1.code",
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.regulars", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"regulars.DP-1.comms"}) {
		t.Errorf("regulars left %v focused, want the band", focused)
	}
	if got := jrn.State().LastDesk; got != "vshop" {
		t.Errorf("last desk %q, want the desk reached from", got)
	}
}

// A band nobody has declared is not a broken command. The answer says how to
// have one rather than reading like a desk that went missing.
func TestDeskRegularsWithNoneYet(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call("desk.regulars", nil)
	if err == nil || !strings.Contains(err.Error(), "no regulars yet") {
		t.Errorf("got %v, want an answer that says how to have regulars", err)
	}
}

// brokenDesks is a desks directory that cannot be read at all, which is what
// one unparseable file in it amounts to.
type brokenDesks struct{}

func (brokenDesks) All() (map[string]*manifest.Desk, error) {
	return nil, errors.New("reading manifests: vshop.yaml: field monitorz not found")
}
func (brokenDesks) Save(*manifest.Desk) (string, error) { return "", errors.New("not writable") }

// Every way of failing that is not "there is no band" has to keep its own
// words. Told they have no regulars, someone with an unreadable manifest or a
// compositor that has gone away would go looking in the wrong place - and the
// thing that actually broke would never be mentioned.
func TestDeskRegularsDoesNotSpeakForOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *Server
		want string
	}{
		{
			name: "a manifest that will not parse",
			s:    New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, brokenDesks{}),
			want: "monitorz",
		},
		{
			name: "a compositor that cannot be read",
			s:    New("test", nil, &fakeCompositor{err: errors.New("NIRI_SOCKET is not set")}, nil),
			want: "NIRI_SOCKET",
		},
		{
			name: "a focus that failed partway",
			s: New("test", nil, &fakeCompositor{
				m: desk.Rebuild([]desk.Workspace{
					{Name: "regulars.DP-1.comms", Output: "DP-1"},
				}, []string{"DP-1"}),
				focused: "regulars.DP-1.comms",
				failOn:  "regulars.DP-1.comms",
			}, nil),
			want: "no such workspace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := DialPath(serve(t, tc.s))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			err = c.Call("desk.regulars", nil)
			if err == nil {
				t.Fatal("answered as though nothing was wrong")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want the one that actually happened (%s)", err, tc.want)
			}
		})
	}
}

func TestDeskRegularsTakesNoArguments(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.regulars", nil, "comms"); err == nil {
		t.Error("desk.regulars with an argument was accepted")
	}
}

// The regulars cannot be written down, and saying so in words about a manifest
// nobody asked for is not saying so.
func TestDeskSnapshotOfTheRegulars(t *testing.T) {
	s := New("test", nil, &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "regulars.DP-1.comms", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "regulars.DP-1.comms",
	}, manifest.Dir(t.TempDir()))
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call("desk.snapshot", nil)
	if err == nil || !strings.Contains(err.Error(), "not a desk") {
		t.Errorf("got %v, want a refusal that says what the regulars are", err)
	}
}

// twoScreens is two desks that both own workspaces on both monitors, which is
// what makes "the window stays on its screen" a claim that can fail.
func twoScreens() *desk.Map {
	return desk.Rebuild([]desk.Workspace{
		{Name: "haven.DP-1.db", Output: "DP-1"},
		{Name: "haven.HDMI-A-1.logs", Output: "HDMI-A-1"},
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "vshop.HDMI-A-1.aux", Output: "HDMI-A-1"},
	}, []string{"DP-1", "HDMI-A-1"})
}

// Shift on the axis brings the window along: it lands on the desk beside this
// one, on the screen it was already on, and focus follows it there.
func TestMoveWindowCarriesItAndFollows(t *testing.T) {
	niri := &fakeCompositor{
		m:             twoScreens(),
		focused:       "haven.HDMI-A-1.logs",
		output:        "HDMI-A-1",
		focusedWindow: 7,
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.move-window", &focused, "next"); err != nil {
		t.Fatal(err)
	}
	if got := niri.carryCalls(); !slices.Equal(got, []string{"vshop.HDMI-A-1.aux"}) {
		t.Errorf("carried the window to %v, want vshop's band on the screen it was on", got)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.code", "vshop.HDMI-A-1.aux"}) {
		t.Errorf("left %v focused, want the whole desk it followed the window to", focused)
	}
}

// The window has to land where the switch is about to look. A desk being
// entered for the first time takes its order from the manifest, not from the
// alphabet, so a carry that consulted only the journal would put the window
// one workspace away from the person who carried it - on the right desk, and
// nowhere they can see.
func TestMoveWindowLandsWhereTheSwitchLooks(t *testing.T) {
	m, err := manifest.Parse([]byte("name: vshop\nmonitors:\n  DP-1: { workspaces: [notes, code] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "haven.DP-1.db", Output: "DP-1"},
			{Name: "vshop.DP-1.code", Output: "DP-1"},  // first alphabetically
			{Name: "vshop.DP-1.notes", Output: "DP-1"}, // first in the manifest
		}, []string{"DP-1"}),
		focused:       "haven.DP-1.db",
		output:        "DP-1",
		focusedWindow: 7,
	}
	s := New("test", nil, niri, fixedDesks{"vshop": m})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.move-window", &focused, "next"); err != nil {
		t.Fatal(err)
	}
	carried := niri.carryCalls()
	if !slices.Equal(carried, []string{"vshop.DP-1.notes"}) {
		t.Errorf("carried the window to %v, want the workspace the manifest enters on", carried)
	}
	if !slices.Equal(focused, carried) {
		t.Errorf("the window went to %v and the switch went to %v", carried, focused)
	}
}

// A desk is not a monitor, but a desk that owns nothing on this screen cannot
// take the window there. Somewhere it owns beats nowhere.
func TestMoveWindowToADeskNotOnThisScreen(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "haven.HDMI-A-1.logs", Output: "HDMI-A-1"},
			{Name: "vshop.DP-1.code", Output: "DP-1"},
		}, []string{"DP-1", "HDMI-A-1"}),
		focused:       "haven.HDMI-A-1.logs",
		output:        "HDMI-A-1",
		focusedWindow: 7,
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.move-window", nil, "next"); err != nil {
		t.Fatal(err)
	}
	if got := niri.carryCalls(); !slices.Equal(got, []string{"vshop.DP-1.code"}) {
		t.Errorf("carried the window to %v, want the only workspace vshop has", got)
	}
}

// Nothing focused is not a refusal: there is no window to bring, so the key
// means what it does without one.
func TestMoveWindowWithNothingToCarry(t *testing.T) {
	niri := &fakeCompositor{
		m:             twoScreens(),
		focused:       "haven.DP-1.db",
		output:        "DP-1",
		focusedWindow: 0,
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.move-window", &focused, "next"); err != nil {
		t.Fatal(err)
	}
	if got := niri.carryCalls(); len(got) != 0 {
		t.Errorf("moved %v with no window focused", got)
	}
	if len(focused) == 0 {
		t.Error("did not switch desks, which is what the key still means")
	}
}

// Which way it carries the window, which two desks cannot show: with a
// rotation of two, next and prev arrive at the same place. From the middle of
// three they do not.
func TestMoveWindowGoesTheWayItWasAsked(t *testing.T) {
	for _, tc := range []struct{ direction, want string }{
		{"next", "vshop.DP-1.code"},
		{"prev", "haven.DP-1.db"},
	} {
		niri := &fakeCompositor{
			m: desk.Rebuild([]desk.Workspace{
				{Name: "haven.DP-1.db", Output: "DP-1"},
				{Name: "mid.DP-1.notes", Output: "DP-1"},
				{Name: "vshop.DP-1.code", Output: "DP-1"},
			}, []string{"DP-1"}),
			focused:       "mid.DP-1.notes",
			output:        "DP-1",
			focusedWindow: 7,
		}
		s := New("test", nil, niri, nil)
		c, err := DialPath(serve(t, s))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Call("desk.move-window", nil, tc.direction); err != nil {
			c.Close()
			t.Fatal(err)
		}
		c.Close()
		if got := niri.carryCalls(); !slices.Equal(got, []string{tc.want}) {
			t.Errorf("move-window %s carried it to %v, want %s", tc.direction, got, tc.want)
		}
	}
}

// niri is asked not to follow the window, and desk.last is what would quietly
// break if it ever did: the switch would read the desk it had already been
// taken to as the desk it was leaving, and Mod+Shift+Tab would bring you back
// to where you already are. The fake follows focus here to prove the answer
// does not depend on it.
func TestMoveWindowRemembersWhereItCameFrom(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()

	niri := &fakeCompositor{
		m:             twoScreens(),
		focused:       "haven.DP-1.db",
		output:        "DP-1",
		focusedWindow: 7,
		followFocus:   true, // as niri would with focus:true
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.move-window", nil, "next"); err != nil {
		t.Fatal(err)
	}
	if got := jrn.State().LastDesk; got != "haven" {
		t.Errorf("last desk %q, want the desk the window was carried out of", got)
	}
}

// Named rather than counted to: how a window reaches a desk that is not
// beside this one, and the only way one gets into the regulars, which no
// rotation ever steps onto.
func TestMoveWindowToADeskByName(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "haven.DP-1.db", Output: "DP-1"},
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "regulars.DP-1.comms", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused:       "vshop.DP-1.code",
		output:        "DP-1",
		focusedWindow: 7,
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.move-window-to", &focused, "regulars"); err != nil {
		t.Fatal(err)
	}
	if got := niri.carryCalls(); !slices.Equal(got, []string{"regulars.DP-1.comms"}) {
		t.Errorf("carried the window to %v, want the regulars band", got)
	}
	if !slices.Equal(focused, []string{"regulars.DP-1.comms"}) {
		t.Errorf("left %v focused, want the band it followed the window to", focused)
	}
}

// The desk you are already on is not a journey. Carrying a window to it would
// move it off the workspace it is on, to wherever that desk is entered.
func TestMoveWindowToTheDeskYouAreOn(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "vshop.DP-1.notes", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused:       "vshop.DP-1.notes",
		output:        "DP-1",
		focusedWindow: 7,
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.move-window-to", &focused, "vshop"); err != nil {
		t.Fatal(err)
	}
	if got := niri.carryCalls(); len(got) != 0 {
		t.Errorf("carried the window to %v while already on that desk", got)
	}
	if len(focused) != 0 {
		t.Errorf("answered %v, want nothing: nothing moved", focused)
	}
}

// A typo is not a desk that is missing, and the two want different things
// next: one wants spelling, the other wants making.
func TestMoveWindowToAnImpossibleName(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1", focusedWindow: 7}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, name := range []string{"VShop", "has space", "with.dots", ""} {
		err := c.Call("desk.move-window-to", nil, name)
		if err == nil || !strings.Contains(err.Error(), "not a desk name") {
			t.Errorf("move-window-to %q: got %v, want a refusal about the name", name, err)
		}
	}
	if got := niri.carryCalls(); len(got) != 0 {
		t.Errorf("moved the window to %v on a name that cannot be a desk", got)
	}
}

// Carrying into a band that is not there is the same dead end as reaching for
// it, so it gets the same way out. The generic refusal offers a manifest, and
// the regulars are the one band a manifest cannot declare.
func TestMoveWindowToTheRegularsWhenThereAreNone(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1", focusedWindow: 7}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call("desk.move-window-to", nil, "regulars")
	if err == nil || !strings.Contains(err.Error(), "no regulars yet") {
		t.Errorf("got %v, want the answer that says how to have a band", err)
	}
	if got := niri.carryCalls(); len(got) != 0 {
		t.Errorf("moved the window to %v with no band to put it in", got)
	}
}

// A desk that could exist but does not keeps the answer about the desk, not
// about the name.
func TestMoveWindowToADeskThatIsNotThere(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1", focusedWindow: 7}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call("desk.move-window-to", nil, "nowhere")
	if err == nil || !strings.Contains(err.Error(), "no workspaces") {
		t.Errorf("got %v, want an answer about the desk", err)
	}
	if got := niri.carryCalls(); len(got) != 0 {
		t.Errorf("moved the window to %v before finding out there was nowhere to put it", got)
	}
}

func TestMoveWindowNeedsADirection(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoScreens(), focused: "haven.DP-1.db"}, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, args := range [][]string{{}, {"sideways"}, {"next", "prev"}} {
		if err := c.Call("desk.move-window", nil, args...); err == nil {
			t.Errorf("desk.move-window %v was accepted", args)
		}
	}
}

// Down and up have to be opposites, and a rotation of two cannot show it: with
// two desks the step lands on the other one whichever way it goes. From the
// middle of three they part company.
func TestNavDirectionsAreOpposites(t *testing.T) {
	threeDesks := func() *desk.Map {
		return desk.Rebuild([]desk.Workspace{
			{Name: "haven.DP-1.db", Output: "DP-1"},
			{Name: "mid.DP-1.notes", Output: "DP-1"},
			{Name: "vshop.DP-1.code", Output: "DP-1"},
		}, []string{"DP-1"})
	}
	for _, tc := range []struct{ method, want string }{
		{"nav.down", "vshop.DP-1.code"},
		{"nav.up", "haven.DP-1.db"},
	} {
		niri := &fakeCompositor{
			m:             threeDesks(),
			focused:       "mid.DP-1.notes",
			focusedWindow: 1, // the end of its stack, so the desk turns
		}
		s := New("test", nil, niri, nil)
		c, err := DialPath(serve(t, s))
		if err != nil {
			t.Fatal(err)
		}
		var focused []string
		if err := c.Call(tc.method, &focused); err != nil {
			c.Close()
			t.Fatal(err)
		}
		c.Close()
		if !slices.Equal(focused, []string{tc.want}) {
			t.Errorf("%s from the middle desk left %v focused, want %s", tc.method, focused, tc.want)
		}
	}
}

// Rotating is a desk-level action, so it arrives the way a switch does: the
// next desk comes up on every monitor it owns, and says which workspaces it
// left focused.
func TestDeskNextGoesToTheDeskBeside(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.db"}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.next", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.code"}) {
		t.Errorf("next left %v focused, want the desk after haven", focused)
	}
}

// The rotation is a loop: the end joins the beginning rather than stopping.
func TestDeskPrevWraps(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.db"}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.prev", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.code"}) {
		t.Errorf("prev from the first desk left %v focused, want the last", focused)
	}
}

// The regulars are reachable from every desk by their own action and belong to
// no band, so rotating steps over them rather than into them.
func TestDeskRotationStepsOverTheRegulars(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "haven.DP-1.db", Output: "DP-1"},
			{Name: "regulars.DP-1.comms", Output: "DP-1"},
		}, []string{"DP-1"}),
		// From the first desk, so the step lands on what comes next rather
		// than wrapping - wrapping from the last desk reaches the first either
		// way, and would pass with the regulars still in the rotation.
		focused: "haven.DP-1.db",
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.next", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.code"}) {
		t.Errorf("next went to %v, want the desk after haven rather than the regulars", focused)
	}
}

// On a workspace nothing has named, where you are is the journal's to answer -
// the same answer adoption spends, so rotating from a fresh workspace goes
// where the desk you are on says, not back to the beginning.
func TestDeskNextFromAWorkspaceWithNoName(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	// The first desk, so that using the journal and ignoring it give different
	// answers: from haven the step is vshop, while from nowhere it is haven.
	jrn.SetOnDesk("haven")

	niri := &fakeCompositor{m: twoDesks(), focused: ""}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.next", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.code"}) {
		t.Errorf("next left %v focused, want the desk after the one the journal is on", focused)
	}
}

// A rotation of one has no desk beside it, and switching to the desk you are
// already on is not the no-op it looks like: a switch restores that desk's
// last-active workspace, so pressing next on a single-desk session would
// scroll you off the workspace you were using and then remember the wrong one.
func TestDeskNextWithOneDeskStaysPut(t *testing.T) {
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "vshop.DP-1.notes", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "vshop.DP-1.notes", // not the first of the band
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.next", &focused); err != nil {
		t.Fatal(err)
	}
	if len(focused) != 0 {
		t.Errorf("next answered %v, want nothing: there is no desk beside this one", focused)
	}
	if got := niri.focusCalls(); len(got) != 0 {
		t.Errorf("focused %v, which would have scrolled off the workspace in use", got)
	}
}

// The socket is a wire anything can write to, so a verb that takes nothing
// says so rather than quietly ignoring what it was handed.
func TestDeskRotationTakesNoArguments(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.db"}, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, method := range []string{"desk.next", "desk.prev"} {
		err := c.Call(method, nil, "nonsense")
		if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
			t.Errorf("%s with an argument: got %v, want a refusal", method, err)
		}
	}
}

// Nothing named yet is not a rotation of nothing to say about: it is a reason
// to say so, rather than to focus a desk that does not exist.
func TestDeskNextWithNothingToRotateThrough(t *testing.T) {
	niri := &fakeCompositor{
		m:       desk.Rebuild([]desk.Workspace{{Name: "", Output: "DP-1"}}, []string{"DP-1"}),
		focused: "",
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call("desk.next", nil)
	if err == nil || !strings.Contains(err.Error(), "no desks") {
		t.Errorf("got %v, want a refusal that says there is nothing to rotate through", err)
	}
	if got := niri.focusCalls(); len(got) != 0 {
		t.Errorf("focused %v with no desks in the map", got)
	}
}

// A switch that failed is not a desk you are on. The journal answer is what
// adoption spends on a workspace with no name of its own, and what desk.last
// goes back to, so recording a desk that was never reached files the next
// window into it and sends desk.last somewhere the user has never been.
func TestDeskSwitchThatFailsLeavesTheDeskYouAreOnAlone(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetOnDesk("vshop")

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
			{ID: 2, Name: "haven.DP-1.db", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "", // on a fresh workspace nothing has named
		failOn:  "haven.DP-1.db",
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.switch", nil, "haven"); err == nil {
		t.Fatal("a failed focus was reported as a switch")
	}
	if got := jrn.State().OnDesk; got != "vshop" {
		t.Errorf("on desk %q after a switch that failed, want vshop", got)
	}
	if got := jrn.State().LastDesk; got != "" {
		t.Errorf("last desk %q after a switch that never happened", got)
	}
}

func TestDeskSwitchNeedsAName(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
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
	s := New("test", jrn, niri, nil)
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
	s := New("test", nil, niri, nil)
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
	s := New("test", nil, niri, nil)
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

// A manifest is something you can ask for: switching to a desk that has never
// been launched creates what it declares instead of refusing.
func TestSwitchCreatesDeclaredWorkspaces(t *testing.T) {
	d, err := manifest.Parse([]byte("name: vshop\nmonitors: { DP-1: { workspaces: [code, agent] } }"))
	if err != nil {
		t.Fatal(err)
	}
	niri := &fakeCompositor{
		base:   []desk.Workspace{{ID: 1, Name: "haven.DP-1.db", Output: "DP-1"}},
		empty:  map[string][]uint64{"DP-1": {10}}, // niri keeps one per strip
		nextID: 10,
	}
	niri.remap()
	s := New("test", nil, niri, fixedDesks{"vshop": d})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var plan []string
	if err := c.Call("desk.switch", &plan, "vshop"); err != nil {
		t.Fatal(err)
	}
	// Both declared workspaces made, one per pass, because niri only offers
	// one empty workspace at a time.
	if got := niri.adoptCalls(); len(got) != 2 || got[0] != "vshop.DP-1.code" || got[1] != "vshop.DP-1.agent" {
		t.Errorf("created %v, want both declared workspaces", got)
	}
	if got := niri.focusCalls(); len(got) != 1 || got[0] != "vshop.DP-1.code" {
		t.Errorf("focused %v, want the first of the new band", got)
	}
}

// A window carried to a desk that exists only as a manifest has to wait for
// that manifest to become workspaces. Carry first and there is nowhere to put
// it - or worse, somewhere stale, while the switch that follows goes to what
// was just created and leaves the window behind on a workspace nobody is
// looking at.
func TestMoveWindowToADeskThatIsOnlyAManifest(t *testing.T) {
	d, err := manifest.Parse([]byte("name: vshop\nmonitors: { DP-1: { workspaces: [code] } }"))
	if err != nil {
		t.Fatal(err)
	}
	niri := &fakeCompositor{
		base:          []desk.Workspace{{ID: 1, Name: "haven.DP-1.db", Output: "DP-1"}},
		empty:         map[string][]uint64{"DP-1": {10}},
		nextID:        10,
		focused:       "haven.DP-1.db",
		output:        "DP-1",
		focusedWindow: 7,
	}
	niri.remap()
	s := New("test", nil, niri, fixedDesks{"vshop": d})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var focused []string
	if err := c.Call("desk.move-window-to", &focused, "vshop"); err != nil {
		t.Fatal(err)
	}
	if got := niri.adoptCalls(); !slices.Equal(got, []string{"vshop.DP-1.code"}) {
		t.Errorf("created %v, want the declared workspace made before the window needed it", got)
	}
	carried := niri.carryCalls()
	if !slices.Equal(carried, []string{"vshop.DP-1.code"}) {
		t.Errorf("carried the window to %v, want the workspace the manifest declares", carried)
	}
	if !slices.Equal(focused, carried) {
		t.Errorf("the window went to %v and the switch went to %v", carried, focused)
	}
}

// What already exists is not made again.
func TestSwitchDoesNotRecreateWhatExists(t *testing.T) {
	d, err := manifest.Parse([]byte("name: vshop\nmonitors: { DP-1: { workspaces: [code] } }"))
	if err != nil {
		t.Fatal(err)
	}
	niri := &fakeCompositor{
		base:   []desk.Workspace{{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"}},
		empty:  map[string][]uint64{"DP-1": {10}},
		nextID: 10,
	}
	niri.remap()
	s := New("test", nil, niri, fixedDesks{"vshop": d})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.switch", nil, "vshop"); err != nil {
		t.Fatal(err)
	}
	if got := niri.adoptCalls(); len(got) != 0 {
		t.Errorf("created %v, want nothing: it is already there", got)
	}
}

// fixedDesks is a manifest set that cannot be written to, for the tests that
// only read.
type fixedDesks map[string]*manifest.Desk

func (f fixedDesks) All() (map[string]*manifest.Desk, error) { return f, nil }
func (f fixedDesks) Save(*manifest.Desk) (string, error) {
	return "", errors.New("not writable")
}

// A snapshot of the desk you are on writes what is there, and what it writes
// has to be a manifest that switching can use.
func TestDeskSnapshot(t *testing.T) {
	dir := manifest.Dir(t.TempDir())
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
			{ID: 2, Name: "vshop.HDMI-A-1.aux", Output: "HDMI-A-1"},
		}, []string{"DP-1", "HDMI-A-1"}),
		focused: "vshop.DP-1.code",
	}
	s := New("test", nil, niri, dir)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var path string
	if err := c.Call("desk.snapshot", &path); err != nil {
		t.Fatal(err)
	}
	all, err := dir.All()
	if err != nil {
		t.Fatal(err)
	}
	d, ok := all["vshop"]
	if !ok {
		t.Fatalf("snapshot wrote %s, which does not load as a desk", path)
	}
	if len(d.Workspaces()) != 2 {
		t.Errorf("snapshot recorded %v", d.Workspaces())
	}
}

// With nothing focused there is no desk you are on, and guessing which one to
// write down would write the wrong one.
func TestDeskSnapshotWithoutFocus(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: ""}
	s := New("test", nil, niri, manifest.Dir(t.TempDir()))
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.snapshot", nil); err == nil {
		t.Error("snapshot guessed a desk with nothing focused")
	}
}

// The case adoption exists for: a window opened past the end of the strip
// makes a fresh workspace, focus follows it there, and that workspace is
// unnamed precisely because nothing has claimed it. If a name were required to
// decide who claims it, adoption could never claim anything.
func TestReconcileAdoptsWhenFocusHasNoName(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetOnDesk("vshop") // where a desk switch left us

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
			{ID: 3, Name: "haven.DP-1.db", Output: "DP-1"},
			{ID: 2, Name: "", Output: "DP-1"}, // the new one, with a window
		}, []string{"DP-1"}),
		focused: "", // focus is on the unnamed workspace
		apps:    map[uint64]string{2: "foot"},
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var r Reconciled
	if err := c.Call("desk.reconcile", &r); err != nil {
		t.Fatal(err)
	}
	if got := niri.adoptCalls(); len(got) != 1 || got[0] != "vshop.DP-1.foot" {
		t.Errorf("adopted %v, want the workspace claimed into the desk we are on", got)
	}
}

// A journal outlives the compositor it was written under. Log out on vshop,
// log back in, and niri starts with nothing named - so the remembered desk
// must not be spent, or the first window of a fresh session is filed into a
// desk that is not there.
func TestReconcileWillNotAdoptIntoADeskThatIsGone(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetOnDesk("vshop") // from the session before this one

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "", Output: "DP-1"}, // a fresh niri: nothing named
		}, []string{"DP-1"}),
		focused: "",
		apps:    map[uint64]string{1: "foot"},
	}
	s := New("test", jrn, niri, nil)
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
		t.Errorf("adopted %v into a desk with no workspaces in it", got)
	}
}

// A workspace somebody else named is not unclaimed, it is theirs. Renaming it
// into a desk is not adoption.
func TestReconcileLeavesAWorkspaceSomebodyElseNamed(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetOnDesk("vshop")

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
			{ID: 2, Name: "notes", Output: "DP-1"}, // the user's own name
		}, []string{"DP-1"}),
		focused: "notes",
		apps:    map[uint64]string{2: "foot"},
	}
	s := New("test", jrn, niri, nil)
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
		t.Errorf("renamed %v, which the user had named themselves", got)
	}
}

// Focus is the better answer whenever it has one, so seeing it refreshes what
// the journal will say when focus goes quiet.
func TestActiveDeskIsRefreshedByFocus(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetOnDesk("vshop") // stale: the user reached haven another way

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
			{ID: 2, Name: "haven.DP-1.db", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "haven.DP-1.db",
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.reconcile", &Reconciled{}); err != nil {
		t.Fatal(err)
	}
	if got := jrn.State().OnDesk; got != "haven" {
		t.Errorf("OnDesk = %q after looking at haven, want it caught up", got)
	}
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

func (f *fakeCompositor) FocusedPlace() (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", "", f.err
	}
	return f.focused, f.output, nil
}

// queueTestServer is a daemon with a journal and two desks, which is what a
// queue needs to be about anything: items belong to desks.
func queueTestServer(t *testing.T, focused string) (*Server, *journal.Journal, *fakeCompositor) {
	t.Helper()
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	niri := &fakeCompositor{m: twoDesks(), focused: focused, output: "DP-1"}
	return New("test", jrn, niri, nil), jrn, niri
}

// What is waiting, and where it waits. The desk is what separates a queue from
// a list.
func TestQueueAddRecordsTheDeskItCameFrom(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var it journal.Item
	if err := c.Call("queue.add", &it, "reply to ilya"); err != nil {
		t.Fatal(err)
	}
	if it.Text != "reply to ilya" || it.Desk != "vshop" || it.ID == 0 {
		t.Errorf("item = %+v, want the text, the desk it was added from, and an id", it)
	}
	if q := jrn.State().Queue; len(q) != 1 || q[0].ID != it.ID {
		t.Errorf("journal queue = %+v, want the item the caller was told about", q)
	}
}

// A compositor that cannot be read is not a reason to lose the reminder. It
// waits nowhere in particular instead.
func TestQueueAddWithoutACompositor(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	s := New("test", jrn, &fakeCompositor{err: errors.New("NIRI_SOCKET is not set")}, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var it journal.Item
	if err := c.Call("queue.add", &it, "pay the invoice"); err != nil {
		t.Fatalf("the reminder was refused because niri was down: %v", err)
	}
	if it.Desk != "" {
		t.Errorf("desk = %q, want none: there was no way to know", it.Desk)
	}
}

// Everything that reads the queue reads it a line at a time.
func TestQueueAddRefusesWhatCannotBePrinted(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, bad := range []string{"", "   ", "two\nlines", "bell\x07", strings.Repeat("x", 301)} {
		if err := c.Call("queue.add", nil, bad); err == nil {
			t.Errorf("queue.add %q was accepted", bad)
		}
	}
}

// Jumping goes to where the oldest thing waits, and leaves it waiting:
// arriving somewhere is not doing the thing.
func TestQueueJumpGoesToTheOldestAndLeavesIt(t *testing.T) {
	s, jrn, niri := queueTestServer(t, "vshop.DP-1.code")
	if _, err := jrn.Queue("first, on haven", "haven"); err != nil {
		t.Fatal(err)
	}
	if _, err := jrn.Queue("second, on vshop", "vshop"); err != nil {
		t.Fatal(err)
	}
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.queue-jump", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"haven.DP-1.db"}) {
		t.Errorf("jumped to %v, want the desk of the oldest thing waiting", focused)
	}
	if got := niri.focusCalls(); len(got) != 1 {
		t.Errorf("focused %v", got)
	}
	if q := jrn.State().Queue; len(q) != 2 {
		t.Errorf("queue = %+v, want both still waiting: jumping is not finishing", q)
	}
}

// An item with no desk cannot be jumped to, but it must not stop the ones that
// can - the reminder taken while niri was down should not wedge the key.
func TestQueueJumpSkipsWhatHasNoDesk(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	jrn.Queue("taken while niri was down", "")
	jrn.Queue("on haven", "haven")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.queue-jump", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"haven.DP-1.db"}) {
		t.Errorf("jumped to %v, want the oldest one that has somewhere to go", focused)
	}
}

func TestQueueJumpWithNothingWaiting(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call("desk.queue-jump", nil)
	if err == nil || !strings.Contains(err.Error(), "nothing is waiting") {
		t.Errorf("got %v, want to be told the queue is empty", err)
	}
}

// The id in the list is the id that finishes it.
func TestQueueDone(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	it, _ := jrn.Queue("reply to ilya", "vshop")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("queue.done", nil, strconv.FormatUint(it.ID, 10)); err != nil {
		t.Fatal(err)
	}
	if q := jrn.State().Queue; len(q) != 0 {
		t.Errorf("queue = %+v, want it gone", q)
	}
	// Not a number is a mistake worth naming; a number nobody is waiting on is
	// not, because it is not waiting either way.
	if err := c.Call("queue.done", nil, "seven-ish"); err == nil {
		t.Error("queue.done accepted something that is not an id")
	}
	if err := c.Call("queue.done", nil, "999"); err != nil {
		t.Errorf("finishing something already finished was an error: %v", err)
	}
}

// The queue outlives the compositor, so a desk it remembers may be gone by the
// next login. One stale reminder must not hold the key down for every reminder
// behind it.
func TestQueueJumpSkipsADeskThatIsGone(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	jrn.Queue("on a desk from last week", "oldproject")
	jrn.Queue("on haven", "haven")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.queue-jump", &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"haven.DP-1.db"}) {
		t.Errorf("jumped to %v, want the oldest one whose desk is still there", focused)
	}
}

// The list is the queue, and the queue is an order: oldest first, the same one
// queue-jump follows. A list that disagreed would send people to the wrong id.
func TestQueueListIsOldestFirst(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	first, _ := jrn.Queue("oldest", "vshop")
	second, _ := jrn.Queue("middle", "haven")
	third, _ := jrn.Queue("newest", "vshop")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var q []journal.Item
	if err := c.Call("queue.list", &q); err != nil {
		t.Fatal(err)
	}
	if len(q) != 3 || q[0].ID != first.ID || q[1].ID != second.ID || q[2].ID != third.ID {
		t.Errorf("list = %+v, want them oldest first", q)
	}
	if q[1].Desk != "haven" {
		t.Errorf("item = %+v, want the desk it was taken on to survive the wire", q[1])
	}
}

// A tab is the CLI's own column separator, so one inside the text prints an
// item with more columns than it has fields.
func TestQueueTextIsOnePrintableLine(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, bad := range []string{"reply\tto ilya", "delete\x7f", "line break"} {
		if err := c.Call("queue.add", nil, bad); err == nil {
			t.Errorf("queue.add %q was accepted", bad)
		}
	}
}

// The bound is a number of characters, and says so. Counting bytes gives a
// reminder written in Cyrillic half the room and tells the writer a number
// twice what they typed.
func TestQueueTextBoundCountsCharacters(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("queue.add", nil, strings.Repeat("я", queueTextMax)); err != nil {
		t.Errorf("a reminder of %d characters was refused: %v", queueTextMax, err)
	}
	if err := c.Call("queue.add", nil, strings.Repeat("я", queueTextMax+1)); err == nil {
		t.Error("one character over the bound was accepted")
	}
}
