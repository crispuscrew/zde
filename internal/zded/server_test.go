package zded

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
	"time"
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
	// windows is what is open, in the order niri happens to list it - which is
	// not the order a picker shows.
	windows []Window
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
	// performed is niri's own actions asked for, by name. The name is the thing
	// worth keeping: it is a string all the way to the compositor, so a typo in
	// one is invisible to Go.
	performed []string
	failOn    string
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

func (f *fakeCompositor) Windows() ([]Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]Window(nil), f.windows...), nil
}

// FocusWindow behaves like niri: focus lands on the window and on the workspace
// it is on, and nothing is moved to get there.
func (f *fakeCompositor) FocusWindow(id uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	for _, w := range f.windows {
		if w.ID != id {
			continue
		}
		f.focusedWindow = id
		f.focused = w.Workspace
		return nil
	}
	return errors.New("no such window")
}

func (f *fakeCompositor) focusedWindowID() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.focusedWindow
}

func (f *fakeCompositor) FocusedOutput() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.output, nil
}

// Perform behaves like niri: it is handed the action's name and either knows it
// or does not.
func (f *fakeCompositor) Perform(action string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.performed = append(f.performed, action)
	return nil
}

func (f *fakeCompositor) performCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.performed...)
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
	return serveAt(t, s, socketPath(t))
}

// serveAt is serve at a path the caller chose, for the one test that cares
// which directory the socket lands in.
func serveAt(t *testing.T, s *Server, path string) string {
	t.Helper()
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

// Whether layer 2 is on this machine (docs/delivery.md), which `zde doctor`
// reports and nothing else can answer: the daemon's PATH is the session's, so
// it is the answer for the keys that launch things rather than for whichever
// shell somebody is typing in. Asked per call rather than at startup, so a
// switch that installs zinc does not need a restart to be believed.
func TestStatusFindsZcrOnTheSessionsPath(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if s.status().Zinc {
		t.Error("status claims zcr with an empty PATH")
	}
	if err := os.WriteFile(filepath.Join(dir, "zcr"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !s.status().Zinc {
		t.Error("zcr is on the PATH and status does not say so")
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
//
// The directory has to be one zded makes, and that is the whole reason this
// test does not use the helper. socketPath hands back a name inside an
// os.MkdirTemp directory, which Go creates 0700 - so Listen's MkdirAll found
// the directory already there and created nothing, and what the assertion
// measured was the mode of Go's temp directory. It passed with the 0700 in
// Listen changed to 0755, and would have gone on passing after somebody
// widened it. A path two levels down is a directory Listen has to make itself,
// which is what a fresh XDG_RUNTIME_DIR looks like on a real login.
func TestSocketIsPrivate(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	path := serveAt(t, s, filepath.Join(t.TempDir(), "zde", "zded.sock"))

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

// One focus per screen, not per monitor a name mentions. With HDMI-A-1
// unplugged, both of vshop's workspaces are sitting on DP-1: focusing each of
// them in turn left the screen showing whichever came last, so the workspace
// the journal remembered was scrolled off by the switch that restored it.
func TestDeskSwitchWithAMonitorGoneFocusesTheScreenOnce(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	code, err := desk.ParseName("vshop.DP-1.code")
	if err != nil {
		t.Fatal(err)
	}
	if err := jrn.SetActive(code); err != nil {
		t.Fatal(err)
	}

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1", Idx: 0},
			{ID: 2, Name: "vshop.HDMI-A-1.aux", Output: "DP-1", Idx: 1}, // parked here
			{ID: 3, Name: "haven.DP-1.db", Output: "DP-1", Idx: 2},
		}, []string{"DP-1"}),
		focused: "haven.DP-1.db",
		output:  "DP-1",
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var plan []string
	if err := c.Call("desk.switch", &plan, "vshop"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(niri.focusCalls(), []string{"vshop.DP-1.code"}) {
		t.Errorf("focused %v, want the one screen brought up once, on what it remembered", niri.focusCalls())
	}
	// And nothing was written down about the monitor that is not there: what
	// vshop had on HDMI-A-1 is still what it will find when it comes back.
	if got := jrn.State().LastActive["vshop"]["HDMI-A-1"]; got != "" {
		t.Errorf("LastActive[vshop][HDMI-A-1] = %q, want nothing recorded for a monitor nothing was focused on", got)
	}
}

// A window carried to a desk whose workspaces are all parked on this screen
// stays on this screen. Asking whether the desk owns a workspace whose name
// says this monitor answered no, and the window went to another screen - on a
// machine that, with the monitor gone, has one.
// It lands on what film was left on, too. The journal keys that memory on the
// monitor in the workspace's name - HDMI-A-1, which is not a screen right now -
// so looking the slot up by the screen the window is on finds nothing and drops
// the window at the top of the strip instead.
func TestMoveWindowToADeskParkedOnThisScreen(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	notes, err := desk.ParseName("film.HDMI-A-1.notes")
	if err != nil {
		t.Fatal(err)
	}
	if err := jrn.SetActive(notes); err != nil {
		t.Fatal(err)
	}

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "haven.DP-1.db", Output: "DP-1", Idx: 0},
			{ID: 2, Name: "film.HDMI-A-1.player", Output: "DP-1", Idx: 1}, // parked here
			{ID: 3, Name: "film.HDMI-A-1.notes", Output: "DP-1", Idx: 2},  // and so is this
		}, []string{"DP-1"}),
		focused:       "haven.DP-1.db",
		output:        "DP-1",
		focusedWindow: 7,
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("desk.move-window-to", nil, "film"); err != nil {
		t.Fatal(err)
	}
	if got := niri.carryCalls(); !slices.Equal(got, []string{"film.HDMI-A-1.notes"}) {
		t.Errorf("carried the window to %v, want the workspace film was left on, on this screen", got)
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

// The desk you came from can stop existing while you are away: its last
// workspace is closed or changes hands, and no manifest declares it. Nothing
// used to clear the pointer, so desk.last named a desk that is not there every
// time it was pressed for the rest of the session. It says so once, and gives
// the pointer up.
func TestDeskLastForgetsADeskThatIsGone(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetLastDesk("haven")

	niri := &fakeCompositor{m: desk.Rebuild([]desk.Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
	}, []string{"DP-1"})}
	s := New("test", jrn, niri, nil)

	resp := s.Dispatch(Request{Method: "desk.last"})
	if resp.Error == "" {
		t.Fatal("went back to a desk with no workspaces")
	}
	if !strings.Contains(resp.Error, "haven") {
		t.Errorf("refusal %q does not name the desk that is gone", resp.Error)
	}
	if got := jrn.State().LastDesk; got != "" {
		t.Errorf("LastDesk = %q, want the pointer given up", got)
	}
	// Pressed again it is the ordinary answer of a session with nowhere to go
	// back to, rather than a desk name to go looking for.
	if resp := s.Dispatch(Request{Method: "desk.last"}); !strings.Contains(resp.Error, "no desk to go back to") {
		t.Errorf("second press = %q, want the answer for having nowhere to go back to", resp.Error)
	}
	if got := niri.focusCalls(); len(got) != 0 {
		t.Errorf("focused %v on the way to a desk that is not there", got)
	}
}

// Any other failure keeps the pointer. A compositor that cannot be reached is
// not a desk that is gone, and the desk is still there to go back to once it
// can.
func TestDeskLastKeepsThePointerWhenNiriIsUnreadable(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	jrn.SetLastDesk("haven")

	s := New("test", jrn, &fakeCompositor{err: errors.New("NIRI_SOCKET is not set")}, nil)
	if resp := s.Dispatch(Request{Method: "desk.last"}); resp.Error == "" {
		t.Fatal("a switch with no compositor was reported as done")
	}
	if got := jrn.State().LastDesk; got != "haven" {
		t.Errorf("LastDesk = %q, want the desk kept: it is still there", got)
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

// brokenDesks is a desks directory that cannot be read at all - the directory
// itself, not a file in it. One unparseable file is a different thing and no
// longer this: it costs that desk and is reported (see partialDesks).
type brokenDesks struct{}

func (brokenDesks) All() (map[string]*manifest.Desk, []manifest.Problem, error) {
	return nil, nil, errors.New("reading manifests: permission denied")
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
			name: "a desks directory that cannot be read",
			s:    New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, brokenDesks{}),
			want: "permission denied",
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

func (f fixedDesks) All() (map[string]*manifest.Desk, []manifest.Problem, error) { return f, nil, nil }

// partialDesks is the ordinary case this all exists for: somebody edited one
// manifest and got it wrong, and the others are fine.
type partialDesks struct {
	good map[string]*manifest.Desk
	bad  []manifest.Problem
}

func (p partialDesks) All() (map[string]*manifest.Desk, []manifest.Problem, error) {
	return p.good, p.bad, nil
}
func (partialDesks) Save(*manifest.Desk) (string, error) { return "", errors.New("not writable") }

// A broken manifest is one desk's problem, and it has to be somebody's problem:
// the daemon carries on with the manifests that work, and `zde status` names
// the file. Before this, the error was dropped and the map dropped with it, so
// every desk on the machine quietly stopped being declared - and nothing
// anywhere said why.
func TestStatusNamesTheManifestItCouldNotRead(t *testing.T) {
	desks := partialDesks{
		good: map[string]*manifest.Desk{
			"vshop": {Name: "vshop", Monitors: map[string]manifest.Monitor{
				"DP-1": {Workspaces: []string{"code"}},
			}},
		},
		bad: []manifest.Problem{{
			Path: "/desks/haven.yaml",
			Err:  errors.New("field monitorz not found"),
		}},
	}
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, desks)

	// Something has to read the manifests before status can know: on a real
	// machine that is any desk switch, and here it is the same call.
	s.manifestFor("vshop")

	st := s.status()
	if len(st.BadManifests) != 1 {
		t.Fatalf("BadManifests = %v, want the one file", st.BadManifests)
	}
	if !strings.Contains(st.BadManifests[0], "haven.yaml") {
		t.Errorf("status says %q, which does not name the file", st.BadManifests[0])
	}
	if !strings.Contains(st.BadManifests[0], "monitorz") {
		t.Errorf("status says %q, which does not say what is wrong with it", st.BadManifests[0])
	}
	// And the desk that parses is still declared, which is the half that used
	// to be lost.
	if s.manifestFor("vshop") == nil {
		t.Error("the manifest that parses went with the one that does not")
	}
}

// Both of the fields status answers with somebody else's words in them are one
// row each, cleaned here where those words enter the daemon.
//
// A manifest is a file somebody edits by hand and a YAML parser answers it by
// quoting the file back - `field <key> not found`, over as many lines as there
// were mistakes, with an ESC in it if the file had one. niri's message is
// whatever niri says. Every reader of these draws a row: a line under
// "manifest" or after "compositor" in `zde status`, a failing check in doctor's
// report, whatever the shell puts them on next. In a row a newline is a second
// row with nothing in column one, which is a line no daemon printed.
func TestStatusAnswersWithOneRowForEachThingItDidNotWrite(t *testing.T) {
	desks := partialDesks{
		good: map[string]*manifest.Desk{},
		bad: []manifest.Problem{{
			Path: "/desks/haven.yaml",
			Err:  errors.New("manifest: yaml: unmarshal errors:\n  line 2: field \x1b[2Jmonitorz not found\n  line 9: no"),
		}},
	}
	s := New("test", nil, &fakeCompositor{err: errors.New("niri: socket\x1b[2J gone\ncompositor connected")}, desks)
	s.manifestFor("vshop")

	st := s.status()
	if len(st.BadManifests) != 1 {
		t.Fatalf("BadManifests = %v, want the one file", st.BadManifests)
	}
	for what, got := range map[string]string{"the manifest problem": st.BadManifests[0], "the compositor line": st.Compositor} {
		if strings.ContainsAny(got, "\n\x1b") {
			t.Errorf("%s is %q, which is more than one row or drives a terminal", what, got)
		}
	}
	// Still legible: the file, what the parser could not use, and what niri
	// said - which is the whole reason these are reported rather than dropped.
	for _, want := range []string{"haven.yaml", "monitorz", "line 9"} {
		if !strings.Contains(st.BadManifests[0], want) {
			t.Errorf("status says %q, which no longer contains %q", st.BadManifests[0], want)
		}
	}
	if !strings.Contains(st.Compositor, "socket") {
		t.Errorf("the compositor line is %q, which no longer says what niri said", st.Compositor)
	}
}
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
	all, problems, err := dir.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("snapshot wrote something that does not load: %v", problems)
	}
	d, ok := all["vshop"]
	if !ok {
		t.Fatalf("snapshot wrote %s, which does not load as a desk", path)
	}
	if len(d.Workspaces()) != 2 {
		t.Errorf("snapshot recorded %v", d.Workspaces())
	}
}

// Snapshot and the reading of a manifest back are meant to be inverses: the
// desk you arranged, written down, comes back as the same desk. This is that
// property rather than a case per way it was not - the desk goes to a file, the
// file comes back onto a machine with nothing on the screen, and the workspaces
// are the same ones in the same order.
//
// The shapes are the ones where a wrong answer shows. Labels that do not sort
// into the strip's order catch a snapshot writing an order of its own; an
// ordinal catches the check that used to refuse the whole desk because adoption
// had minted one for a window with no readable app id.
//
// The manifest is the only record here - no journal - so what comes back comes
// back out of the file.
func TestSnapshotAndSwitchAreInverses(t *testing.T) {
	// reconcile below writes niri's placement rules, which go under
	// XDG_CONFIG_HOME. This keeps them out of the home directory of whoever is
	// running the tests.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cases := []struct {
		name  string
		strip map[string][]string // monitor -> its strip, top first
	}{
		{"labels the strip does not have in sorted order", map[string][]string{"DP-1": {"zsh", "agent"}}},
		{"a workspace adoption named with an ordinal", map[string][]string{"DP-1": {"code", "1"}}},
		{"ordinals in the strip's order and not in numeric order", map[string][]string{"DP-1": {"10", "2"}}},
		{"two monitors", map[string][]string{"DP-1": {"zsh", "agent"}, "HDMI-A-1": {"aux"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := manifest.Dir(t.TempDir())
			monitors := make([]string, 0, len(c.strip))
			for mon := range c.strip {
				monitors = append(monitors, mon)
			}
			sort.Strings(monitors)

			// The desk as it is on the screen: each monitor's strip, in order.
			var all []desk.Workspace
			var want []string
			for _, mon := range monitors {
				for i, slot := range c.strip[mon] {
					name := "vshop." + mon + "." + slot
					all = append(all, desk.Workspace{ID: uint64(len(all) + 1), Name: name, Output: mon, Idx: uint8(i)})
					want = append(want, name)
				}
			}
			before := &fakeCompositor{m: desk.Rebuild(all, monitors), focused: all[0].Name, output: monitors[0]}
			if resp := New("test", nil, before, dir).Dispatch(
				Request{Method: "desk.snapshot", Args: []string{"vshop"}}); resp.Error != "" {
				t.Fatalf("snapshot: %s", resp.Error)
			}

			// The other machine: the manifest, and nothing but niri's empty
			// tail on each monitor.
			after := &fakeCompositor{empty: map[string][]uint64{}, nextID: 100, output: monitors[0]}
			for i, mon := range monitors {
				after.empty[mon] = []uint64{uint64(10 + i)}
			}
			after.remap()
			s := New("test", nil, after, dir)
			if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
				t.Fatalf("switch: %s", resp.Error)
			}
			if got := after.adoptCalls(); !slices.Equal(got, want) {
				t.Errorf("the desk came back as %v, want %v", got, want)
			}
			// And each monitor is entered where its strip starts, which is
			// where you were standing when you wrote the desk down.
			var top []string
			for _, mon := range monitors {
				top = append(top, "vshop."+mon+"."+c.strip[mon][0])
			}
			if got := after.focusCalls(); !slices.Equal(got, top) {
				t.Errorf("entered on %v, want the top of each strip %v", got, top)
			}
			// And reconcile finds nothing to correct: the round trip converged
			// rather than leaving work for the next pass to do.
			after.focused = top[0]
			if resp := s.Dispatch(Request{Method: "desk.reconcile"}); resp.Error != "" {
				t.Fatalf("reconcile: %s", resp.Error)
			}
			if got := after.renameCalls(); len(got) != 0 {
				t.Errorf("reconcile renamed %v after a round trip", got)
			}
			if got := after.adoptCalls(); !slices.Equal(got, want) {
				t.Errorf("reconcile named %v as well", got[len(want):])
			}
		})
	}
}

// A snapshot taken with the lid down writes the laptop panel, because the map
// still says so. It is the same fix: nothing renamed the workspace off eDP-1,
// so the name FromMap reads is still the truth, and the manifest gets home
// rather than whichever screen survived the lid closing. There is no separate
// guard here and none is needed - the snapshot writes down the map, and the
// map is right.
func TestDeskSnapshotWithTheLidDown(t *testing.T) {
	dir := manifest.Dir(t.TempDir())
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.eDP-1.code", Output: "DP-1"}, // parked by the lid closing
			{ID: 2, Name: "vshop.DP-1.aux", Output: "DP-1"},
		}, []string{"DP-1"}), // eDP-1 is a connector, not a screen
		focused: "vshop.eDP-1.code",
	}
	s := New("test", nil, niri, dir)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Call("desk.snapshot", nil); err != nil {
		t.Fatal(err)
	}
	all, problems, err := dir.All()
	if err != nil || len(problems) != 0 {
		t.Fatalf("snapshot wrote something that does not load: %v %v", err, problems)
	}
	d, ok := all["vshop"]
	if !ok {
		t.Fatal("snapshot wrote nothing that loads as vshop")
	}
	if got := d.Monitors["eDP-1"].Workspaces; len(got) != 1 || got[0] != "code" {
		t.Errorf("eDP-1 got %v, want the workspace that belongs to the laptop panel written under it", got)
	}
	if got := d.Monitors["DP-1"].Workspaces; len(got) != 1 || got[0] != "aux" {
		t.Errorf("DP-1 got %v, want only the workspace that is actually its own", got)
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

// The two questions the bar asks on a clock stay the same price as the session
// gets longer.
//
// queue.list and attn.mode are sixty of the daemon's requests a minute between
// them, for as long as somebody is logged in, and each of them reads one field.
// Both used to read that field out of a copy of everything the journal
// remembers - the queue, and a map of desks with a map of monitors inside each -
// so the cost of the cheapest thing zded does grew with how many desks had been
// visited since login.
//
// Measured by comparison rather than against a number, because what was wrong
// with it was the shape of the cost and not its size (internal/journal,
// Waiting).
func TestTheBarsPollDoesNotCopyTheWholeJournal(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	if resp := s.Dispatch(Request{Method: "queue.add", Args: []string{"reply to ilya"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	poll := func() {
		s.Dispatch(Request{Method: "queue.list"})
		s.Dispatch(Request{Method: "attn.mode"})
	}
	small := testing.AllocsPerRun(50, poll)

	// A session that has been used: a hundred desks, each remembering where it
	// was left on two monitors.
	for i := 0; i < 100; i++ {
		for _, monitor := range []string{"DP-1", "DP-2"} {
			n, err := desk.NewName("d"+strconv.Itoa(i), monitor, "code")
			if err != nil {
				t.Fatal(err)
			}
			if err := jrn.SetActive(n); err != nil {
				t.Fatal(err)
			}
		}
	}
	big := testing.AllocsPerRun(50, poll)
	// With a few allocations of slack, because this counts everything the
	// process allocated while the poll ran and not only the poll's own:
	// goroutines other tests left behind land in the same number, and so does a
	// json encoder pool a GC emptied between the two measurements. Without the
	// slack it fails once in a handful of runs on a loaded machine, over a drift
	// of exactly one allocation - and it fails on whichever branch happened to
	// add a test above it.
	//
	// What is being caught is the shape of the cost and not its size: a hundred
	// desks copied per poll is a hundred allocations and more, so the slack sits
	// two orders of magnitude below the regression.
	const noise = 5
	if big > small+noise {
		t.Errorf("one poll costs %v allocations after a hundred desks and %v before it: the bar is paying for the whole journal to read two fields",
			big, small)
	}
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

// Printable and drawn are not the same thing, and this door used to ask only
// the first. A combining acute on its own is printable by every test Go has and
// draws no character of its own, so it made a queue item whose text column was
// blank - nothing to read, nothing to recognise, and nothing to act on but the
// id beside it.
//
// The floor the app's door now stands on too, which is the point of the two of
// them asking it with one function (internal/attn, Draws, and
// TestASummaryThatDrawsNothingIsNoSummary beside it). A summary of nothing but
// zero-width joiners used to walk past that door and land here as a row nobody
// could read, refused at this one and let in at the other.
//
// Only the floor. Above it the doors are meant to differ and do: this one
// refuses a tab where oneLine folds it, because a person typing a reminder can
// be told to try again and an app's notification is the only copy there will
// ever be of something that already happened.
func TestQueueAddRefusesTextThatDrawsNothing(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, bad := range []string{"́", "́́́", "️", "⃣"} {
		if err := c.Call("queue.add", nil, bad); err == nil {
			t.Errorf("queue.add %q was accepted, and there is nothing in it to read", bad)
		}
	}
	// And what does draw goes on through, marks and all: an accented word is a
	// word, and this refuses text rather than characters.
	if err := c.Call("queue.add", nil, "réponse à Ilya"); err != nil {
		t.Errorf("a reminder with something to draw was refused: %v", err)
	}
}

// Jumping goes to where the oldest thing waits, and leaves it waiting:
// arriving somewhere is not doing the thing.
func TestQueueJumpGoesToTheOldestAndLeavesIt(t *testing.T) {
	s, jrn, niri := queueTestServer(t, "vshop.DP-1.code")
	if _, err := jrn.Queue(journal.Item{Text: "first, on haven", Desk: "haven"}); err != nil {
		t.Fatal(err)
	}
	if _, err := jrn.Queue(journal.Item{Text: "second, on vshop", Desk: "vshop"}); err != nil {
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
	jrn.Queue(journal.Item{Text: "taken while niri was down", Desk: ""})
	jrn.Queue(journal.Item{Text: "on haven", Desk: "haven"})
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
	it, _ := jrn.Queue(journal.Item{Text: "reply to ilya", Desk: "vshop"})
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

// One call empties the whole thing, and says how much it emptied.
//
// The way out of a queue that has got away from somebody. It was a thousand
// calls, which is not a way out, and a journal written before the queue had any
// bounds at all can still hold more than the ceiling - the replay does not trim
// it on purpose (internal/journal, QueueMax).
func TestQueueClearEmptiesItAndSaysHowMuch(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	for i := 0; i < 5; i++ {
		if _, err := jrn.Queue(journal.Item{Text: "owed", Desk: "vshop"}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var cleared Cleared
	if err := c.Call("queue.clear", &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.Count != 5 {
		t.Errorf("cleared %d, want the 5 that were waiting", cleared.Count)
	}
	if q := jrn.Waiting(); len(q) != 0 {
		t.Errorf("queue = %+v, want it empty", q)
	}
	// Twice in a row is a person checking, not an error.
	if err := c.Call("queue.clear", &cleared); err != nil || cleared.Count != 0 {
		t.Errorf("clearing an empty queue = %d, %v", cleared.Count, err)
	}
	// And no half of it: an argument silently ignored is how somebody empties
	// the whole queue believing they emptied part of it.
	if err := c.Call("queue.clear", nil, "ci"); err == nil {
		t.Error("queue.clear took an argument")
	}
}

// What the clear drops, the rest of attn is told about.
//
// The same two things queue.done does for one item: the history stops showing it
// as waiting, and whoever sent it hears that it is gone. An app blocked on its
// own notification's closure has no other way to learn, and a center still
// drawing a row the queue has let go of is the two halves of attn disagreeing
// about one arrival.
func TestQueueClearTellsTheHistoryAndTheSenders(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	var told []uint64
	s.Watching(tellTale{ids: &told})
	id, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"})
	if err != nil {
		t.Fatal(err)
	}
	if resp := s.Dispatch(Request{Method: "queue.clear"}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	for _, r := range s.history.Recent() {
		if r.ID == id && !r.Dismissed {
			t.Error("the center still shows it as waiting after the queue let go of it")
		}
	}
	if !slices.Contains(told, id) {
		t.Errorf("dismissed %v, want the cleared id told to whoever sent it", told)
	}
}

// The queue outlives the compositor, so a desk it remembers may be gone by the
// next login. One stale reminder must not hold the key down for every reminder
// behind it.
func TestQueueJumpSkipsADeskThatIsGone(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	jrn.Queue(journal.Item{Text: "on a desk from last week", Desk: "oldproject"})
	jrn.Queue(journal.Item{Text: "on haven", Desk: "haven"})
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
	first, _ := jrn.Queue(journal.Item{Text: "oldest", Desk: "vshop"})
	second, _ := jrn.Queue(journal.Item{Text: "middle", Desk: "haven"})
	third, _ := jrn.Queue(journal.Item{Text: "newest", Desk: "vshop"})
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

// A notification becomes a queue item on the desk it arrived on, with what
// sent it recorded as the claim it is.
func TestNotificationArrivesOnTheQueue(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	id, err := s.Arrived(attn.Notification{From: "Fractal", Text: "Ilya: about the invoice", Urgent: true})
	if err != nil {
		t.Fatal(err)
	}
	q := jrn.State().Queue
	if len(q) != 1 {
		t.Fatalf("queue = %+v", q)
	}
	if q[0].Text != "Ilya: about the invoice" || q[0].Desk != "vshop" || q[0].From != "Fractal" || !q[0].Urgent {
		t.Errorf("item = %+v, want the text, the desk it arrived on, the claim, and the urgency", q[0])
	}
	if uint64(id) != q[0].ID {
		t.Errorf("answered with id %d, queued %d: the app could not close it", id, q[0].ID)
	}
}

// Replacing is attn's to decide - it is the half that knows which sender owns
// which id - so what the daemon has to get right is doing as it is told.
func TestNotificationClosedTakesTheRightOneOff(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	first, err := s.Arrived(attn.Notification{From: "curl", Text: "downloading 1%"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Arrived(attn.Notification{From: "curl", Text: "downloading 90%"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Closed(first); err != nil {
		t.Fatal(err)
	}
	q := jrn.State().Queue
	if len(q) != 1 || q[0].Text != "downloading 90%" {
		t.Errorf("queue = %+v, want the superseded one gone and the newest left", q)
	}
}

// The message a notification carried is somebody's mail, and the journal is a
// file: fsynced a line at a time, compacted only at Open, and still there in
// the morning. The record in memory keeps the whole of it, which is what the
// notification center shows; what reaches the disk is the row the queue is made
// of and nothing else (internal/journal, Item).
//
// Asserted against the bytes of the file, because what is on the disk is the
// whole of what this promises.
func TestANotificationBodyNeverReachesTheJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	jrn, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	niri := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", jrn, niri, nil)
	if _, err := s.Arrived(attn.Notification{
		From: "clinic", Text: "your results are in", Body: "the biopsy came back clear",
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "the biopsy came back clear") {
		t.Error("a notification body is in the journal, where it outlives the session and anything that can read the state directory can read it")
	}
	if !strings.Contains(string(raw), "your results are in") {
		t.Error("nothing was queued: the row is what a queue item is, and losing it is zde forgetting what you still owe")
	}
	// And the whole of it is still in the center, which is the half allowed to
	// have it: display policy, never data policy (docs/vision.md, principle 3).
	if seen := s.history.Recent(); len(seen) != 1 || seen[0].Body != "the biopsy came back clear" {
		t.Errorf("history = %+v, want the message itself kept in memory", seen)
	}
}

// Whoever sent something may be waiting to hear it is gone, and queue done is
// the route that tells them.
func TestQueueDoneTellsTheSender(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	told := []uint64{}
	s.Watching(tellTale{ids: &told})
	it, _ := jrn.Queue(journal.Item{Text: "waiting on you", From: "app"})
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("queue.done", nil, strconv.FormatUint(it.ID, 10)); err != nil {
		t.Fatal(err)
	}
	if len(told) != 1 || told[0] != it.ID {
		t.Errorf("told %v, want the id that was finished", told)
	}
}

// tellTale is the bus, watched: what zded told the sender, and about what.
type tellTale struct {
	ids *[]uint64
	// invoked is what was pressed, as "id key", and err is what the bus said
	// about it - an app that has exited is a refusal, not a silence. The key is
	// kept because a center that offers three buttons and always sends the
	// default would archive what somebody meant to reply to.
	invoked *[]string
	// forgotten is the ids the daemon said nothing can address any more.
	forgotten *[]uint64
	err       error
}

func (t tellTale) Dismissed(id uint64) { *t.ids = append(*t.ids, id) }

// Forget is the bus side being told a notification has fallen out of the
// history, which is the only thing that prunes what it remembers about senders.
func (t tellTale) Forget(id uint64) {
	if t.forgotten != nil {
		*t.forgotten = append(*t.forgotten, id)
	}
}

func (t tellTale) Invoke(id uint64, key string) error {
	if t.invoked != nil {
		*t.invoked = append(*t.invoked, strconv.FormatUint(id, 10)+" "+key)
	}
	return t.err
}

// An app taking its own notification back.
func TestNotificationClosed(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	id, _ := s.Arrived(attn.Notification{From: "app", Text: "transient"})
	if err := s.Closed(id); err != nil {
		t.Fatal(err)
	}
	if q := jrn.State().Queue; len(q) != 0 {
		t.Errorf("queue = %+v, want it taken back", q)
	}
}

// niri being unreadable must not lose the notification: it is the only copy
// there will ever be of something that already happened.
func TestNotificationArrivesWithoutACompositor(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	s := New("test", jrn, &fakeCompositor{err: errors.New("NIRI_SOCKET is not set")}, nil)
	if _, err := s.Arrived(attn.Notification{From: "app", Text: "the build failed"}); err != nil {
		t.Fatalf("the notification was lost because niri was down: %v", err)
	}
	if q := jrn.State().Queue; len(q) != 1 || q[0].Desk != "" {
		t.Errorf("queue = %+v, want it kept with no desk", q)
	}
}

// The regulars had no way to come into being. A manifest cannot declare the
// band, adoption names into the desk you are standing on, and so the only
// route was naming a workspace by hand through niri - which is not something
// the product can ask of anyone. This is that verb, and it is the same rename
// in both directions, so it is also how work comes back out of a band that
// would otherwise only fill up (docs/roadmap.md).
func TestMoveWorkspaceToMakesTheRegulars(t *testing.T) {
	f := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.comms", Output: "DP-1"},
			{Name: "vshop.DP-1.code", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "vshop.DP-1.comms",
		output:  "DP-1",
	}
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	s := New("test", jrn, f, nil)

	var got []string
	resp := s.Dispatch(Request{Method: "desk.move-workspace-to", Args: []string{"regulars"}})
	if resp.Error != "" {
		t.Fatalf("move-workspace-to: %s", resp.Error)
	}
	if err := json.Unmarshal(resp.Ok, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "regulars.DP-1.comms" {
		t.Errorf("answered %v, want [regulars.DP-1.comms]", got)
	}
	if len(f.renames) != 1 || f.renames[0] != "vshop.DP-1.comms -> regulars.DP-1.comms" {
		t.Errorf("renames = %v", f.renames)
	}
	// You did not go anywhere - the workspace under you changed bands - so the
	// journal has to agree that you are in the regulars now, or desk.last
	// takes you to where you already are.
	st := jrn.State()
	if st.OnDesk != "regulars" {
		t.Errorf("OnDesk = %q, want regulars", st.OnDesk)
	}
	if st.LastDesk != "vshop" {
		t.Errorf("LastDesk = %q, want vshop, so desk.last comes back", st.LastDesk)
	}
}

// The workspace changes hands and the position remembered for it goes with it.
// It used to be written back into the desk that lost it, so vshop remembered a
// workspace the regulars own - a last-active slot that is not a memory of
// anything, and the desk it names is one no switch to vshop can reach.
func TestMoveWorkspaceToMovesTheRememberedPosition(t *testing.T) {
	f := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{ID: 1, Name: "vshop.DP-1.comms", Output: "DP-1", Idx: 0},
			{ID: 2, Name: "vshop.DP-1.code", Output: "DP-1", Idx: 1},
		}, []string{"DP-1"}),
		focused: "vshop.DP-1.comms",
		output:  "DP-1",
	}
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	comms, err := desk.NewName("vshop", "DP-1", "comms")
	if err != nil {
		t.Fatal(err)
	}
	jrn.SetActive(comms)
	s := New("test", jrn, f, nil)

	if resp := s.Dispatch(Request{Method: "desk.move-workspace-to", Args: []string{"regulars"}}); resp.Error != "" {
		t.Fatalf("move-workspace-to: %s", resp.Error)
	}
	st := jrn.State()
	if got, stale := st.LastActive["vshop"]["DP-1"]; stale {
		t.Errorf("vshop remembers %q on DP-1, which the regulars own now", got)
	}
	if got := st.LastActive["regulars"]["DP-1"]; got != "comms" {
		t.Errorf("regulars last active = %q, want the workspace they were given", got)
	}
}

// A slot the manifest declares would be recreated by the next switch, so the
// move would look undone by something invisible. Refusing has to name the desk
// whose file needs editing.
func TestMoveWorkspaceToRefusesADeclaredSlot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vshop.yaml"),
		[]byte("name: vshop\nmonitors: { DP-1: { workspaces: [comms] } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.comms", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "vshop.DP-1.comms",
		output:  "DP-1",
	}
	s := New("test", nil, f, manifest.Dir(dir))

	resp := s.Dispatch(Request{Method: "desk.move-workspace-to", Args: []string{"regulars"}})
	if resp.Error == "" {
		t.Fatal("moved a workspace its own manifest declares, which comes straight back")
	}
	if !strings.Contains(resp.Error, "vshop") || !strings.Contains(resp.Error, "comms") {
		t.Errorf("refusal %q names neither the desk nor the slot", resp.Error)
	}
	if len(f.renames) != 0 {
		t.Errorf("refused and renamed anyway: %v", f.renames)
	}
}

// An unnamed workspace has no label to keep and no band to leave, and niri
// keeps an empty one at the end of every strip. The refusal has to say what to
// do instead, because "not a zde name" explains nothing to somebody who just
// pressed a key.
func TestMoveWorkspaceToRefusesAnUnnamedWorkspace(t *testing.T) {
	f := &fakeCompositor{
		m:       twoDesks(),
		focused: "7",
		output:  "DP-1",
	}
	s := New("test", nil, f, nil)

	resp := s.Dispatch(Request{Method: "desk.move-workspace-to", Args: []string{"regulars"}})
	if resp.Error == "" {
		t.Fatal("moved a workspace that has no zde name")
	}
	if !strings.Contains(resp.Error, "adopted") {
		t.Errorf("refusal %q does not say how to make it movable", resp.Error)
	}
}

// The advice for an empty band names this verb. A message telling somebody to
// run something has to name something that runs: the old one told them to name
// a workspace through niri by hand, which is what this verb replaced.
func TestTheRegularsAdviceNamesAVerbThatExists(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	resp := s.Dispatch(Request{Method: "desk.regulars"})
	if resp.Error == "" {
		t.Fatal("there are no regulars here and desk.regulars did not say so")
	}
	if !strings.Contains(resp.Error, "move-workspace-to") {
		t.Fatalf("advice %q does not name the verb that makes a band", resp.Error)
	}
	// And that verb answers, rather than being a name in a string.
	if got := s.Dispatch(Request{Method: "desk.move-workspace-to", Args: []string{"regulars"}}); strings.HasPrefix(got.Error, "unknown method") {
		t.Errorf("the advice names %q, which zded does not have", "desk.move-workspace-to")
	}
}

// desk.apps is what a desk declares, put in the two forms something else can
// use: the address zinc takes (zde keeps app and instance in separate fields,
// so somebody has to join them) and the workspace name zde uses everywhere
// else.
func TestDeskApps(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vshop.yaml"), []byte(
		"name: vshop\nmonitors: { DP-1: { workspaces: [code, web] } }\n"+
			"apps:\n"+
			"  - { app: nvim, instance: vshop, monitor: DP-1, workspace: code }\n"+
			"  - { app: browser }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, manifest.Dir(dir))

	// No name: the desk on the screen, which is what a keybind would ask for.
	list := deskAppsOf(t, s, Request{Method: "desk.apps"})
	if len(list) != 2 {
		t.Fatalf("desk.apps returned %d apps, want 2: %+v", len(list), list)
	}
	if list[0].Address != "nvim@vshop" {
		t.Errorf("address = %q, want nvim@vshop", list[0].Address)
	}
	if list[0].Place != "vshop.DP-1.code" {
		t.Errorf("place = %q, want the workspace name it is pinned to", list[0].Place)
	}
	// No instance is the app's bare name - zinc renames nothing that ran before
	// instances existed - and no pin is adoption's business, not a place.
	if list[1].Address != "browser" || list[1].Place != "" {
		t.Errorf("unpinned app with no instance = %+v", list[1])
	}
}

// The desk on the screen and the desk asked about are different questions, and
// the second one has to work when the first has no answer: the reason to ask
// about a desk you are not on is to find out what it would start.
func TestDeskAppsByName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "haven.yaml"), []byte(
		"name: haven\nmonitors: { DP-1: { workspaces: [read] } }\n"+
			"apps: [{ app: reader, instance: haven }]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, manifest.Dir(dir))

	list := deskAppsOf(t, s, Request{Method: "desk.apps", Args: []string{"haven"}})
	if len(list) != 1 || list[0].Address != "reader@haven" {
		t.Fatalf("desk.apps haven = %+v", list)
	}
}

// A desk that exists on the screen and was never written down is the ordinary
// way to land here, so the refusal lists what is declared rather than saying
// the name is wrong.
func TestDeskAppsNamesWhatIsDeclared(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "haven.yaml"),
		[]byte("name: haven\nmonitors: { DP-1: { workspaces: [read] } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, manifest.Dir(dir))

	resp := s.Dispatch(Request{Method: "desk.apps", Args: []string{"vshop"}})
	if resp.Error == "" {
		t.Fatal("listed the apps of a desk with no manifest")
	}
	if !strings.Contains(resp.Error, "haven") {
		t.Errorf("refusal %q does not say which desks are declared", resp.Error)
	}
}

func deskAppsOf(t *testing.T, s *Server, req Request) []DeskApp {
	t.Helper()
	resp := s.Dispatch(req)
	if resp.Error != "" {
		t.Fatalf("%s: %s", req.Method, resp.Error)
	}
	var list []DeskApp
	if err := json.Unmarshal(resp.Ok, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

// A desk is what its manifest declares, so entering one starts it. The two
// halves worth pinning: what gets asked for (the address, both fields joined),
// and that standing still is not entering - half the nav keys re-enter the desk
// you are on, and each pass would be another launch attempt per app.
func TestSwitchStartsTheDesksApps(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vshop.yaml"), []byte(
		"name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\n"+
			"apps:\n  - { app: nvim, instance: vshop }\n  - { app: browser }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.read", output: "DP-1"}
	// With a journal, because the record of which desk you were on is what
	// decides this: by the time a switch reads the compositor, the desk it is
	// entering has already been named into existence.
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	s := New("test", jrn, f, manifest.Dir(dir))

	started := make(chan string, 4)
	s.launch = func(_ context.Context, address string) error {
		started <- address
		return nil
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var got []string
	for range 2 {
		select {
		case a := <-started:
			got = append(got, a)
		case <-time.After(2 * time.Second):
			t.Fatalf("the desk declares two apps and started %v", got)
		}
	}
	sort.Strings(got)
	if got[0] != "browser" || got[1] != "nvim@vshop" {
		t.Errorf("started %v, want [browser nvim@vshop]", got)
	}

	// Already there: nothing to bring up.
	f.focused = "vshop.DP-1.code"
	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	select {
	case a := <-started:
		t.Errorf("re-entering the desk you are on started %s again", a)
	case <-time.After(300 * time.Millisecond):
	}
}

// A request line is bounded, and the bound is what stops this socket being an
// allocator anything on this machine can drive.
//
// Measured on a running zded before it existed: four connections each pushing
// 200 MB with no newline in them took RSS from 8 MB to 2856 MB, roughly twice
// the bytes sent, and none of it came back when the connections closed. The
// daemon answered `zde status` instantly throughout, so the one diagnostic a
// person would run said it was fine.
//
// What is asserted is the bound under load rather than the constant: 128 MiB
// arrives with no newline in it, and what the daemon may allocate reading that
// is what four capped lines cost rather than what 128 MiB costs. The refusal is
// asserted too, because a caller that sent something impossible is owed a
// sentence saying so.
func TestAnEndlessRequestLineIsRefusedRatherThanHeld(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	path := serve(t, s)

	// Four connections and 32 MiB each, which is 32 times the cap apiece.
	const conns = 4
	const each = 32 << 20

	var wrote sync.WaitGroup
	said := make(chan string, conns)
	for i := 0; i < conns; i++ {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		wrote.Add(1)
		go func() {
			defer wrote.Done()
			// No newline anywhere in it, which is the whole of the attack:
			// nothing here is a request, so nothing ever finishes being read.
			chunk := bytes.Repeat([]byte("a"), 1<<20)
			for sent := 0; sent < each; sent += len(chunk) {
				if _, err := c.Write(chunk); err != nil {
					return
				}
			}
		}()
		go func() {
			line, err := bufio.NewReader(c).ReadString('\n')
			if err != nil {
				said <- ""
				return
			}
			said <- line
		}()
	}

	// Counted from before the writing, so everything the reading allocates is
	// inside it. TotalAlloc rather than a heap reading: what is bounded is what
	// the daemon may take to read this at all, and a heap reading is an answer
	// about when the collector last ran.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	done := make(chan struct{})
	go func() { wrote.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the writers were still writing a minute later, so nothing refused them")
	}
	runtime.ReadMemStats(&after)

	// The arithmetic: a scanner doubling from 4 KiB to requestMax allocates
	// about 2 MiB over one connection's life, so four of them is about 8 MiB.
	// Uncapped, the same 128 MiB costs about twice itself - every fragment is
	// copied into the line and the line is copied again each time it grows - so
	// 256 MiB, which is the shape measured at 2856 MB for 1400 MB sent.
	grew := after.TotalAlloc - before.TotalAlloc
	if grew > 64<<20 {
		t.Errorf("%d MiB arrived with no newline in it and the daemon allocated %d MiB reading it, past the 64 MiB four capped lines could cost",
			(conns*each)>>20, grew>>20)
	}

	for i := 0; i < conns; i++ {
		select {
		case line := <-said:
			if !strings.Contains(line, "longer than") {
				t.Errorf("a connection that sent %d MiB with no newline in it was answered %q", each>>20, strings.TrimSpace(line))
			}
		case <-time.After(30 * time.Second):
			t.Fatal("a connection sent 32 MiB with no newline in it and the daemon is still reading")
		}
	}
}

// And the largest thing a legitimate caller sends still fits, which is the other
// half of choosing the number. An ask.run carries a whole conversation, bounded
// at askContextMax as the tier reads it - and the Go client writes it with
// json.Marshal, whose HTML escaping turns "<" into six bytes. A conversation of
// nothing but those is the worst case anybody honest can produce, and it has to
// be answered rather than refused for its length.
func TestTheLargestConversationACallerMaySendFitsInOneRequest(t *testing.T) {
	writeTiers(t, map[string][]string{})
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	path := serve(t, s)

	// A bare question is handed to the tier as itself, with no frame and no
	// turns (ask.go, askDoc), so this is exactly askContextMax of document.
	question := strings.Repeat("<", askContextMax)
	line, err := json.Marshal(Request{Method: MethodAskRun, Args: []string{TierLocal, question}})
	if err != nil {
		t.Fatal(err)
	}
	// The arithmetic requestMax was chosen by, as a measurement rather than a
	// claim in a comment: six bytes on the wire for every byte of conversation.
	if len(line)+1 > requestMax {
		t.Fatalf("the largest conversation a caller may send is %d KiB on the wire, past requestMax of %d KiB",
			(len(line)+1)>>10, requestMax>>10)
	}

	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(MethodAskRun, nil, TierLocal, question)
	if err == nil {
		t.Fatal("there is no local tier and the ask was accepted anyway")
	}
	// Refused for the tier it has not got, which is the daemon having read the
	// whole request. Refused for its length would be the bound eating a caller
	// doing nothing wrong.
	if strings.Contains(err.Error(), "longer than") {
		t.Errorf("the largest legitimate conversation was refused for its length: %v", err)
	}
	if !strings.Contains(err.Error(), "no local tier") {
		t.Errorf("the answer was %v, want the missing tier", err)
	}
}

// peer is one connection held open by a test, with the reader that goes with
// it. Raw rather than a Client, because these tests are about connections that
// are not being used: what has to be asserted is what arrives on one that is
// only open, and Client has no way to read without asking first.
type peer struct {
	conn net.Conn
	r    *bufio.Reader
}

// dialPeers opens n connections and makes each of them ask for something, in
// order, so that the order they were admitted in is the order they will be
// dropped in.
//
// The asking is not decoration. admit runs on the connection's own goroutine,
// so n dials in a loop are n goroutines racing to be counted and which of them
// is the oldest is whatever the scheduler decided. A reply read back is proof
// that this connection was admitted before the next one dialled.
func dialPeers(t *testing.T, path string, n int) []*peer {
	t.Helper()
	peers := make([]*peer, 0, n)
	for i := 0; i < n; i++ {
		p := dialPeer(t, path)
		if line := p.ask(t, `{"method":"status"}`); !strings.Contains(line, `"ok"`) {
			t.Fatalf("connection %d was answered %q", i, strings.TrimSpace(line))
		}
		peers = append(peers, p)
	}
	return peers
}

func dialPeer(t *testing.T, path string) *peer {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &peer{conn: c, r: bufio.NewReader(c)}
}

// ask writes one request and waits for one line back.
func (p *peer) ask(t *testing.T, req string) string {
	t.Helper()
	if _, err := fmt.Fprintln(p.conn, req); err != nil {
		t.Fatalf("writing %s: %v", req, err)
	}
	line, err := p.hear(10 * time.Second)
	if err != nil {
		t.Fatalf("after %s: %v", req, err)
	}
	return line
}

// hear waits for one line, or says what stopped it. An error is an answer here
// rather than a failure: a connection that was closed under its client is
// exactly what these tests are about.
func (p *peer) hear(wait time.Duration) (string, error) {
	if err := p.conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return "", err
	}
	return p.r.ReadString('\n')
}

// How many connections zded keeps is bounded, over the socket, the way a
// program with a socket would find out.
//
// Measured on a running zded before this existed, connections opened and then
// held idle: 10,000 of them cost 99 MB of RSS and 10,007 descriptors, and
// 100,000 cost 882 MB and 100,007 - about 9 KB and one descriptor each, opened
// at 65,000 a second, and none of the memory came back when they closed. Past
// that the daemon does not slow down, it stops: accept4 returns EMFILE at
// RLIMIT_NOFILE, Serve returns it and the process exits and removes its socket.
// Measured at a lowered limit: 262,138 connections in 4.3 seconds and then no
// zded at all.
//
// What is asserted is the bound under the load rather than the existence of a
// constant. Four times the cap arrives, and what the daemon is holding
// afterwards is the cap - in connections, and in the goroutines behind them,
// because a connection dropped from the table that went on reading would be the
// same leak with the count hidden.
func TestOnlySoManyConnectionsAreKeptAtOnce(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	path := serve(t, s)

	before := runtime.NumGoroutine()
	const flood = ConnectionsMax * 4
	for i := 0; i < flood; i++ {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		defer c.Close()
	}

	waitFor(t, "the table settling at the cap", func() bool { return s.held() == ConnectionsMax })
	if n := s.held(); n != ConnectionsMax {
		t.Errorf("%d connections open after %d were dialled, want the cap of %d", n, flood, ConnectionsMax)
	}

	// And the goroutines with them. One read loop per connection kept, plus the
	// handful the daemon runs on its own account, against one per connection
	// dialled if nothing dropped them.
	waitFor(t, "the dropped connections' goroutines ending", func() bool {
		return runtime.NumGoroutine()-before < ConnectionsMax+64
	})
	if grew := runtime.NumGoroutine() - before; grew >= ConnectionsMax+64 {
		t.Errorf("%d connections were dialled and the daemon grew by %d goroutines, past the %d the cap allows",
			flood, grew, ConnectionsMax+64)
	}
}

// And the cap is safe to have because of what happens at it: the connection
// that has gone longest without asking anything is dropped, and the one that
// has just arrived gets in.
//
// This is the whole reason the bound is shaped this way. Refusing the newest is
// the obvious thing and it is the one thing that must not happen here, because
// the newest connection may be the shell dialling again after a switch
// restarted zded - and a shell that cannot reconnect is the failure
// fix/bar-redial exists to prevent, reached from the other side.
//
// So: a full table of connections that are only open, and then a shell. It
// subscribes, and it is drawn to.
func TestAShellDialingIntoAFullTableStillGetsIn(t *testing.T) {
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)
	path := serve(t, s)

	idle := dialPeers(t, path, ConnectionsMax)
	waitFor(t, "the table filling", func() bool { return s.held() == ConnectionsMax })

	shell := dialPeer(t, path)
	if line := shell.ask(t, `{"method":"events"}`); !strings.Contains(line, "listening") {
		t.Fatalf("a shell dialling into a full table was answered %q", strings.TrimSpace(line))
	}
	if n := s.listeners(); n != 1 {
		t.Fatalf("%d listeners after the shell subscribed, want 1", n)
	}

	// Not merely admitted: drawn to. A connection that is in the table and gets
	// no events is a shell that is dark, which is the failure with a different
	// name on it.
	if sent := s.broadcast(Event{Kind: EventPicker, Desks: []string{"vshop"}}); sent != 1 {
		t.Fatalf("a broadcast reached %d listeners, want the shell", sent)
	}
	line, err := shell.hear(10 * time.Second)
	if err != nil {
		t.Fatalf("the shell got no event: %v", err)
	}
	if !strings.Contains(line, EventPicker) {
		t.Errorf("the shell was sent %q, want a picker", strings.TrimSpace(line))
	}

	// And the one that paid for it is the one that had gone longest without
	// asking anything, which is the first of the idle ones.
	// It is told why (see the test below) and then it ends.
	if _, err := idle[0].hear(10 * time.Second); err != nil {
		t.Errorf("the longest-idle connection was closed with nothing said: %v", err)
	}
	if _, err := idle[0].hear(2 * time.Second); err == nil {
		t.Error("the longest-idle connection is still open and answering, so the shell got in by going over the cap")
	}
	if n := s.held(); n != ConnectionsMax {
		t.Errorf("%d connections open, want the cap of %d", n, ConnectionsMax)
	}
}

// The one connection that must never be dropped is the one that looks idlest.
//
// A shell holding the event stream says `events` once at login and then reads
// for the rest of the session. Measured on its own traffic it is the quietest
// connection zded has, so "longest without asking anything" finds it first - and
// losing it is the shell going dark, which is what the cap exists to prevent.
//
// It is exempt because listenersMax bounds the listeners separately at 16, so
// the exemption cannot be turned into a way of filling the table. Four times
// the cap arrives after it, and it is still there and still drawn to.
func TestTheEventStreamIsNotWhatGetsDropped(t *testing.T) {
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)
	path := serve(t, s)

	// First, so that nothing else in the table has been quiet for as long.
	shell := dialPeer(t, path)
	if line := shell.ask(t, `{"method":"events"}`); !strings.Contains(line, "listening") {
		t.Fatalf("the shell was answered %q", strings.TrimSpace(line))
	}

	const flood = ConnectionsMax * 4
	for i := 0; i < flood; i++ {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		defer c.Close()
	}
	waitFor(t, "the table settling at the cap", func() bool { return s.held() == ConnectionsMax })

	if n := s.listeners(); n != 1 {
		t.Fatalf("%d listeners after %d connections arrived, want the shell's one", n, flood)
	}
	if sent := s.broadcast(Event{Kind: EventPicker, Desks: []string{"vshop"}}); sent != 1 {
		t.Fatalf("a broadcast reached %d listeners after the flood, want the shell", sent)
	}
	line, err := shell.hear(10 * time.Second)
	if err != nil {
		t.Fatalf("the event stream was dropped under %d connections: %v", flood, err)
	}
	if !strings.Contains(line, EventPicker) {
		t.Errorf("the shell was sent %q, want a picker", strings.TrimSpace(line))
	}
}

// Nor is a connection with a tier running on it, which is the same trap on a
// shorter clock. An ask.run is answered over as long as a model takes, up to
// askTimeout of two minutes, and for the whole of it the connection has nothing
// more to send - so by the measure this cap uses it is idle, while a person is
// sitting in front of it waiting for the answer.
//
// Bounded the same way the listeners are: asksMax allows four across the daemon
// (ask.go, claimAsk), so the exemption is worth four connections of 256.
func TestATierRunningIsNotDroppedToMakeRoom(t *testing.T) {
	writeTiers(t, map[string][]string{TierLocal: fakeTier("slow")})
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	path := serve(t, s)

	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodAskRun, nil, TierLocal, "how long"); err != nil {
		t.Fatalf("ask.run: %v", err)
	}

	// The flood, while the tier is still thinking. The fake one sleeps two
	// seconds before it says anything, which is what makes this connection the
	// quietest thing in the table at the moment the table fills.
	const flood = ConnectionsMax * 4
	for i := 0; i < flood; i++ {
		f, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		defer f.Close()
	}
	waitFor(t, "the table settling at the cap", func() bool { return s.held() == ConnectionsMax })

	var b strings.Builder
	for {
		ev, err := c.NextEventBefore(time.Now().Add(20 * time.Second))
		if err != nil {
			t.Fatalf("the answer never arrived, so the connection asking for it was dropped: %v", err)
		}
		if ev.Kind != EventAskText {
			continue
		}
		b.WriteString(ev.Text)
		if ev.Done {
			break
		}
	}
	if got := b.String(); got != "eventually" {
		t.Errorf("the answer was %q, want the tier's", got)
	}
}

// A dropped connection is told why, and that is the difference between a bound
// and a daemon that looks like it has crashed.
//
// A socket that ends under a client is what a crash looks like from the outside,
// and this is not one: zded is running, it has just answered the connection that
// took this one's place, and what the client should do is dial again - which the
// shell does on its own (shell/Dialer.qml) and a person does by running the verb
// again. A bare EOF sends somebody to the journal looking for a daemon that
// never died.
func TestADroppedConnectionIsToldWhyBeforeItEnds(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	path := serve(t, s)

	idle := dialPeers(t, path, ConnectionsMax)
	waitFor(t, "the table filling", func() bool { return s.held() == ConnectionsMax })

	// One more, which is what makes room have to be found.
	dialPeer(t, path)

	line, err := idle[0].hear(10 * time.Second)
	if err != nil {
		t.Fatalf("the longest-idle connection was closed with nothing said: %v", err)
	}
	for _, want := range []string{"make room", strconv.Itoa(ConnectionsMax), "zded is running", "dial again"} {
		if !strings.Contains(line, want) {
			t.Errorf("a dropped connection was told %q, which does not say %q", strings.TrimSpace(line), want)
		}
	}
	// And then it ends, rather than being left half-open for a client to keep
	// writing into.
	if _, err := idle[0].hear(10 * time.Second); err == nil {
		t.Error("the connection was told it had been dropped and is still open")
	}
}

// `zde status` carries the count and what has been dropped, because the
// listener count only half answered this. A flood that opens sockets and never
// subscribes never reaches the listeners at all, so status said "shell no, 0
// listening" on a daemon a second away from being killed by its own descriptor
// limit.
func TestStatusSaysHowManyConnectionsAreOpen(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	path := serve(t, s)

	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var st Status
	if err := c.Call("status", &st); err != nil {
		t.Fatal(err)
	}
	if st.Connections != 1 {
		t.Errorf("status says %d connections on a daemon with one caller, want 1", st.Connections)
	}
	if st.Dropped != 0 {
		t.Errorf("status says %d dropped on a daemon nothing has flooded, want 0", st.Dropped)
	}

	const flood = ConnectionsMax * 2
	for i := 0; i < flood; i++ {
		f, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		defer f.Close()
	}
	waitFor(t, "the table settling at the cap", func() bool { return s.held() == ConnectionsMax })

	// Asked on a connection of its own, because the one above is long since
	// dropped - which is itself the point: the diagnostic still answers.
	c2, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if err := c2.Call("status", &st); err != nil {
		t.Fatal(err)
	}
	if st.Connections != ConnectionsMax {
		t.Errorf("status says %d connections under a flood of %d, want the cap of %d", st.Connections, flood, ConnectionsMax)
	}
	if st.Dropped == 0 {
		t.Error("status says nothing was dropped after a flood that filled the table twice over")
	}
}

// One byte is not asking for anything, and what counts as asking is what decides
// who goes.
//
// The connection that has gone longest without asking anything is the one
// dropped to make room. That was measured on every line the scanner returned,
// stamped before the JSON was looked at - so a connection sending "\n", answered
// "malformed request", was refreshed exactly as if it had asked something.
// Measured against a running daemon: a flood holding the table and writing one
// byte per connection kept every one of its own and named which of the session's
// went, getting targets 130, 7 and 255 each on the first try, and dropping a
// shell three rounds running before it could subscribe.
//
// So: a full table, and the connection at the front of the queue writes a byte.
// It is still the one that goes.
func TestAByteOnAConnectionIsNotAskingForAnything(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	path := serve(t, s)

	idle := dialPeers(t, path, ConnectionsMax)
	waitFor(t, "the table filling", func() bool { return s.held() == ConnectionsMax })

	// A bare newline, and the answer that says the daemon read it and made
	// nothing of it. Read back, so that what follows cannot race the line.
	if line := idle[0].ask(t, ""); !strings.Contains(line, "malformed") {
		t.Fatalf("a bare newline was answered %q, want a malformed request", strings.TrimSpace(line))
	}

	// One more connection, so that somebody has to go.
	dialPeer(t, path)

	line, err := idle[0].hear(10 * time.Second)
	if err != nil {
		t.Fatalf("the connection that had asked nothing since it arrived was kept, so a byte bought its place: %v", err)
	}
	if !strings.Contains(line, "make room") {
		t.Errorf("it was told %q, want the sentence a dropped connection gets", strings.TrimSpace(line))
	}
	// And the one behind it in the queue is untouched, which is the other half:
	// a byte that refreshed the first would have moved the choice on to this one.
	if line := idle[1].ask(t, `{"method":"status"}`); !strings.Contains(line, `"ok"`) {
		t.Errorf("the next connection along was answered %q, so it was dropped instead", strings.TrimSpace(line))
	}
}

// What goes is what the process holding most of the table holds, and the measure
// above is only the tie-break under it.
//
// "Longest without asking anything" can be beaten by asking - a byte before this
// branch, a well-formed line after it - and the connections that have genuinely
// asked nothing are the session's own: a `zde` verb sitting inside its 200ms
// ackWait, the bar between polls. So the first question is not what a connection
// sent. A process holding two hundred connections is holding them however noisy
// it is, and the only way to hold that many while looking thin is to spread them
// over processes, which costs a process each rather than a byte each.
//
// Built rather than dialled, because two processes cannot be arranged from inside
// one: every connection a test dials is held by the test. The socket-level
// version is the flood in a process of its own, below.
func TestWhatGoesIsWhatTheProcessHoldingMostOfTheTableHolds(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	now := time.Now().UnixNano()

	// One program holding all but one of the table, and every connection of it
	// noisy: each has just asked something well-formed.
	s.conns = map[*sink]struct{}{}
	for i := 0; i < ConnectionsMax-1; i++ {
		c := &sink{pid: 4242}
		c.asked.Store(now)
		s.conns[c] = struct{}{}
	}
	// And one of the session's own, quiet since the moment it arrived: a verb
	// waiting inside ackWait to hear that a surface drew.
	quiet := &sink{pid: 7}
	quiet.asked.Store(now - int64(time.Minute))
	s.conns[quiet] = struct{}{}

	out, held := s.admit(&sink{pid: 9})
	if out == nil {
		t.Fatal("a connection arrived at a full table and nothing was dropped")
	}
	if out == quiet {
		t.Fatal("the session's own connection was dropped while one program held 255 of 256: " +
			"a flood that keeps its connections noisy chooses which of the session's goes")
	}
	if out.pid != 4242 {
		t.Errorf("the connection dropped belonged to pid %d, want the program holding most of the table", out.pid)
	}
	if held != ConnectionsMax-1 {
		t.Errorf("it was reported as one of %d from that process, want %d", held, ConnectionsMax-1)
	}
	if _, still := s.conns[out]; still {
		t.Error("the dropped connection is still in the table")
	}
}

// The connection that has just arrived is not the one chosen, and timestamps
// cannot make it the one either.
//
// admit stamped the arriving connection before it took the lock, so a flood
// keeping all of its own connections freshly stamped could make the one waiting
// on that lock the oldest thing in the table by the time it got in - and answer
// it with its own eviction, which is a shell locked out of its own socket by a
// program that dials fast enough. The stamp is under the lock now and the
// arriving connection is skipped in the walk, so this is closed by construction
// rather than by winning a race.
//
// Driven from the far end of it, and with the first key taken out of the way:
// every connection in the table comes from a process of its own, so nothing is
// holding more of it than anything else and the choice is the timestamps alone.
// Each is stamped a second into the future, which is the flood keeping its own
// connections fresh while this one waited to be let in.
func TestTheConnectionThatHasJustArrivedIsNotTheOneDropped(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	ahead := time.Now().Add(time.Second).UnixNano()
	s.conns = map[*sink]struct{}{}
	for i := 0; i < ConnectionsMax; i++ {
		c := &sink{pid: int32(1000 + i)}
		c.asked.Store(ahead)
		s.conns[c] = struct{}{}
	}

	shell := &sink{pid: 7}
	out, _ := s.admit(shell)
	if out == shell {
		t.Fatal("the connection that had just arrived paid for its own slot while 256 others could have, " +
			"so a flood that keeps its own connections stamped keeps everything else out")
	}
	if _, in := s.conns[shell]; !in {
		t.Error("the connection that arrived is not in the table")
	}
}

// And when there is genuinely nothing else to choose it is the one that pays,
// because the alternative is a cap that silently is not one.
//
// It takes a table of 256 listeners and tiers to reach, against the 20 those two
// caps allow between them (events.go, listenersMax; ask.go, asksMax), so this is
// a branch being kept honest rather than one anything reaches.
func TestAConnectionPaysForItsOwnSlotWhenEverythingElseIsExempt(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	s.conns = map[*sink]struct{}{}
	s.subs = map[*sink]struct{}{}
	for i := 0; i < ConnectionsMax; i++ {
		c := &sink{pid: 4242}
		s.conns[c] = struct{}{}
		s.subs[c] = struct{}{}
	}

	k := &sink{pid: 7}
	out, _ := s.admit(k)
	if out != k {
		t.Fatalf("a table of %d listeners took another connection, so the cap is not one", ConnectionsMax)
	}
	if n := s.held(); n != ConnectionsMax {
		t.Errorf("%d connections held, want the cap of %d", n, ConnectionsMax)
	}
}

// floodSocket names the socket a flood should dial, and is what tells the
// process below that it is one.
const floodSocket = "ZDE_TEST_FLOOD_SOCKET"

// TestAFloodInAProcessOfItsOwn is not a test. It is the flood, and it is a
// process because what the test after it is about cannot be arranged from inside
// one: the cap tells a session's five connections from somebody's four hundred
// by the process holding them (see admit), and every connection a test dials is
// held by the test.
//
// It holds twice the cap, keeps dialling so that something is being dropped
// throughout, and on every connection it holds it writes both of the things that
// can pass for being used: a bare byte, which is what used to count, and a real
// `status` request, which is what would count if the fix stopped at "well-formed
// lines only". It reads what comes back, because a client that lets its own
// answers pile up has its connection closed under it (events.go, replyWait).
// The most patient attacker there is, rather than the most obvious one.
func TestAFloodInAProcessOfItsOwn(t *testing.T) {
	path := os.Getenv(floodSocket)
	if path == "" {
		t.Skip("this is the flood, run as a process of its own by the test below it")
	}
	// A ceiling of its own, so that a parent which dies without saying so leaves
	// nothing behind for longer than a test run.
	deadline := time.Now().Add(2 * time.Minute)
	stop := make(chan struct{})
	go func() {
		// The parent closes stdin when it is done with the flood.
		io.Copy(io.Discard, os.Stdin)
		close(stop)
	}()

	const holds = ConnectionsMax * 2
	conns := make([]net.Conn, 0, holds)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for time.Now().Before(deadline) {
		select {
		case <-stop:
			return
		default:
		}
		// A few more each round, so that connections are being dropped to make
		// room throughout rather than only at the start.
		for i := 0; i < 4; i++ {
			if len(conns) >= holds {
				conns[0].Close()
				conns = conns[1:]
			}
			c, err := net.Dial("unix", path)
			if err != nil {
				break
			}
			conns = append(conns, c)
			go io.Copy(io.Discard, c)
		}
		for _, c := range conns {
			// Errors ignored: many of these are connections the daemon has
			// already dropped, which is the flood paying for its own slots.
			c.Write([]byte("\n{\"method\":\"status\"}\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// flood starts one, and makes sure it does not outlive the test that started it.
func flood(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAFloodInAProcessOfItsOwn$", "-test.timeout=3m")
	cmd.Env = append(os.Environ(), floodSocket+"="+path)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the flood: %v", err)
	}
	t.Cleanup(func() {
		in.Close()
		cmd.Process.Kill()
		cmd.Wait()
	})
}

// A shell that has not said what it is yet still gets in, and stays in.
//
// This is the property the cap was written for, through the one moment it did
// not cover. A connection is exempt from being dropped once it is a listener,
// and between accepting it and its `{"method":"events"}` line there is a shell's
// own event loop - about 7ms of table at the dial rate a flood reaches.
// Demonstrated against a running daemon: with 50ms between dialling and
// subscribing, the shell's connection was dropped before it could subscribe,
// three times out of three.
//
// What covers the gap is not patience with a connection that has said nothing,
// which is an exemption an attacker buys by being slow. It is that the flood
// holds hundreds of connections in one process and the shell holds one in
// another, whatever either of them is saying or not saying.
func TestAShellIsNotDroppedInTheGapBeforeItSubscribes(t *testing.T) {
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)
	path := serve(t, s)

	flood(t, path)
	waitFor(t, "the flood filling the table", func() bool { return s.held() == ConnectionsMax })

	// The shell dials, and then does what a shell does before it says anything,
	// which is nothing at all for as long as its own event loop takes. The flood
	// is still arriving and still having connections dropped throughout.
	shell := dialPeer(t, path)
	time.Sleep(200 * time.Millisecond)

	if line := shell.ask(t, `{"method":"events"}`); !strings.Contains(line, "listening") {
		t.Fatalf("a shell that waited before subscribing was answered %q", strings.TrimSpace(line))
	}
	if n := s.listeners(); n != 1 {
		t.Fatalf("%d listeners after the shell subscribed, want 1", n)
	}
	// And it is drawn to, which is the difference between holding a connection
	// and being a shell that is not dark.
	if sent := s.broadcast(Event{Kind: EventPicker, Desks: []string{"vshop"}}); sent != 1 {
		t.Fatalf("a broadcast reached %d listeners under the flood, want the shell", sent)
	}
	line, err := shell.hear(10 * time.Second)
	if err != nil {
		t.Fatalf("the shell got no event: %v", err)
	}
	if !strings.Contains(line, EventPicker) {
		t.Errorf("the shell was sent %q, want a picker", strings.TrimSpace(line))
	}
	if n := s.held(); n != ConnectionsMax {
		t.Errorf("%d connections held under a flood, want the cap of %d", n, ConnectionsMax)
	}
}

// ---- what one connection can make the daemon spend ------------------------

// One connection cannot make the daemon spend a goroutine on every line it
// sends, whatever the line says.
//
// Two methods are answered off the read loop, and rightly: ask.run runs a tier
// and clip.history <id> puts an entry back through wl-copy, so both take as long
// as something outside zde takes and a keypress must not be what waits for them.
// What went off the loop with them was the loop's own back-pressure - one reply
// at a time, on the goroutine that read the line - and nothing replaced it. The
// goroutine was spawned before the request had been looked at, so asksMax and
// sink.asking bounded the work and not the goroutines; and since every refused
// goroutine still ends in a reply, each one parked for up to replyWait against a
// client that is not reading, with a timer apiece.
//
// Measured on a running zded over a real socket, one connection, no tier
// configured and nothing ever started: 400,000 ask.run lines - 17.9 MiB on the
// wire - took the daemon from 3 goroutines to 399,867 and its RSS from 8.7 MB to
// 2.26 GB, and the same in clip.history took it to 399,756 and 2.15 GB. The
// connection cap saw none of it, because it is one connection. The control on
// the same harness is what this test is written to: 400,000 `status` lines,
// answered on the read loop, moved the daemon by nothing and closed the
// connection after 13,107 of them, which is back-pressure working.
//
// What is asserted is that ceiling under the load rather than the constants
// behind it. What the daemon may hold for one connection is its read loop, the
// clipboard put it has in flight, and a share of the asksMax runs - three, at
// today's numbers. A hundred is that with room for the runtime's own workers and
// for a method nobody has written yet that starts one goroutine per connection,
// and it is four thousand times below what one connection bought before.
func TestOneConnectionCannotSpawnAGoroutinePerLine(t *testing.T) {
	// A tier file of this test's own, and an empty one: no tier configured is
	// the case that was measured and the cheapest one for a caller, since every
	// line is refused before anything can be spent on it. It is also what keeps
	// this test off whatever the developer running it has configured.
	writeTiers(t, map[string][]string{})

	// Enough that the old shape is unmistakable - it was one goroutine each -
	// and few enough that the writing is over well inside the deadline below.
	const lines = 20 << 10
	const room = 100

	for _, tc := range []struct{ name, line string }{
		{MethodAskRun, `{"method":"ask.run","args":["provider","hi"]}`},
		{MethodClip, `{"method":"clip.history","args":["1"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
			path := serve(t, s)
			c, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			// Counted from here, so that what is measured is what the lines cost
			// and not what a connection costs.
			before := runtime.NumGoroutine()
			sent := make(chan int, 1)
			go func() {
				w := bufio.NewWriterSize(c, 1<<16)
				n := 0
				for ; n < lines; n++ {
					if _, err := fmt.Fprintln(w, tc.line); err != nil {
						break
					}
				}
				w.Flush() //nolint:errcheck // a short write is the back-pressure, and it is expected here
				sent <- n
			}()

			// Sampled while the lines are arriving rather than after them,
			// because what is wrong here drains on its own: every goroutine the
			// old shape made left at its own replyWait, so a count taken six
			// seconds later was 33 on the daemon that had just been holding
			// 399,867.
			peak := before
			deadline := time.Now().Add(time.Minute)
			for done := false; !done; {
				if n := runtime.NumGoroutine(); n > peak {
					peak = n
				}
				select {
				case <-sent:
					done = true
				case <-time.After(time.Millisecond):
				}
				if !done && time.Now().After(deadline) {
					t.Fatalf("%d lines were still being written a minute later, with the daemon up %d goroutines",
						lines, runtime.NumGoroutine()-before)
				}
			}
			if n := runtime.NumGoroutine(); n > peak {
				peak = n
			}
			if grew := peak - before; grew > room {
				t.Errorf("one connection sending %d lines of %s took the daemon up by %d goroutines, "+
					"past the %d one connection may cost: the read loop is spawning rather than answering",
					lines, tc.name, grew, room)
			}
		})
	}
}

// And the caller is still told, which is the half a bound is worthless without:
// a refusal that arrives as silence is a client waiting for an answer that is
// never coming, and the whole reason ask.run is answered by the connection it
// arrived on is that somebody is sitting in front of it.
func TestAnAskRefusedBeforeItStartsStillSaysWhy(t *testing.T) {
	writeTiers(t, map[string][]string{})
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	path := serve(t, s)

	p := dialPeer(t, path)
	// Twice on the one connection, because a refusal that costs the caller its
	// connection is not one it can act on.
	for i := 0; i < 2; i++ {
		line := p.ask(t, `{"method":"ask.run","args":["provider","hi"]}`)
		if !strings.Contains(line, "no provider tier") {
			t.Fatalf("ask %d on a machine with no tier was answered %q", i, strings.TrimSpace(line))
		}
	}
	// And the connection is still a connection afterwards.
	if line := p.ask(t, `{"method":"status"}`); !strings.Contains(line, `"ok"`) {
		t.Errorf("the connection was answered %q after two refusals", strings.TrimSpace(line))
	}
}

// The connection that pays for its own slot hears the sentence before its
// socket goes.
//
// handle's first deferred statement is conn.Close, so the sentence was being
// said on a goroutine started one line above a return: the close fired
// immediately and took the descriptor out from under the write. Measured with
// the table arranged so that every dial took this branch: 0 of 20 dials heard
// anything and all 20 got a bare EOF - which is the "cap that silently is not
// one" the branch exists to avoid, and a bare EOF sends somebody to the journal
// looking for a daemon that never died.
//
// The table is arranged rather than reached, because reaching it needs 256
// listeners and tiers against the 20 those caps allow between them (events.go,
// listenersMax; ask.go, asksMax). Every dial after that is a real one over the
// real socket, which is the half nothing covered: the test beside this one calls
// admit directly and never goes through handle, so the race lived in the three
// lines between them.
//
// Twenty dials rather than one, because one dial hearing the sentence is a
// scheduler's decision and twenty is a measurement.
func TestTheConnectionThatPaysForItsOwnSlotIsToldBeforeItEnds(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	path := serve(t, s)

	s.mu.Lock()
	s.conns = map[*sink]struct{}{}
	s.subs = map[*sink]struct{}{}
	for i := 0; i < ConnectionsMax; i++ {
		c := &sink{pid: 4242}
		s.conns[c] = struct{}{}
		s.subs[c] = struct{}{}
	}
	s.mu.Unlock()

	const dials = 20
	told, left := 0, 0
	for i := 0; i < dials; i++ {
		p := dialPeer(t, path)
		line, err := p.hear(10 * time.Second)
		if err != nil || !strings.Contains(line, "make room") || !strings.Contains(line, "dial again") {
			continue
		}
		told++
		// And then it ends, because this is not a connection the daemon kept.
		if _, err := p.hear(10 * time.Second); err == nil {
			left++
		}
	}
	if told != dials {
		t.Errorf("%d of %d connections that paid for their own slot were closed with nothing said", dials-told, dials)
	}
	if left != 0 {
		t.Errorf("%d of the %d that were told were left open afterwards", left, told)
	}
}

// The connections the cap has to skip are the ones with a tier running, and
// there are never more of them than there are tiers.
//
// admit skips a connection with sink.asking set, and says why in one sentence:
// "Bounded the same way: asksMax allows four across the whole daemon". That was
// not a bound at all. asking was set before the daemon's own place had been
// claimed and cleared by a deferred store, so it was also set for the whole of
// the refusal a connection gets when there is no place free - and that refusal
// is a reply, which parks for up to replyWait against a client that is not
// reading. Measured against a daemon with a tier configured and 256 connections
// asking and never reading: 103 of them exempt at once, while the number the
// comment names sat at four for the whole run.
//
// What that buys an attacker is the far end of the connection cap: exempt
// connections are the ones admit cannot choose, and a table it cannot choose
// from is a table where the connection dialling in pays for its own slot - which
// is the shell, dialling again after a switch.
//
// So what is asserted is the sentence itself, under the load that broke it:
// however many connections are asking, no more than asksMax of them are exempt.
// The connections do not read, and they send more than a receive buffer holds,
// because a refusal that is taken instantly is a refusal that holds the flag for
// microseconds and proves nothing.
func TestNoMoreConnectionsAreExemptFromTheCapThanThereAreTiers(t *testing.T) {
	// A tier that holds the place it took, the way a model loading weights
	// does, so that the connections behind it meet the daemon at its cap.
	dir := t.TempDir()
	writeTiers(t, map[string][]string{TierProvider: fakeTier("linger", filepath.Join(dir, "tier"))})
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	path := serve(t, s)

	// Eight times asksMax, so that most of them are refused, and enough lines
	// each to fill the socket both ways: about 425 KB of buffer between the two
	// ends, against refusals of about ninety bytes.
	const askers = asksMax * 8
	const lines = 20 << 10
	for i := 0; i < askers; i++ {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		go func() {
			w := bufio.NewWriterSize(c, 1<<16)
			for j := 0; j < lines; j++ {
				if _, err := fmt.Fprintln(w, `{"method":"ask.run","args":["provider","hold on"]}`); err != nil {
					return
				}
			}
			w.Flush() //nolint:errcheck // the daemon not taking it is what this test is about
		}()
	}
	waitFor(t, "the daemon filling every place it runs a tier in", func() bool { return s.asking() == asksMax })

	// Sampled rather than read once, because what was wrong is a window: the
	// flag was set across a reply, so how many connections are inside it at any
	// instant is the scheduler's answer and the peak is the measurement.
	peak, runs := 0, 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n := exemptConns(s); n > peak {
			peak = n
		}
		if n := s.asking(); n > runs {
			runs = n
		}
		time.Sleep(2 * time.Millisecond)
	}
	if peak > asksMax {
		t.Errorf("%d connections were exempt from the connection cap at once while %d tiers ran, "+
			"and admit's whole reason for skipping them is that asksMax bounds how many there can be",
			peak, runs)
	}
	if peak == 0 {
		t.Error("no connection was ever exempt, so this run proved nothing about the bound")
	}
}

// exemptConns is how many of the connections in the table admit would have to
// skip because a tier is running on them (see admit).
func exemptConns(s *Server) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.conns {
		if c.asking.Load() {
			n++
		}
	}
	return n
}
