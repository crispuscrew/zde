package zded

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/journal"
)

// Startup must restore both journal state and the compositor's generated config.
func TestZenSurvivesARestart(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	path := filepath.Join(cfg, "niri", "dynamic.kdl")
	state := filepath.Join(t.TempDir(), "j.jsonl")
	jrn, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	first := New("test", jrn, &fakeCompositor{m: twoDesks()}, nil)
	defer first.Close()
	if response := first.Dispatch(Request{Method: "desk.zen", Args: []string{"toggle"}}); response.Error != "" {
		t.Fatalf("toggling zen: %s", response.Error)
	}
	jrn.Close()
	// Remove the previous file so the second daemon has to recreate it.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	again, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	second := New("test", again, &fakeCompositor{m: twoDesks()}, nil)
	defer second.Close()
	if !zenOf(t, second.Dispatch(Request{Method: "desk.zen"})) {
		t.Error("zen was on but the restarted daemon says off")
	}
	second.SyncRules()
	if got := dynamic(t, path); !strings.Contains(got, "gaps 0") {
		t.Errorf("restarted daemon did not restore zen config:\n%s", got)
	}
}

func TestZenAsksNiriToReadTheConfigNow(t *testing.T) {
	server, compositor, _ := zenServer(t)
	server.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if count := compositor.reloadCalls(); count != 1 {
		t.Errorf("niri was asked to reload %d times, want 1", count)
	}
}

// A failed immediate reload must leave the file available to niri's watcher.
func TestZenSurvivesANiriThatWillNotReload(t *testing.T) {
	server, compositor, path := zenServer(t)
	compositor.reloadErr = errors.New("niri is not answering")
	response := server.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if response.Error != "" {
		t.Errorf("niri would not reload and zen refused: %s", response.Error)
	}
	if got := dynamic(t, path); !strings.Contains(got, "gaps 0") {
		t.Errorf("zen did not write the file niri's watcher reads:\n%s", got)
	}
}

func TestZenIsAConfigNiriAccepts(t *testing.T) {
	binary, err := exec.LookPath("niri")
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
	if output, err := exec.Command(binary, "validate", "-c", path).CombinedOutput(); err != nil {
		t.Errorf("niri refuses zen's included config:\n%s\nwrote:\n%s", output, both)
	}
}
