package zded

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/manifest"
)

// lockServer is a daemon with two desks in niri, a locker this machine knows
// how to run, and spawns that are recorded rather than started - so the lock
// can be watched without taking the screen of whoever is running the tests.
//
// preset is what goes in lock.json, and "" writes no file at all: a machine
// that has never set one is one of the cases this action has to survive.
func lockServer(t *testing.T, preset string, desks map[string]string) (*Server, *fakeCompositor, *spy) {
	t.Helper()
	withZdeOnPath(t)
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	// What `zde system lock` resolves against, and the reason lockPreset can
	// know there is something to lock with before it moves anything.
	writeJSON(t, filepath.Join(cfg, "zde", "apps.json"), map[string][]string{
		"lock": {"true"},
	})
	if preset != "" {
		writeJSON(t, filepath.Join(cfg, "zde", "lock.json"), map[string]string{"preset": preset})
	}

	dir := t.TempDir()
	for name, body := range desks {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// clinic is up in niri like the other two, so that the only thing standing
	// between lock-preset and a private desk is the check for one. A private
	// desk that had no workspaces would fail the switch anyway and the test
	// would pass on a daemon with no such check in it.
	f := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "haven.DP-1.db", Output: "DP-1"},
			{Name: "clinic.DP-1.mail", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "vshop.DP-1.code",
		output:  "DP-1",
	}
	s := New("test", nil, f, manifest.Dir(dir))
	t.Cleanup(func() { s.Close() })
	sp := &spy{}
	s.spawn = sp.run
	return s, f, sp
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The whole feature is the order. If the lock starts while the switch is still
// on its way, the desk you were on is the desk the lock screen is over and the
// desk an unlock reveals - which is the thing W23 exists to stop, and it fails
// invisibly: the switch does happen, a moment too late.
//
// So the assertion is made from inside the spawn: at the instant the locker is
// started, niri must already have been told to focus the preset desk. A test
// that looked afterwards would pass on the broken ordering too.
//
// The mutation: run the lock before switchToPreset. Every other test here still
// passes; this one fails.
func TestTheDeskHasChangedBeforeTheLockerStarts(t *testing.T) {
	s, f, _ := lockServer(t, "haven", nil)

	var mu sync.Mutex
	var focusedWhenLocked []string
	s.spawn = func(argv []string) error {
		mu.Lock()
		defer mu.Unlock()
		focusedWhenLocked = f.focusCalls()
		return nil
	}

	resp := s.Dispatch(Request{Method: "system.lock-preset"})
	if resp.Error != "" {
		t.Fatalf("system.lock-preset: %s", resp.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(focusedWhenLocked) == 0 {
		t.Fatal("the locker started before niri had been asked for anything, so the lock screen is over the desk you were on")
	}
	if focusedWhenLocked[len(focusedWhenLocked)-1] != "haven.DP-1.db" {
		t.Errorf("when the locker started niri had been asked for %v, want the preset desk", focusedWhenLocked)
	}
}

// And it is the same locker every other route to a lock uses. A second idea of
// what locks this screen is how a machine ends up with a key that locks and a
// menu that does not (power.go, powerRun).
func TestLockPresetRunsTheCommandTheLockKeyRuns(t *testing.T) {
	s, _, sp := lockServer(t, "haven", nil)

	if resp := s.Dispatch(Request{Method: "system.lock-preset"}); resp.Error != "" {
		t.Fatalf("system.lock-preset: %s", resp.Error)
	}
	got := sp.all()
	if len(got) != 1 || strings.Join(got[0], " ") != "zde system lock" {
		t.Fatalf("lock-preset spawned %v, want what the lock key spawns", got)
	}
}

// No preset set is the ordinary state of a machine nobody has configured, and
// it locks. Falling back to locking where you are gives up the preset; refusing
// to lock gives up the lock, and this is the action somebody presses on the way
// out of a room.
//
// The note is the other half: a lock that quietly did half its job is a feature
// somebody believes they have.
//
// The mutation: return an error instead of the note. The screen then stays
// unlocked on every machine that has not set the option.
func TestWithNoPresetItStillLocksAndSaysWhatItDidNotDo(t *testing.T) {
	s, f, sp := lockServer(t, "", nil)

	resp := s.Dispatch(Request{Method: "system.lock-preset"})
	if resp.Error != "" {
		t.Fatalf("system.lock-preset with nothing configured: %s", resp.Error)
	}
	if len(sp.all()) != 1 {
		t.Fatalf("the screen was not locked: %v", sp.all())
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v with no preset set, and there was no desk to switch to", got)
	}
	if !strings.Contains(resp.Note, "zde.lock.preset") {
		t.Errorf("the note reads %q, and it should name the option to set", resp.Note)
	}
}

// A preset naming a desk that is gone - renamed, deleted, never made - locks
// too, and names the desk. The thing to do about it is to fix the name, and
// that is not discoverable from a screen that has just locked.
//
// The mutation: refuse the lock when the switch fails. One stale line in a
// config then leaves the machine unlocked.
func TestAPresetNamingADeskThatIsGoneStillLocks(t *testing.T) {
	s, _, sp := lockServer(t, "ghost", nil)

	resp := s.Dispatch(Request{Method: "system.lock-preset"})
	if resp.Error != "" {
		t.Fatalf("system.lock-preset with a missing desk: %s", resp.Error)
	}
	if len(sp.all()) != 1 {
		t.Fatalf("the screen was not locked: %v", sp.all())
	}
	if !strings.Contains(resp.Note, "ghost") {
		t.Errorf("the note reads %q, and it should name the desk that is not there", resp.Note)
	}
}

// A private desk is out of the picker, popups off, capture-blocked
// (docs/vision.md, section 3). Switching away from one is exactly what this
// action is for; switching to one would put the desk with the most to hide on
// the screen an unlock reveals and behind the lock a shoulder is reading.
//
// The mutation: drop the private check. lock-preset then happily unlocks onto
// the one desk whose whole point is not being seen.
func TestAPrivateDeskIsNotSomethingToUnlockOnto(t *testing.T) {
	s, f, sp := lockServer(t, "clinic", map[string]string{
		"clinic": "name: clinic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n",
	})

	resp := s.Dispatch(Request{Method: "system.lock-preset"})
	if resp.Error != "" {
		t.Fatalf("system.lock-preset with a private preset: %s", resp.Error)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v, and a private desk is not what an unlock should show", got)
	}
	if len(sp.all()) != 1 {
		t.Fatalf("the screen was not locked: %v", sp.all())
	}
	if !strings.Contains(resp.Note, "private") {
		t.Errorf("the note reads %q, and it should say why the switch did not happen", resp.Note)
	}
}

// And the private check fails closed. A manifest that will not parse is the
// ordinary state of a directory somebody edits by hand, and the switch does not
// need a manifest to happen - so a check that read "cannot tell" as "not
// private" would switch to a private desk on exactly the machine where nobody
// can see that it did (docs/vision.md, principle 9).
//
// The mutation: read the preset's manifest through manifestFor, which drops the
// error, so an unreadable directory and a desk nothing declares answer the same.
// The switch goes through.
func TestAManifestThatWillNotParseStopsTheSwitchRatherThanTheCheck(t *testing.T) {
	s, f, sp := lockServer(t, "haven", map[string]string{
		"broken": "name: haven\nmonitorz: nope\n",
	})

	resp := s.Dispatch(Request{Method: "system.lock-preset"})
	if resp.Error != "" {
		t.Fatalf("system.lock-preset beside a broken manifest: %s", resp.Error)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v while nothing could say whether that desk is private", got)
	}
	if len(sp.all()) != 1 {
		t.Fatalf("the screen was not locked: %v", sp.all())
	}
	if !strings.Contains(resp.Note, "broken.yaml") {
		t.Errorf("the note reads %q, and it should name the file to fix", resp.Note)
	}
}

// Nothing to lock with is the one failure that stops this action, and it stops
// it before anything moves. The alternative is a session walked off its desk and
// left unlocked on another one, which is worse than a key that does nothing.
//
// The mutation: switch first, then discover there is no locker. The desk has
// changed and the screen is open.
func TestWithNoLockerNothingIsSwitchedAndTheRefusalSaysSo(t *testing.T) {
	s, f, sp := lockServer(t, "haven", nil)
	cfg := os.Getenv("XDG_CONFIG_HOME")
	writeJSON(t, filepath.Join(cfg, "zde", "apps.json"), map[string][]string{
		"terminal": {"true"},
	})

	resp := s.Dispatch(Request{Method: "system.lock-preset"})
	if resp.Error == "" {
		t.Fatalf("with no locker configured, lock-preset answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "lock") {
		t.Errorf("the refusal reads %q, and it should say what is missing", resp.Error)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v by a lock that could not happen", got)
	}
	if got := sp.all(); len(got) != 0 {
		t.Errorf("something was spawned anyway: %v", got)
	}
}

// The desk is a line in a config and never an argument. A desk name typed after
// this verb is a name to get wrong at the moment somebody is leaving the room.
func TestLockPresetTakesNoArguments(t *testing.T) {
	s, _, _ := lockServer(t, "haven", nil)

	if resp := s.Dispatch(Request{Method: "system.lock-preset", Args: []string{"haven"}}); resp.Error == "" {
		t.Errorf("system.lock-preset haven answered %s, and there is no such spelling", resp.Ok)
	}
}
