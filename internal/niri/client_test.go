package niri

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeNiri speaks the protocol niri documents: one JSON request per line, one
// JSON reply per line. Replies are given in order, so a test says what the
// compositor answers and nothing else.
func fakeNiri(t *testing.T, replies ...string) string {
	return fakeNiriAsked(t, nil, replies...)
}

// asked is what a fake niri was sent, kept for the tests where the request is
// the thing worth checking: an action name is a string all the way to the
// compositor, so a typo in one is invisible to Go and shows up only as a niri
// that refuses it. Locked because the connection is served from a goroutine
// and the test reads this from its own.
type asked struct {
	mu    sync.Mutex
	lines []string
}

func (a *asked) add(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lines = append(a.lines, line)
}

func (a *asked) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.lines...)
}

// fakeNiriAsked is fakeNiri that keeps what it was asked. nil records nothing.
func fakeNiriAsked(t *testing.T, sent *asked, replies ...string) string {
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
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			if sent != nil {
				sent.add(strings.TrimSpace(string(line)))
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

func TestScreens(t *testing.T) {
	path := fakeNiri(t, `{"Ok":{"Outputs":{
		"DP-1":{"name":"DP-1","make":"Dell","model":"U2515H","logical":`+logical+`},
		"HDMI-A-1":{"name":"HDMI-A-1","make":"x","model":"y","logical":`+logical+`}
	}}}`)
	got, err := dial(t, path).Screens()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Screens = %v, want two", got)
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "DP-1") || !strings.Contains(joined, "HDMI-A-1") {
		t.Errorf("Screens = %v", got)
	}
}

// logical is what niri puts on an output it is showing something on. Every
// fixture here carries one, because a real niri does: the whole point of the
// field is the outputs that do not have it.
const logical = `{"x":0,"y":0,"width":2560,"height":1440,"scale":1.0,"transform":"Normal"}`

// The lid closes with an external monitor attached and niri switches the laptop
// panel off. The connector still has a crtc, so it is still in the Outputs
// reply - with no logical output, because niri has no layout monitor for it.
// It is not a screen, and reading it as one is what turns a migration into a
// wrongly-detected move.
func TestScreensDropsADisabledOutput(t *testing.T) {
	path := fakeNiri(t, `{"Ok":{"Outputs":{
		"eDP-1":{"name":"eDP-1","current_mode":null,"logical":null},
		"DP-1":{"name":"DP-1","logical":`+logical+`}
	}}}`)
	got, err := dial(t, path).Screens()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "DP-1" {
		t.Errorf("Screens = %v, want only the output niri is showing something on", got)
	}
}

// A niri that says nothing about logical outputs leaves the list empty, and an
// empty list turns renaming off (internal/desk, Rebuild). The failure has a
// safe direction and this is it.
func TestScreensWithoutLogicalIsEmpty(t *testing.T) {
	path := fakeNiri(t, `{"Ok":{"Outputs":{"DP-1":{"name":"DP-1"}}}}`)
	got, err := dial(t, path).Screens()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("Screens = %v, want none rather than a guess", got)
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
		`{"Ok":{"Outputs":{"DP-1":{"name":"DP-1","logical":`+logical+`},`+
			`"HDMI-A-1":{"name":"HDMI-A-1","logical":`+logical+`}}}}`,
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
// list of screens has to reach the desk model intact.
func TestDeskMapUnpluggedMonitor(t *testing.T) {
	path := fakeNiri(t,
		`{"Ok":{"Workspaces":[{"id":1,"idx":1,"name":"vshop.DP-1.code","output":"eDP-1"}]}}`,
		`{"Ok":{"Outputs":{"eDP-1":{"name":"eDP-1","logical":`+logical+`}}}}`,
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

// The lid closes on a docked laptop. niri parks eDP-1's workspaces on DP-1 and
// switches the panel off, but the connector is still in the Outputs reply - so
// zde used to see both monitors present, one workspace sitting on the wrong
// one, and call it a move. The rename that followed wrote DP-1 into the name,
// which was the only record that the workspace belongs to the laptop panel.
func TestDeskMapLidClosedIsNotAMove(t *testing.T) {
	path := fakeNiri(t,
		`{"Ok":{"Workspaces":[{"id":1,"idx":0,"name":"vshop.eDP-1.code","output":"DP-1"}]}}`,
		`{"Ok":{"Outputs":{`+
			`"eDP-1":{"name":"eDP-1","current_mode":null,"logical":null},`+
			`"DP-1":{"name":"DP-1","logical":`+logical+`}}}}`,
	)
	m, err := dial(t, path).DeskMap()
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Renames(); len(got) != 0 {
		t.Errorf("Renames = %v, want none: eDP-1 is off, so this is a migration", got)
	}
	if got := m.Displaced(); len(got) != 1 || got[0].Monitor != "eDP-1" {
		t.Errorf("Displaced = %v, want the workspace with the laptop panel still recorded", got)
	}
}

// The join the window picker is made of: niri says which workspace a window is
// on by id, and a person recognises the workspace by its zde name. Both replies
// are read here so that the two describe one moment.
func TestOpenWindows(t *testing.T) {
	path := fakeNiri(t,
		`{"Ok":{"Windows":[
			{"id":7,"title":"invoice.md","app_id":"nvim","workspace_id":1,"is_focused":true},
			{"id":9,"title":null,"app_id":"foot","workspace_id":2,"is_focused":false},
			{"id":11,"title":"floating","app_id":"foot","workspace_id":null,"is_focused":false}
		]}}`,
		`{"Ok":{"Workspaces":[
			{"id":1,"idx":1,"name":"vshop.DP-1.code","output":"DP-1","is_focused":true},
			{"id":2,"idx":2,"name":null,"output":"DP-1","is_focused":false}
		]}}`,
	)
	got, err := dial(t, path).OpenWindows()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("OpenWindows = %+v, want three", got)
	}
	want := OpenWindow{ID: 7, Title: "invoice.md", AppID: "nvim", Workspace: "vshop.DP-1.code"}
	if got[0] != want {
		t.Errorf("first window = %+v, want %+v", got[0], want)
	}
	// A workspace nothing has named yet is not a workspace called "2": the id
	// means nothing outside the compositor, so the column is left empty and the
	// window is still in the list.
	if got[1].Workspace != "" || got[1].AppID != "foot" {
		t.Errorf("window on an unnamed workspace = %+v", got[1])
	}
	// A title niri reports as null is not the string "null", and a window on no
	// workspace at all is still open.
	if got[2].Title != "floating" || got[2].Workspace != "" {
		t.Errorf("window on no workspace = %+v", got[2])
	}
	if got[1].Title != "" {
		t.Errorf("title = %q, want empty for a window niri gave none", got[1].Title)
	}
}

// The action, not the reply: FocusWindow is a name and an id in a JSON object,
// and getting either wrong reaches niri as a request it refuses. Nothing in Go
// would notice, so this is where it is noticed.
func TestFocusWindowAsksForThatId(t *testing.T) {
	var sent asked
	path := fakeNiriAsked(t, &sent, `{"Ok":"Handled"}`)
	if err := dial(t, path).FocusWindow(9); err != nil {
		t.Fatal(err)
	}
	lines := sent.all()
	if len(lines) != 1 {
		t.Fatalf("niri was asked %v, want one action", lines)
	}
	if !strings.Contains(lines[0], `"FocusWindow"`) {
		t.Errorf("asked %s, want niri's FocusWindow action", lines[0])
	}
	if !strings.Contains(lines[0], `"id":9`) {
		t.Errorf("asked %s, want the id to focus", lines[0])
	}
}

// An action niri did not handle is not a jump that happened.
func TestFocusWindowNotHandled(t *testing.T) {
	path := fakeNiri(t, `{"Err":"no such window"}`)
	err := dial(t, path).FocusWindow(9)
	if err == nil || !strings.Contains(err.Error(), "no such window") {
		t.Errorf("got %v, want niri's refusal", err)
	}
}

// The palette runs a niri native by the name its bind gives it, and niri wants
// that name in its own spelling. Convert it wrongly and every native row in the
// palette reaches the compositor as an action it has never heard of - which no
// Go type can catch, because both spellings are strings.
func TestPerformSpellsTheActionTheWayNiriDoes(t *testing.T) {
	var sent asked
	path := fakeNiriAsked(t, &sent, `{"Ok":"Handled"}`)
	if err := dial(t, path).Perform("consume-window-into-column"); err != nil {
		t.Fatal(err)
	}
	lines := sent.all()
	if len(lines) != 1 {
		t.Fatalf("niri was asked %v, want one action", lines)
	}
	if !strings.Contains(lines[0], `{"Action":{"ConsumeWindowIntoColumn":{}}}`) {
		t.Errorf("asked %s, want niri's ConsumeWindowIntoColumn", lines[0])
	}
}

// And a name niri does not know comes back as niri's refusal rather than as a
// palette row that appeared to work. That is what lets Perform derive the
// spelling instead of keeping a table of twenty of them.
func TestPerformCarriesNirisRefusal(t *testing.T) {
	path := fakeNiri(t, `{"Err":"unknown action"}`)
	err := dial(t, path).Perform("frobnicate-window")
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("got %v, want niri's refusal", err)
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
	path := fakeNiri(t, `{"Ok":{"Outputs":{"DP-1":{"name":"DP-1","logical":`+logical+`}}}}`)
	t.Setenv(SocketEnv, path)
	c, err := Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, err := c.Screens(); err != nil || len(got) != 1 {
		t.Errorf("Screens = %v, %v", got, err)
	}
}
