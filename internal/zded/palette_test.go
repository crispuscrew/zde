package zded

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A machine's cheatsheet, where the palette looks for the keys: the file the
// config build writes beside the binds niri loaded (nix/zde-config.nix).
func withCheatsheet(t *testing.T, text string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zde", "keymap.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
}

// A PATH with `zde` on it. Whether the program behind a row is on this machine
// is half of what makes a row live, and a test run from a tree nobody has
// installed would see every zde row as missing - a true answer to a question
// these tests are not asking.
func withZdeOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zde"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// A server whose spawns are recorded rather than run: what the palette starts
// is the thing worth asserting, and a test should not have to start a terminal
// to see it.
type spy struct {
	mu   sync.Mutex
	argv [][]string
}

func (s *spy) run(argv []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.argv = append(s.argv, append([]string(nil), argv...))
	return nil
}

func (s *spy) all() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.argv...)
}

func paletteServer(t *testing.T) (*Server, *fakeCompositor, *spy) {
	t.Helper()
	withZdeOnPath(t)
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	s := New("test", nil, f, nil)
	sp := &spy{}
	s.spawn = sp.run
	return s, f, sp
}

func list(t *testing.T, s *Server) Palette {
	t.Helper()
	resp := s.Dispatch(Request{Method: "palette.list"})
	if resp.Error != "" {
		t.Fatalf("palette.list: %s", resp.Error)
	}
	var p Palette
	if err := json.Unmarshal(resp.Ok, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func row(t *testing.T, p Palette, name string) Action {
	t.Helper()
	for _, a := range p.Actions {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no %s row in the palette", name)
	return Action{}
}

// Running an action from the palette must do what its key does, argument and
// all. If the argv is assembled differently here than in the bind, then the
// palette is a second way to run things that quietly does something else - and
// the one place a person would notice is a terminal opening in the wrong
// directory.
func TestPaletteRunsWhatTheBindRuns(t *testing.T) {
	withCheatsheet(t, "launch\n  Mod+t\tapp.launch terminal\tlaunch terminal\n")
	s, _, sp := paletteServer(t)

	if resp := s.Dispatch(Request{Method: "palette.run", Args: []string{"app.launch terminal"}}); resp.Error != "" {
		t.Fatalf("palette.run: %s", resp.Error)
	}
	got := sp.all()
	if len(got) != 1 || strings.Join(got[0], " ") != "zde app launch terminal" {
		t.Fatalf("the palette spawned %v, want what Mod+t spawns", got)
	}
}

// A niri native goes to niri, by the name the bind gives it. Spawning it
// instead would look for a program called focus-column-left, and nothing on the
// machine has one.
func TestPaletteRunsANativeThroughNiri(t *testing.T) {
	withCheatsheet(t, "window\n  Mod+h\twindow.focus left\tfocus the column to the left\n")
	s, f, sp := paletteServer(t)

	if resp := s.Dispatch(Request{Method: "palette.run", Args: []string{"window.focus left"}}); resp.Error != "" {
		t.Fatalf("palette.run: %s", resp.Error)
	}
	if got := f.performCalls(); len(got) != 1 || got[0] != "focus-column-left" {
		t.Errorf("niri was asked %v, want the action the bind names", got)
	}
	if got := sp.all(); len(got) != 0 {
		t.Errorf("a niri native was also spawned as a command: %v", got)
	}
}

// The honesty. Most of the keymap is bound to commands nobody has written, and
// the key does nothing and says nothing. A palette that ran one of those rows
// the same way would be the same silence with an extra step, so it refuses and
// says why - and it refuses before anything is started.
func TestPaletteRefusesWhatIsNotWrittenYet(t *testing.T) {
	withCheatsheet(t, "desk\n  Mod+Shift+Escape\tdesk.panic\tpanic: decoy desk, mute, silence\n")
	s, _, sp := paletteServer(t)

	resp := s.Dispatch(Request{Method: "palette.run", Args: []string{"desk.panic"}})
	if resp.Error == "" {
		t.Fatal("the palette ran an action with nothing behind it")
	}
	if !strings.Contains(resp.Error, "nothing is written behind it") {
		t.Errorf("refusal is %q, and it has to say why nothing happened", resp.Error)
	}
	if got := sp.all(); len(got) != 0 {
		t.Errorf("it refused and started %v anyway", got)
	}
}

// And it says so on the row as well, with the key still beside it: somebody who
// has forgotten a key is better served by "not written yet, and here is the key
// it will be" than by a list that pretends the action does not exist.
func TestPaletteMarksTheSilentRowsAndKeepsThem(t *testing.T) {
	withCheatsheet(t, "desk\n"+
		"  Mod+Tab\tdesk.switcher\topen the desk switcher\n"+
		"  Mod+Shift+Escape\tdesk.panic\tpanic: decoy desk, mute, silence\n")
	s, _, _ := paletteServer(t)
	p := list(t, s)

	panicRow := row(t, p, "desk.panic")
	if panicRow.Live {
		t.Error("desk.panic reads as working, and pressing its key does nothing at all")
	}
	if panicRow.Why == "" {
		t.Error("a row that cannot run says nothing about why")
	}
	if panicRow.Key != "Mod+Shift+Escape" {
		t.Errorf("the silent row lost its key: %+v", panicRow)
	}
	if live := row(t, p, "desk.switcher"); !live.Live || live.Why != "" {
		t.Errorf("desk.switcher reads as %+v, and Mod+Tab works", live)
	}
}

// The other half of a dead row: the command is written and the program is not
// on this machine. zlg is zinc's launcher and lives in layer 2, so a machine
// without it has Mod+g doing nothing - and the row has to name the program,
// because "it did nothing" and "you have not installed zinc" look identical
// from a keyboard.
func TestPaletteNamesTheProgramThatIsMissing(t *testing.T) {
	withCheatsheet(t, "launch\n  Mod+g\tlauncher.open\tzlg, the app launcher\n")
	s, _, sp := paletteServer(t) // a PATH with zde on it and nothing else

	got := row(t, list(t, s), "launcher.open")
	if got.Live || !strings.Contains(got.Why, "zlg") {
		t.Errorf("launcher.open reads as %+v on a machine with no zlg", got)
	}
	if resp := s.Dispatch(Request{Method: "palette.run", Args: []string{"launcher.open"}}); resp.Error == "" {
		t.Error("the palette started a program this machine does not have")
	}
	if started := sp.all(); len(started) != 0 {
		t.Errorf("it refused and started %v anyway", started)
	}
}

// The three screenshots are the rows this is most worth being right about. The
// key takes a screenshot, and niri refuses the same action asked for over the
// socket - it wants a field the bind does not carry and the config parser fills
// in. So the palette must not offer them, and must go on showing the key, which
// is the thing that works. Marked live, this was the palette doing the exact
// silent-key trick it exists to expose: pick "screenshot a region", watch the
// surface close, and nothing happens.
func TestPaletteWillNotClaimToTakeAScreenshot(t *testing.T) {
	withCheatsheet(t, "capture\n  Mod+Print\tcapture.shot-full\tscreenshot the whole output\n")
	s, f, _ := paletteServer(t)

	got := row(t, list(t, s), "capture.shot-full")
	if got.Live {
		t.Error("the palette offers a screenshot, and niri answers that request with an error")
	}
	if got.Key != "Mod+Print" {
		t.Errorf("the row stopped teaching the key that does work: %+v", got)
	}
	if resp := s.Dispatch(Request{Method: "palette.run", Args: []string{"capture.shot-full"}}); resp.Error == "" {
		t.Error("palette.run took a screenshot request niri would refuse")
	}
	if asked := f.performCalls(); len(asked) != 0 {
		t.Errorf("niri was asked %v anyway", asked)
	}
}

// The key on a row comes from the file the config build wrote, because that
// file and the binds niri actually loaded came out of one build. Answering from
// the registry instead would be a palette confidently teaching a chord this
// machine does not have.
func TestPaletteTakesTheKeysFromTheCheatsheet(t *testing.T) {
	withCheatsheet(t, "desk\n  Mod+F9\tdesk.switcher\topen the desk switcher\n")
	s, _, _ := paletteServer(t)

	if got := row(t, list(t, s), "desk.switcher").Key; got != "Mod+F9" {
		t.Errorf("desk.switcher key = %q, want the one this machine has bound", got)
	}
}

// With no cheatsheet at all - a zde built by hand, or one whose home-manager
// module has never been activated - the actions are still there and only the
// keys are not. The palette is the surface somebody reaches for when the rest
// of the session is unwell, so it must not be the one that refuses.
func TestPaletteWorksWithNoCheatsheet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _, _ := paletteServer(t)

	p := list(t, s)
	if len(p.Actions) < 20 {
		t.Fatalf("the palette has %d rows without a cheatsheet, want every action", len(p.Actions))
	}
	if got := row(t, p, "desk.switcher"); got.Key != "" || !got.Live {
		t.Errorf("desk.switcher = %+v, want an action with no key it can name", got)
	}
}

// An action the file has and this build does not is left out rather than shown:
// nothing here knows what is behind it, and a row that can only fail is worse
// than one that is missing.
func TestPaletteLeavesOutWhatThisBuildDoesNotKnow(t *testing.T) {
	withCheatsheet(t, "desk\n  Mod+y\tdesk.teleport\tsomething a later zde has\n")
	s, _, _ := paletteServer(t)

	for _, a := range list(t, s).Actions {
		if a.Name == "desk.teleport" {
			t.Fatal("the palette lists an action it cannot run")
		}
	}
	resp := s.Dispatch(Request{Method: "palette.run", Args: []string{"desk.teleport"}})
	if resp.Error == "" || !strings.Contains(resp.Error, "no action called") {
		t.Errorf("running an unknown action answered %q", resp.Error)
	}
}

// The palette is the one surface that has to work while the compositor does
// not: the list is a file and a compiled-in registry, and `zde doctor` is on it
// by name. A niri that answers nothing must cost the screen it would have
// appeared on, and nothing else.
func TestPaletteListsWithoutACompositor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := &fakeCompositor{err: errClosed}
	s := New("test", nil, f, nil)

	p := list(t, s)
	if len(p.Actions) == 0 {
		t.Fatal("no compositor and no actions either")
	}
	row(t, p, "system.doctor")
}

// The event carries the rows, so the surface can draw without asking a second
// question - the same bargain the picker's event makes.
func TestPaletteTellsAListener(t *testing.T) {
	withCheatsheet(t, "desk\n  Mod+Tab\tdesk.switcher\topen the desk switcher\n")
	s, _, _ := paletteServer(t)
	rec := &recorder{}
	s.listen(&sink{w: rec})

	p := list(t, s)
	if p.Shown {
		t.Error("nobody acknowledged the event and the answer claims a palette was shown")
	}
	var got struct{ Event Event }
	line := strings.TrimSpace(rec.String())
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the event is not one line of json: %q", line)
	}
	if got.Event.Kind != EventPalette {
		t.Errorf("kind = %q, want the palette's own kind", got.Event.Kind)
	}
	if len(got.Event.Actions) != len(p.Actions) {
		t.Errorf("the event carries %d rows and the answer has %d", len(got.Event.Actions), len(p.Actions))
	}
	if got.Event.Token == "" {
		t.Error("the event carries no token, so nothing can say it drew the surface")
	}
}
