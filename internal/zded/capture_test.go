package zded

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

// captureServer is a daemon with a journal to remember a decision in, a place to
// write niri's config, and one focused window to press the key on.
func captureServer(t *testing.T, appID string) (*Server, *fakeCompositor, string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	f := &fakeCompositor{
		m:             twoDesks(),
		focused:       "vshop.DP-1.code",
		output:        "DP-1",
		focusedWindow: 7,
		windows:       []Window{{ID: 7, AppID: appID, Title: "a window", Workspace: "vshop.DP-1.code"}},
	}
	return New("test", jrn, f, manifest.Dir(t.TempDir())), f, filepath.Join(cfg, "niri", "dynamic.kdl")
}

// blockOf reads the answer a window.capture-block call gave back.
func blockOf(t *testing.T, r Response) CaptureBlock {
	t.Helper()
	if r.Error != "" {
		t.Fatalf("window.capture-block: %s", r.Error)
	}
	var b CaptureBlock
	if err := json.Unmarshal(r.Ok, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func block(s *Server, args ...string) Response {
	return s.Dispatch(Request{Method: "window.capture-block", Args: args})
}

// The rule niri needs, whole. Three parts and each is a different failure: the
// match anchored on the focused window's app id, or it blocks something else;
// block-out-from, or it blocks nothing; and the app id quoted for the regex, or
// a dot in org.mozilla.firefox matches every app on the machine.
func TestCaptureBlockWritesTheRuleThatBlocksTheWindow(t *testing.T) {
	s, _, path := captureServer(t, "org.mozilla.firefox")
	if r := block(s, "on"); r.Error != "" {
		t.Fatalf("blocking the focused window: %s", r.Error)
	}
	got := dynamic(t, path)
	for _, want := range []string{
		`match app-id="^org\\.mozilla\\.firefox$"`,
		`block-out-from "screen-capture"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the window is blocked and niri's config has no %s:\n%s", want, got)
		}
	}
}

// The value, on its own, because it is the whole of what this action promises.
//
// niri takes two words and they nest: `screencast` blocks only niri's PipeWire
// casting, `screen-capture` blocks every render target that is not the physical
// output - the cast, wlr-screencopy, and niri's automatic screenshot actions
// (src/render_helpers/mod.rs, should_block_out). vision.md section 3 says the
// default blocks all capture paths, so the weaker word here would be a window
// somebody blocked that grim still reads, with nothing anywhere saying so.
func TestCaptureBlockBlocksEveryPathAndNotOnlyTheCast(t *testing.T) {
	s, _, path := captureServer(t, "org.keepassxc.KeePassXC")
	block(s, "on")
	got := dynamic(t, path)
	if !strings.Contains(got, `block-out-from "screen-capture"`) {
		t.Errorf("the block is not screen-capture, so something still reads the window:\n%s", got)
	}
	if strings.Contains(got, `block-out-from "screencast"`) {
		t.Errorf("the block is the cast only, and a screenshot tool still reads the window:\n%s", got)
	}
}

// One verb both ways, and off has to take the rule out of the file rather than
// only out of the journal: a rule left behind is a window that stays black on
// every capture with nothing left saying why.
func TestCaptureBlockOffTakesTheRuleOut(t *testing.T) {
	s, _, path := captureServer(t, "org.mozilla.firefox")
	block(s, "on")
	if b := blockOf(t, block(s, "off")); b.Blocked {
		t.Error("window.capture-block off came back saying blocked")
	}
	if got := dynamic(t, path); strings.Contains(got, "block-out-from") {
		t.Errorf("the block was lifted and niri's config still blocks the window:\n%s", got)
	}
}

// And toggle goes both ways off the same word, which is what a key spawns.
func TestCaptureBlockTogglesBothWays(t *testing.T) {
	s, _, _ := captureServer(t, "org.mozilla.firefox")
	if b := blockOf(t, block(s, "toggle")); !b.Blocked {
		t.Fatal("the first toggle left the window unblocked")
	}
	if b := blockOf(t, block(s, "toggle")); b.Blocked {
		t.Error("the second toggle left the window blocked, so the key is one-way")
	}
}

// It survives the daemon, which is the reason it is not in the daemon's memory:
// zded is restarted by every home-manager switch, and a block that a rebuild
// quietly lifted is a window handed to the next screencast with nothing on the
// screen different either way.
func TestCaptureBlockSurvivesARestart(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	path := filepath.Join(cfg, "niri", "dynamic.kdl")
	state := filepath.Join(t.TempDir(), "j.jsonl")
	windows := []Window{{ID: 7, AppID: "org.keepassxc.KeePassXC", Workspace: "vshop.DP-1.code"}}

	jrn, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	first := New("test", jrn, &fakeCompositor{m: twoDesks(), focusedWindow: 7, windows: windows}, nil)
	if r := block(first, "toggle"); r.Error != "" {
		t.Fatalf("blocking the window: %s", r.Error)
	}
	jrn.Close()
	// And the file gone with it, so what the new daemon writes is the new
	// daemon's doing and not the old one's leftovers.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	again, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	second := New("test", again, &fakeCompositor{m: twoDesks(), focusedWindow: 7, windows: windows}, nil)
	if b := blockOf(t, block(second)); !b.Blocked {
		t.Error("the window was blocked, the daemon restarted, and it came back saying it is not")
	}
	// SyncRules is what startup calls, and it is what has to make niri agree
	// again: without it the state is right and every capture sees the window.
	second.SyncRules()
	if got := dynamic(t, path); !strings.Contains(got, "block-out-from") {
		t.Errorf("a restarted daemon did not put the block back into niri's config:\n%s", got)
	}
}

// The file is one file and three things live in it. A capture block that
// dropped the placement rules would send every pinned app back to wherever niri
// felt like putting it, and one that dropped zen would put the borders back on
// a screen somebody had cleared.
func TestCaptureBlockKeepsZenAndThePlacementRules(t *testing.T) {
	s, _, path := captureServer(t, "org.keepassxc.KeePassXC")
	yaml := "name: vshop\nmonitors: { DP-1: { workspaces: [web] } }\n" +
		"apps: [{ app: browser, app_id: org.mozilla.firefox, monitor: DP-1, workspace: web }]\n"
	if err := os.WriteFile(filepath.Join(string(s.desks.(manifest.Dir)), "vshop.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	block(s, "on")
	got := dynamic(t, path)
	for _, want := range []string{"gaps 0", `open-on-workspace "vshop.DP-1.web"`, "block-out-from"} {
		if !strings.Contains(got, want) {
			t.Errorf("blocking a window dropped %q from niri's config:\n%s", want, got)
		}
	}
}

// hostileAppID is what an application may call itself. niri's IPC hands the
// app id over as the client set it (docs/vision.md, ask 1), so this is the one
// string in dynamic.kdl a sandboxed app writes.
const hostileAppID = "evil\" \n binds { Mod+Shift+Escape { spawn \"sh\" \"-c\" \"true\"; } }\n//"

// Nothing this writes may be a bind. dynamic.kdl is included by the config that
// carries the keymap, and niri keeps the last bind it read for a chord - so a
// `binds` node here is an application taking the panic key by naming itself
// after one.
func TestCaptureBlockWritesNoBinds(t *testing.T) {
	s, _, path := captureServer(t, hostileAppID)
	if r := block(s, "on"); r.Error != "" {
		t.Fatalf("blocking a window with a hostile app id: %s", r.Error)
	}
	got := dynamic(t, path)
	if !strings.Contains(got, "block-out-from") {
		t.Fatalf("the block was not written at all, so this proves nothing:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "binds") {
			t.Errorf("an app id put a binds node into the config that carries the keymap:\n%s", got)
		}
	}
}

// And niri has to take the file, which is the other half of the same failure: a
// config niri refuses is a config niri drops whole, and the binds go with it.
//
// Skips without niri, the way the zen and placement tests do: niri is in the
// devShell CI runs go test inside.
func TestCaptureBlockIsAConfigNiriAccepts(t *testing.T) {
	bin, err := exec.LookPath("niri")
	if err != nil {
		t.Skip("no niri on PATH: nix/tests/smoke.nix is the copy of this that cannot skip")
	}
	all := dynamicKDL(true, []string{"org.mozilla.firefox", hostileAppID, "app\tid\x00"}, desks(t,
		"name: haven\nmonitors: { DP-1: { workspaces: [web] } }\n"+
			"apps: [{ app: browser, app_id: org.mozilla.firefox, monitor: DP-1, workspace: web }]\n"))
	path := filepath.Join(t.TempDir(), "dynamic.kdl")
	if err := os.WriteFile(path, []byte(all), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "validate", "-c", path).CombinedOutput(); err != nil {
		t.Errorf("niri will not load what a capture block writes, so the config that includes it is refused "+
			"and the machine has no binds:\n%s\nwrote:\n%s", out, all)
	}
}

// What goes into the rule is the app id as niri gave it, and what is printed is
// that string made safe for a terminal. The two must not be swapped: a cleaned
// app id in the rule is a rule matching nothing, which is this action failing in
// the one direction it must not fail in - somebody told a window is blocked
// while every capture still reads it.
func TestCaptureBlockMatchesTheRawAppIDAndPrintsASafeOne(t *testing.T) {
	const raw = "ev\x1b[2Jil"
	s, _, path := captureServer(t, raw)
	b := blockOf(t, block(s, "on"))
	if strings.ContainsRune(b.AppID, '\x1b') {
		t.Errorf("the app id came back with an escape in it, and it is printed to a terminal: %q", b.AppID)
	}
	if got := dynamic(t, path); !strings.Contains(got, `\u{1b}`) {
		t.Errorf("the rule was written against a cleaned app id, so it matches no window:\n%s", got)
	}
}

// A window that told niri nothing about what it is. There is no pattern to
// write, and an empty one is a rule that matches every window on the machine -
// so this says so rather than blacking out the session or doing nothing quietly.
func TestCaptureBlockRefusesAWindowWithNoAppID(t *testing.T) {
	s, _, path := captureServer(t, "")
	r := block(s, "on")
	if r.Error == "" {
		t.Fatal("a window with no app id was blocked, which is a rule with an empty pattern")
	}
	if _, err := os.Stat(path); err == nil {
		if got := dynamic(t, path); strings.Contains(got, "block-out-from") {
			t.Errorf("the refusal still wrote a rule:\n%s", got)
		}
	}
}

// Reading it back, which is how somebody confirms a window is blocked without
// taking a screenshot: the reply names the focused window's app id and says
// which way it is, and lists everything else that is blocked.
func TestCaptureBlockReadsBackWhatIsBlocked(t *testing.T) {
	s, f, _ := captureServer(t, "org.mozilla.firefox")
	if b := blockOf(t, block(s)); b.Blocked || b.AppID != "org.mozilla.firefox" {
		t.Errorf("a fresh session read back %+v, want the focused app id and not blocked", b)
	}
	block(s, "on", "org.keepassxc.KeePassXC")
	b := blockOf(t, block(s))
	if b.Blocked {
		t.Error("blocking one app said the focused window is blocked too")
	}
	if len(b.All) != 1 || b.All[0] != "org.keepassxc.KeePassXC" {
		t.Errorf("the list of what is blocked is %v, want the one app that is", b.All)
	}
	// And with nothing focused it still answers the half it can. Somebody
	// looking for what they blocked is often looking at a desk with nothing on
	// it.
	f.focusedWindow = 0
	if b := blockOf(t, block(s)); len(b.All) != 1 || b.AppID != "" {
		t.Errorf("with nothing focused it read back %+v, want no app id and the list", b)
	}
}

// Naming an app id is how a block is lifted after the window it was put on is
// closed. Without it the only way back is editing the journal by hand, which is
// a decision somebody made that they cannot unmake.
func TestCaptureBlockLiftsABlockFromAnAppWhoseWindowIsGone(t *testing.T) {
	s, f, path := captureServer(t, "org.keepassxc.KeePassXC")
	block(s, "on")
	f.focusedWindow = 0
	f.windows = nil
	if r := block(s, "off", "org.keepassxc.KeePassXC"); r.Error != "" {
		t.Fatalf("lifting a block by name with the window closed: %s", r.Error)
	}
	if got := dynamic(t, path); strings.Contains(got, "block-out-from") {
		t.Errorf("the block was lifted by name and the rule is still in niri's config:\n%s", got)
	}
}

// niri is asked to read the file now. Without it the block arrives at niri's
// next poll, up to half a second later (internal/niri, ReloadConfig) - and a
// half second is a frame of the screencast the window was being hidden from.
func TestCaptureBlockAsksNiriToReadTheConfigNow(t *testing.T) {
	s, f, _ := captureServer(t, "org.mozilla.firefox")
	block(s, "on")
	if n := f.reloadCalls(); n != 1 {
		t.Errorf("niri was asked to reload %d times, want 1", n)
	}
}

// And a niri that will not reload is that delay rather than a failure: the file
// is written and niri's own watcher is still watching it.
func TestCaptureBlockSurvivesANiriThatWillNotReload(t *testing.T) {
	s, f, path := captureServer(t, "org.mozilla.firefox")
	f.reloadErr = errors.New("niri is not answering")
	if r := block(s, "on"); r.Error != "" {
		t.Errorf("niri would not reload and the block refused: %s", r.Error)
	}
	if got := dynamic(t, path); !strings.Contains(got, "block-out-from") {
		t.Errorf("niri would not reload and the rule its watcher reads was not written:\n%s", got)
	}
}

// The words it takes and the ones it does not. Nothing spells a state as "yes",
// so a request that did is a caller with a different idea of this protocol - and
// guessing which direction they meant is how a window ends up unblocked by a
// script that meant to block it.
func TestCaptureBlockRefusesAStateItDoesNotKnow(t *testing.T) {
	s, _, _ := captureServer(t, "org.mozilla.firefox")
	for _, args := range [][]string{
		{"yes"},
		{"screencast"},
		{"on", "a", "b"},
	} {
		if r := block(s, args...); r.Error == "" {
			t.Errorf("window.capture-block took %v", args)
		}
	}
}

// With no journal there is nowhere to remember the decision, so it refuses
// rather than blocking a window until the next login. The daemon runs without
// one only when the state file could not be opened at all, and a block that
// quietly expires is worse than no block: nothing on the screen marks either.
func TestCaptureBlockRefusesWithNowhereToRememberIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := New("test", nil, &fakeCompositor{
		m: twoDesks(), focusedWindow: 7,
		windows: []Window{{ID: 7, AppID: "org.mozilla.firefox"}},
	}, nil)
	if r := block(s, "on"); r.Error == "" {
		t.Error("a daemon with no journal blocked a window it cannot remember blocking")
	}
}
