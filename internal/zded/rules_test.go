package zded

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/manifest"
)

func desks(t *testing.T, yamls ...string) map[string]*manifest.Desk {
	t.Helper()
	out := map[string]*manifest.Desk{}
	for _, y := range yamls {
		d, err := manifest.Parse([]byte(y))
		if err != nil {
			t.Fatal(err)
		}
		out[d.Name] = d
	}
	return out
}

func TestPlacementRules(t *testing.T) {
	got := placementRules(desks(t,
		"name: vshop\nmonitors: { DP-1: { workspaces: [web, code] } }\n"+
			"apps:\n"+
			"  - { app: browser, app_id: org.mozilla.firefox, monitor: DP-1, workspace: web }\n"+
			"  - { app: nvim, monitor: DP-1, workspace: code }\n"+
			"  - { app: chat, app_id: chat }\n"))

	// The rule niri needs: what to match, and where it goes. The workspace is
	// the zde name, because that is what the workspace is actually called.
	for _, want := range []string{
		`match app-id="^org\.mozilla\.firefox$"`,
		`open-on-output "DP-1"`,
		`open-on-workspace "vshop.DP-1.web"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rules are missing %s:\n%s", want, got)
		}
	}
	// The dots in an app id are regex metacharacters. Unescaped, this rule
	// would match orgxmozillaxfirefox too - and more usefully for whoever
	// notices it, a one-character app id of "." matches every window there is.
	if strings.Contains(got, `app-id="^org.mozilla.firefox$"`) {
		t.Errorf("the app id went in as a regex rather than as a name:\n%s", got)
	}
	// An app with no app_id cannot be recognised before it exists, and one with
	// no pin has nowhere to be put. Either way there is no rule to write, and
	// writing half of one would place windows nobody asked to place.
	if strings.Contains(got, "nvim") || strings.Contains(got, `app-id="^chat$"`) {
		t.Errorf("wrote a rule for an app that has no app id or no pin:\n%s", got)
	}
}

// Two desks, one file: it is the whole of niri's dynamic config, so a rewrite
// that kept only the desk being entered would drop the other desk's placement
// the moment you walked between them.
func TestPlacementRulesCoverEveryDesk(t *testing.T) {
	got := placementRules(desks(t,
		"name: vshop\nmonitors: { DP-1: { workspaces: [web] } }\n"+
			"apps: [{ app: browser, app_id: firefox, monitor: DP-1, workspace: web }]\n",
		"name: haven\nmonitors: { DP-1: { workspaces: [read] } }\n"+
			"apps: [{ app: reader, app_id: zathura, monitor: DP-1, workspace: read }]\n"))
	if !strings.Contains(got, "vshop.DP-1.web") || !strings.Contains(got, "haven.DP-1.read") {
		t.Errorf("one desk's rules replaced the other's:\n%s", got)
	}
	// Stable order, so that writing the same manifests twice is the same bytes
	// and niri is not asked to reload for nothing.
	if strings.Index(got, "haven") > strings.Index(got, "vshop") {
		t.Errorf("desks are not in a stable order:\n%s", got)
	}
}

func TestWriteRulesOnlyWhenChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dynamic.kdl")
	if err := writeRules(path, "one\n"); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same bytes: the file must not be replaced. niri reloads its whole config
	// on a write here, and a reload re-evaluates the rules for every window
	// that is already open - so an unchanged rewrite on every desk switch is a
	// reload all day.
	if err := writeRules(path, "one\n"); err != nil {
		t.Fatal(err)
	}
	again, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// By identity, not by timestamp: the write renames a fresh file over this
	// one, so an unchanged rewrite is a different file at the same path - and
	// two writes a microsecond apart can carry the same mtime.
	if !os.SameFile(first, again) {
		t.Error("identical rules were written again, which makes niri reload for nothing")
	}

	if err := writeRules(path, "two\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two\n" {
		t.Errorf("file holds %q after a change", got)
	}
	// Nothing left behind: a temp file beside dynamic.kdl is a file niri's
	// include does not read, but a directory that fills up is still a bug.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("left %d files in the directory, want only dynamic.kdl", len(entries))
	}
}
