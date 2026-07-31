package zded

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
)

// twoWindows is one window on each desk of twoDesks, which is what makes
// "jumping crossed a desk" a claim that can fail.
func twoWindows() []Window {
	return []Window{
		{ID: 7, Title: "invoice.md", AppID: "nvim", Workspace: "vshop.DP-1.code"},
		{ID: 9, Title: "psql", AppID: "foot", Workspace: "haven.DP-1.db"},
	}
}

// The picker is a surface somebody else draws, so the verb's job is to tell
// them - and the event has to carry everything a row needs, because a surface
// that has to ask before it can draw appears in two steps.
func TestJumpTellsAListener(t *testing.T) {
	f := &fakeCompositor{
		m:       twoDesks(),
		focused: "vshop.DP-1.code",
		output:  "DP-1",
		windows: twoWindows(),
	}
	s := New("test", nil, f, nil)

	rec := &recorder{}
	s.listen(&sink{w: rec})

	resp := s.Dispatch(Request{Method: "window.jump-to"})
	if resp.Error != "" {
		t.Fatalf("window.jump-to: %s", resp.Error)
	}
	var j Jump
	if err := json.Unmarshal(resp.Ok, &j); err != nil {
		t.Fatal(err)
	}
	// Nothing acknowledged, so nothing was shown: a listener that takes the
	// bytes and draws nothing is the case that used to report success.
	if j.Shown {
		t.Error("nobody acknowledged the event and the answer claims a picker was shown")
	}

	var got struct{ Event Event }
	line := strings.TrimSpace(rec.String())
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the event is not one line of json: %q", line)
	}
	if got.Event.Kind != EventWindows {
		t.Errorf("kind = %q, want %q", got.Event.Kind, EventWindows)
	}
	if got.Event.Output != "DP-1" {
		t.Errorf("output = %q, want the screen being looked at", got.Event.Output)
	}
	if got.Event.Token == "" {
		t.Error("the event carries no token, so nothing can say it drew the surface")
	}
	if len(got.Event.Windows) != 2 {
		t.Fatalf("event windows = %+v, want both", got.Event.Windows)
	}
	// Every field a row is made of, asserted one by one: a window you cannot
	// tell from the one below it is a row you cannot choose, and a missing
	// workspace is a list that does not say which desk anything is on.
	want := Window{ID: 7, Title: "invoice.md", AppID: "nvim", Workspace: "vshop.DP-1.code"}
	if got.Event.Windows[1] != want {
		t.Errorf("window = %+v, want %+v", got.Event.Windows[1], want)
	}
}

// With nothing listening the verb must not pretend: the caller prints the list
// itself, and only knows to because of this field.
func TestJumpSaysWhenNothingIsListening(t *testing.T) {
	s := New("test", nil, &fakeCompositor{
		m:       twoDesks(),
		focused: "vshop.DP-1.code",
		windows: twoWindows(),
	}, nil)

	resp := s.Dispatch(Request{Method: "window.jump-to"})
	if resp.Error != "" {
		t.Fatalf("window.jump-to: %s", resp.Error)
	}
	var j Jump
	json.Unmarshal(resp.Ok, &j)
	if j.Shown {
		t.Error("nothing was listening and the answer claims the picker was shown")
	}
	if len(j.Windows) != 2 {
		t.Errorf("windows = %+v, and there is no list to print either", j.Windows)
	}
}

// Nothing open is a refusal that says so. An empty surface would have to be
// dismissed before the key could do anything else, which is a worse answer than
// a line of text.
func TestJumpWithNothingOpen(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	rec := &recorder{}
	s.listen(&sink{w: rec})

	resp := s.Dispatch(Request{Method: "window.jump-to"})
	if resp.Error == "" || !strings.Contains(resp.Error, "nothing is open") {
		t.Errorf("got %q, want a refusal that says why there is nothing to pick", resp.Error)
	}
	if rec.String() != "" {
		t.Errorf("asked a shell to draw an empty picker: %q", rec.String())
	}
}

// The rows have to sit still. niri's order follows the strip and the stacking,
// both of which move as you work, so a list taken straight from it would put a
// different window under the same digit each time - and the digits are the
// fastest way to a window you can see.
func TestJumpListIsInAStableOrder(t *testing.T) {
	s := New("test", nil, &fakeCompositor{
		m:       twoDesks(),
		focused: "vshop.DP-1.code",
		windows: []Window{
			{ID: 9, AppID: "foot", Workspace: "vshop.DP-1.code"},
			{ID: 3, AppID: "nvim", Workspace: "vshop.DP-1.code"},
			{ID: 5, AppID: "psql", Workspace: "haven.DP-1.db"},
		},
	}, nil)

	var j Jump
	json.Unmarshal(s.Dispatch(Request{Method: "window.jump-to"}).Ok, &j)
	var order []uint64
	for _, w := range j.Windows {
		order = append(order, w.ID)
	}
	// haven before vshop, and within one workspace the older window first.
	if !slices.Equal(order, []uint64{5, 3, 9}) {
		t.Errorf("order = %v, want the list grouped by workspace and then by id", order)
	}
}

// Choosing goes to that window. The id it left focused is the whole assertion:
// counting windows, or checking that something is focused, passes just as well
// when the jump lands on the wrong one.
func TestJumpToFocusesThatWindow(t *testing.T) {
	niri := &fakeCompositor{
		m:             twoDesks(),
		focused:       "vshop.DP-1.code",
		output:        "DP-1",
		focusedWindow: 7,
		windows:       twoWindows(),
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var focused []string
	if err := c.Call("window.jump-to", &focused, "9"); err != nil {
		t.Fatal(err)
	}
	if got := niri.focusedWindowID(); got != 9 {
		t.Errorf("window %d is focused, want the one that was asked for", got)
	}
	if !slices.Equal(focused, []string{"haven.DP-1.db"}) {
		t.Errorf("answered %v, want the workspace the window is on", focused)
	}
}

// A window on another desk is on a workspace only that desk's monitor is
// showing, so the desk has to come up the way a switch brings it up. Without
// that, one screen shows haven and the rest still show vshop - a session on two
// desks at once, which the model has no name for.
func TestJumpToAnotherDeskBringsTheWholeDeskUp(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()

	// Two of haven's workspaces on the far monitor, with the window on the
	// second. The switch enters that monitor on the first, so what the journal
	// ends up holding is the jump's own correction and not the switch's answer -
	// which is the difference this test would otherwise not be able to see.
	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "haven.DP-1.db", Output: "DP-1"},
			{Name: "haven.HDMI-A-1.logs", Output: "HDMI-A-1"},
			{Name: "haven.HDMI-A-1.notes", Output: "HDMI-A-1"},
		}, []string{"DP-1", "HDMI-A-1"}),
		focused:       "vshop.DP-1.code",
		output:        "DP-1",
		focusedWindow: 7,
		windows: []Window{
			{ID: 7, AppID: "nvim", Workspace: "vshop.DP-1.code"},
			{ID: 9, AppID: "psql", Workspace: "haven.HDMI-A-1.notes"},
		},
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Call("window.jump-to", nil, "9"); err != nil {
		t.Fatal(err)
	}
	// Every monitor haven owns, not only the one the window is on.
	if got := niri.focusCalls(); !slices.Equal(got, []string{"haven.DP-1.db", "haven.HDMI-A-1.logs"}) {
		t.Errorf("focused %v, want haven on every monitor it owns", got)
	}
	// And the window itself, after the desk came up rather than before it: a
	// switch focuses that desk's landing workspace, so the other order would
	// leave the jump one workspace short of where it was going.
	if got := niri.focusedWindowID(); got != 9 {
		t.Errorf("window %d is focused, want the one that was jumped to", got)
	}
	// Where the desk was left, so that coming back lands on the window you
	// jumped to rather than on the workspace the switch passed through
	// (invariant 5).
	st := jrn.State()
	if st.OnDesk != "haven" {
		t.Errorf("on desk %q after jumping to a window on haven", st.OnDesk)
	}
	if got := st.LastActive["haven"]["HDMI-A-1"]; got != "notes" {
		t.Errorf("haven's last active on HDMI-A-1 is %q, want where the jump ended", got)
	}
	if st.LastDesk != "vshop" {
		t.Errorf("last desk %q, want the desk jumped out of", st.LastDesk)
	}
}

// A window on the desk you are on is one focus and nothing else. Switching to
// the desk you are already on is not the no-op it looks like: a switch restores
// that desk's landing workspace, so it would scroll the other monitors off
// whatever they were showing on the way to a window on this one.
func TestJumpToOnThisDeskMovesNothingElse(t *testing.T) {
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()

	niri := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "vshop.DP-1.notes", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused:       "vshop.DP-1.code",
		output:        "DP-1",
		focusedWindow: 7,
		windows: []Window{
			{ID: 7, AppID: "nvim", Workspace: "vshop.DP-1.code"},
			{ID: 8, AppID: "foot", Workspace: "vshop.DP-1.notes"},
		},
	}
	s := New("test", jrn, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Call("window.jump-to", nil, "8"); err != nil {
		t.Fatal(err)
	}
	if got := niri.focusCalls(); len(got) != 0 {
		t.Errorf("focused %v on the way to a window on this desk", got)
	}
	if got := niri.focusedWindowID(); got != 8 {
		t.Errorf("window %d is focused, want 8", got)
	}
	// And the desk remembers the workspace the jump ended on. Nothing else
	// writes it here - no switch ran - so this is the jump's own bookkeeping or
	// none at all, and without it desk.last comes back to the workspace you
	// jumped away from.
	if got := jrn.State().LastActive["vshop"]["DP-1"]; got != "notes" {
		t.Errorf("vshop's last active is %q, want where the jump ended", got)
	}
}

// A window on a workspace nothing has named belongs to no desk, so there is no
// desk to bring up first - and refusing would make the one workspace adoption
// exists for the one place a jump cannot reach.
func TestJumpToAWindowOnAnUnnamedWorkspace(t *testing.T) {
	niri := &fakeCompositor{
		m:             twoDesks(),
		focused:       "vshop.DP-1.code",
		output:        "DP-1",
		focusedWindow: 7,
		windows:       []Window{{ID: 4, AppID: "foot"}},
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var focused []string
	if err := c.Call("window.jump-to", &focused, "4"); err != nil {
		t.Fatal(err)
	}
	if got := niri.focusedWindowID(); got != 4 {
		t.Errorf("window %d is focused, want the unnamed workspace's window", got)
	}
	if len(focused) != 0 {
		t.Errorf("answered %v, want nothing: there is no workspace name to say", focused)
	}
}

// A window that closed between the list and the choice. The picker is on screen
// for as long as somebody takes to read it, so this is ordinary rather than
// exotic, and it has to say what happened rather than pass niri's answer about
// an id nobody typed.
func TestJumpToAWindowThatIsGone(t *testing.T) {
	niri := &fakeCompositor{
		m:       twoDesks(),
		focused: "vshop.DP-1.code",
		windows: twoWindows(),
	}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	err = c.Call("window.jump-to", nil, "404")
	if err == nil || !strings.Contains(err.Error(), "not open any more") {
		t.Errorf("got %v, want a refusal that says the window has gone", err)
	}
	if got := niri.focusCalls(); len(got) != 0 {
		t.Errorf("focused %v on the way to a window that is not there", got)
	}
}

// The socket is a wire anything can write to, so an id that is not one is a
// refusal in words about what to send instead.
func TestJumpToNeedsAnId(t *testing.T) {
	niri := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", windows: twoWindows()}
	s := New("test", nil, niri, nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, arg := range []string{"nvim", "", "-1", "7.5"} {
		err := c.Call("window.jump-to", nil, arg)
		if err == nil || !strings.Contains(err.Error(), "wants the id from the list") {
			t.Errorf("jump-to %q: got %v, want a refusal about the id", arg, err)
		}
	}
	if err := c.Call("window.jump-to", nil, "7", "9"); err == nil {
		t.Error("window.jump-to with two ids was accepted")
	}
	if got := niri.focusedWindowID(); got != 0 {
		t.Errorf("focused window %d, want nothing focused by a refused request", got)
	}
}
