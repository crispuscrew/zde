package zded

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

// Give zen its own journal and config directory, never the host's niri config.
func zenServer(t *testing.T) (*Server, *fakeCompositor, string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	compositor := &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}
	server := New("test", jrn, compositor, manifest.Dir(t.TempDir()))
	t.Cleanup(func() { server.Close() })
	return server, compositor, filepath.Join(cfg, "niri", "dynamic.kdl")
}

func zenOf(t *testing.T, response Response) bool {
	t.Helper()
	if response.Error != "" {
		t.Fatalf("desk.zen: %s", response.Error)
	}
	var state Zen
	if err := json.Unmarshal(response.Ok, &state); err != nil {
		t.Fatal(err)
	}
	return state.Zen
}

func dynamic(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("niri's dynamic config: %v", err)
	}
	return string(content)
}

func TestZenHidesTheBordersAndTheGaps(t *testing.T) {
	server, _, path := zenServer(t)
	if response := server.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}}); response.Error != "" {
		t.Fatalf("turning zen on: %s", response.Error)
	}
	got := dynamic(t, path)
	for _, want := range []string{"gaps 0", "focus-ring {\n        off\n    }", "border {\n        off\n    }"} {
		if !strings.Contains(got, want) {
			t.Errorf("zen is on and niri's config has no %q:\n%s", want, got)
		}
	}
}

// Off removes overrides rather than guessing the person's preferred layout values.
func TestZenOffLeavesNiriItsOwnLayout(t *testing.T) {
	server, _, path := zenServer(t)
	server.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if response := server.Dispatch(Request{Method: "desk.zen", Args: []string{"off"}}); response.Error != "" {
		t.Fatalf("turning zen off: %s", response.Error)
	}
	if got := dynamic(t, path); strings.Contains(got, "layout") {
		t.Errorf("zen is off but niri's config still overrides layout:\n%s", got)
	}
}

func TestZenKeepsThePlacementRules(t *testing.T) {
	server, _, path := zenServer(t)
	yaml := "name: vshop\nmonitors: { DP-1: { workspaces: [web] } }\n" +
		"apps: [{ app: browser, app_id: org.mozilla.firefox, monitor: DP-1, workspace: web }]\n"
	if err := os.WriteFile(filepath.Join(string(server.desks.(manifest.Dir)), "vshop.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	server.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	got := dynamic(t, path)
	if !strings.Contains(got, "open-on-workspace \"vshop.DP-1.web\"") {
		t.Errorf("turning zen on dropped placement rules:\n%s", got)
	}
	if !strings.Contains(got, "gaps 0") {
		t.Errorf("placement rules dropped zen:\n%s", got)
	}
}

// Dynamic config must not override keybindings, especially panic and lock keys.
func TestZenWritesNoBinds(t *testing.T) {
	server, _, path := zenServer(t)
	server.Dispatch(Request{Method: "desk.zen", Args: []string{"on"}})
	if got := dynamic(t, path); strings.Contains(got, "binds") {
		t.Errorf("zen wrote bindings into the included config:\n%s", got)
	}
}

func TestZenRefusesAStateItDoesNotKnow(t *testing.T) {
	server, _, _ := zenServer(t)
	if response := server.Dispatch(Request{Method: "desk.zen", Args: []string{"yes"}}); response.Error == "" {
		t.Error("desk.zen took a state other than on, off or toggle")
	}
	if response := server.Dispatch(Request{Method: "desk.zen", Args: []string{"on", "off"}}); response.Error == "" {
		t.Error("desk.zen took two states at once")
	}
}
