package zded

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

// zenServer is a daemon with somewhere to remember a toggle and somewhere to
// write niri's config, which is the whole of what zen needs.
func zenServer(t *testing.T) (*Server, *fakeCompositor, string) {
	t.Helper()
	// XDG_CONFIG_HOME, so dynamicPath lands in a directory this test owns
	// rather than in the config directory of whoever is running it.
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	f := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	return New("test", jrn, f, manifest.Dir(t.TempDir())), f, filepath.Join(cfg, "niri", "dynamic.kdl")
}

// zenOf reads the answer a desk.zen call gave back.
func zenOf(t *testing.T, r Response) bool {
	t.Helper()
	if r.Error != "" {
		t.Fatalf("desk.zen: %s", r.Error)
	}
	var z Zen
	if err := json.Unmarshal(r.Ok, &z); err != nil {
		t.Fatal(err)
	}
	return z.Zen
}

func dynamic(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("niri's dynamic config: %v", err)
	}
	return string(b)
}

// All three, because the glossary names all three: hide bar, borders, gaps.
//
// The bar is the shell's half. These two are niri's, and niri 26.04 has no IPC
// action for either - so what proves they are hidden is what is in the file
// niri reads, and it has to be the whole set. A zen that took the gaps and left
// the focus ring is a screen with a coloured rectangle around the content it
// was supposed to be showing on its own.
func TestZenHidesTheBordersAndTheGaps(t *testing.T) {
	s, _, path := zenServer(t)
	if r := s.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}}); r.Error != "" {
		t.Fatalf("turning zen on: %s", r.Error)
	}
	got := dynamic(t, path)
	for _, want := range []string{"gaps 0", "focus-ring {\n        off\n    }", "border {\n        off\n    }"} {
		if !strings.Contains(got, want) {
			t.Errorf("zen is on and niri's config has no %q:\n%s", want, got)
		}
	}
}

// And nothing at all when it is off, which is what puts the person's own values
// back. There is no "gaps 8" to write here: what niri/config.kdl and the host's
// local.kdl say is whatever they say, and zen restoring a number of its own
// would be zen deciding what the desktop looks like the rest of the time.
func TestZenOffLeavesNiriItsOwnLayout(t *testing.T) {
	s, _, path := zenServer(t)
	s.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if r := s.Dispatch(Request{Method: "desk.zen", Args: []string{"off"}}); r.Error != "" {
		t.Fatalf("turning zen off: %s", r.Error)
	}
	if got := dynamic(t, path); strings.Contains(got, "layout") {
		t.Errorf("zen is off and niri's config still has a layout section in it:\n%s", got)
	}
}

// The file is one file, and the two things in it have to survive each other. A
// zen toggle that dropped the placement rules would send every pinned app back
// to wherever niri felt like putting it, silently, at the next launch.
func TestZenKeepsThePlacementRules(t *testing.T) {
	s, _, path := zenServer(t)
	yaml := "name: vshop\nmonitors: { DP-1: { workspaces: [web] } }\n" +
		"apps: [{ app: browser, app_id: org.mozilla.firefox, monitor: DP-1, workspace: web }]\n"
	if err := os.WriteFile(filepath.Join(string(s.desks.(manifest.Dir)), "vshop.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	got := dynamic(t, path)
	if !strings.Contains(got, "open-on-workspace \"vshop.DP-1.web\"") {
		t.Errorf("turning zen on dropped the placement rules:\n%s", got)
	}
	if !strings.Contains(got, "gaps 0") {
		t.Errorf("the placement rules dropped zen:\n%s", got)
	}
}

// Nothing in that file may be a bind. dynamic.kdl is included by the config
// that carries the keymap, and niri replaces a bind of the same chord with the
// last one it read (niri-config, the binds section) - so a `binds` node here
// would be zen able to take the panic key, the lock key, or the key that hands
// the keyboard back. Zen is comfort; it does not get to touch those.
func TestZenWritesNoBinds(t *testing.T) {
	s, _, path := zenServer(t)
	s.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if got := dynamic(t, path); strings.Contains(got, "binds") {
		t.Errorf("zen wrote something about binds into the config that carries the keymap:\n%s", got)
	}
}

// The toggle survives the shell, which is the whole reason it does not live
// there: the shell is restarted by every home-manager switch, and so is zded.
//
// A second Server over the same journal is what both of those look like from
// here - the daemon's memory is gone and the file is not - and it has to come
// back in zen, and put niri back into it without being asked.
func TestZenSurvivesARestart(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	path := filepath.Join(cfg, "niri", "dynamic.kdl")
	state := filepath.Join(t.TempDir(), "j.jsonl")

	jrn, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	first := New("test", jrn, &fakeCompositor{m: twoDesks()}, nil)
	if r := first.Dispatch(Request{Method: "desk.zen", Args: []string{"toggle"}}); r.Error != "" {
		t.Fatalf("toggling zen: %s", r.Error)
	}
	jrn.Close()
	// And the file gone with it, so that what the new daemon writes is the new
	// daemon's doing and not the old one's leftovers.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	again, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	second := New("test", again, &fakeCompositor{m: twoDesks()}, nil)
	if !zenOf(t, second.Dispatch(Request{Method: "desk.zen"})) {
		t.Error("zen was on, the daemon restarted, and it came back saying zen is off")
	}
	// SyncRules is what startup calls, and it is what has to make niri agree
	// again: without it the state would be right and the screen would have its
	// borders back.
	second.SyncRules()
	if got := dynamic(t, path); !strings.Contains(got, "gaps 0") {
		t.Errorf("a restarted daemon did not put zen back into niri's config:\n%s", got)
	}
}

// The half of the toggle that is not a file: niri is asked to read it now.
//
// Without this the borders go when niri's own watcher next polls, which is up
// to half a second after the key (internal/niri, ReloadConfig). That is the
// difference between a key and a key that seems not to have worked.
func TestZenAsksNiriToReadTheConfigNow(t *testing.T) {
	s, f, _ := zenServer(t)
	s.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if n := f.reloadCalls(); n != 1 {
		t.Errorf("niri was asked to reload %d times, want 1", n)
	}
}

// And a niri that will not is half a second of delay rather than a failure: the
// file is written either way and niri's watcher is still watching it. A zen
// that refused here would leave the state recorded, the file written, and the
// caller told it did not happen.
func TestZenSurvivesANiriThatWillNotReload(t *testing.T) {
	s, f, path := zenServer(t)
	f.reloadErr = errors.New("niri is not answering")
	r := s.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if r.Error != "" {
		t.Errorf("niri would not reload and zen refused: %s", r.Error)
	}
	if got := dynamic(t, path); !strings.Contains(got, "gaps 0") {
		t.Errorf("niri would not reload and zen did not write the file its watcher reads:\n%s", got)
	}
}

// The shell is told, and told to ask rather than told the answer.
//
// The event carries no state on purpose: the reply to desk.zen is the one place
// this fact is published, so a shell that missed an event polls its way back to
// the truth instead of holding a second copy that can be wrong.
func TestZenTellsTheShell(t *testing.T) {
	s, _, _ := zenServer(t)
	path := serve(t, s)
	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodEvents, nil); err != nil {
		t.Fatal(err)
	}

	asker, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer asker.Close()
	var z Zen
	if err := asker.Call("desk.zen", &z, "on"); err != nil {
		t.Fatal(err)
	}
	if !z.Zen {
		t.Error("desk.zen on came back saying off")
	}

	ev, err := c.NextEventBefore(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatalf("nothing told the shell that the chrome changed: %v", err)
	}
	if ev.Kind != EventZen {
		t.Errorf("the shell was sent a %q event, want %q", ev.Kind, EventZen)
	}
}

// What zen must not do. It hides chrome, and chrome is not what tells somebody
// something needs them: the mode is what decides that (docs/vision.md,
// principle 3), and zen leaves it exactly where it found it. A zen that reached
// for quiet would be a comfort key that silences a session, with the one line
// that would have explained the silence hidden by the same keypress.
func TestZenChangesNoDisplayPolicy(t *testing.T) {
	s, _, _ := zenServer(t)
	if r := s.Dispatch(Request{Method: "attn.mode", Args: []string{"focus"}}); r.Error != "" {
		t.Fatal(r.Error)
	}
	before := s.mode()
	s.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if got := s.mode(); got != before {
		t.Errorf("turning zen on changed the attn mode from %q to %q", before, got)
	}
	// And an arrival still gets to interrupt. The popup is the surface W6 pairs
	// with zen in the first place - clean media view until something needs me -
	// so it is the one thing that must keep coming through.
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodEvents, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Arrived(attn.Notification{From: "somebody", Text: "the build fell over", Urgent: true}); err != nil {
		t.Fatal(err)
	}
	ev, err := c.NextEventBefore(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatalf("zen is on and an urgent arrival drew nothing: %v", err)
	}
	if ev.Kind != EventAttnPopup {
		t.Errorf("zen is on and the arrival came through as a %q event, want %q", ev.Kind, EventAttnPopup)
	}
}

// The words the request takes, and the one it does not. Nothing spells a state
// as "yes" or "1", so a request that did would be a caller with a different
// idea of this protocol - and answering it by guessing which direction they
// meant is how a toggle ends up flipping when a script meant to set it.
func TestZenRefusesAStateItDoesNotKnow(t *testing.T) {
	s, _, _ := zenServer(t)
	if r := s.Dispatch(Request{Method: "desk.zen", Args: []string{"yes"}}); r.Error == "" {
		t.Error("desk.zen took a state that is not on, off or toggle")
	}
	if r := s.Dispatch(Request{Method: "desk.zen", Args: []string{"on", "off"}}); r.Error == "" {
		t.Error("desk.zen took two states at once")
	}
}

// The bar's half, read where it is written. QML is a file nothing here
// compiles, so this is a scan and not a run - the same bargain the rest of this
// package makes with shell/ (wire_test.go, qml_test.go) - and what it pins is
// the one binding that decides whether the strip is on the screen.
//
// Three names have to be in it and each is a different failure if it is not.
// `root.zen` is the toggle doing anything at all. `root.zenKnown` is the failure
// pointing the right way: a bar hidden because zded stopped answering cannot be
// brought back, since zded is the only thing that could. `micState.live` is the
// promise zen makes about what it will not hide - a room being heard is not
// chrome, and a comfort toggle that could take that off the strip would be a way
// to hide a warning.
func TestTheBarGoesInZenAndComesBackForTheMicrophone(t *testing.T) {
	src := string(readShell(t))
	// The binding on the panel, not any `visible:` in the file: every surface
	// this shell draws has one.
	visible := regexp.MustCompile(`(?m)^\s+visible: (.*)$`)
	var found string
	for _, m := range visible.FindAllStringSubmatch(src, -1) {
		if strings.Contains(m[1], "zen") {
			found = m[1]
		}
	}
	if found == "" {
		t.Fatalf("nothing in the bar's `visible` binding mentions zen, so Mod+Shift+z leaves the strip on the screen")
	}
	for _, want := range []string{"root.zen", "root.zenKnown", "micState.live"} {
		if !strings.Contains(found, want) {
			t.Errorf("the bar is shown when %q and that binding does not read %s", found, want)
		}
	}
}

// And the shell never decides it. zded owns the toggle because the shell is
// restarted by every home-manager switch; a shell that wrote the state down
// would be the copy that comes back wrong.
//
// What that means in the file is that every mention of desk.zen is a bare
// question. `{"method":"desk.zen","args":["on"]}` from here would be a bar with
// an opinion, and the first thing it would do on connecting is argue with the
// daemon about a key nobody pressed.
func TestTheBarAsksAboutZenAndNeverSetsIt(t *testing.T) {
	src := string(readShell(t))
	asks := regexp.MustCompile(`\{"method":"desk\.zen"[^}]*\}`).FindAllString(src, -1)
	if len(asks) == 0 {
		t.Fatal("the bar never asks about zen, so a restarted shell has no way to find out")
	}
	for _, ask := range asks {
		if ask != `{"method":"desk.zen"}` {
			t.Errorf("the bar sends %s, which sets zen rather than reading it", ask)
		}
	}
}

// The whole file, through niri's own parser, for the reason the placement rules
// go through it: the layout section is a shape somebody worked out from another
// project's source, and dynamic.kdl is included by the config that carries the
// binds. A block niri refuses is a machine with no keybinds, and it would be
// written the moment somebody pressed Mod+Shift+z.
//
// It skips without niri, which the placement test pays for the same way: niri
// is in the devShell CI runs go test inside, and the VM smoke test runs a real
// one with no skip in it at all.
func TestZenIsAConfigNiriAccepts(t *testing.T) {
	bin, err := exec.LookPath("niri")
	if err != nil {
		t.Skip("no niri on PATH: nix/tests/smoke.nix is the copy of this that cannot skip")
	}
	both := dynamicKDL(true, desks(t,
		"name: haven\nmonitors: { DP-1: { workspaces: [web] } }\n"+
			"apps: [{ app: browser, app_id: org.mozilla.firefox, monitor: DP-1, workspace: web }]\n"))
	path := filepath.Join(t.TempDir(), "dynamic.kdl")
	if err := os.WriteFile(path, []byte(both), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "validate", "-c", path).CombinedOutput(); err != nil {
		t.Errorf("niri will not load what zen writes, so the config that includes it is refused "+
			"and the machine has no binds:\n%s\nwrote:\n%s", out, both)
	}
}
