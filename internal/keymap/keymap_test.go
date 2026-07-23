package keymap

import (
	"strings"
	"testing"
)

const sample = `
binds:
  - { action: desk.pick, key: Mod+D }
  - { action: workspace.go 3, key: Mod+3 }
  - { action: window.focus left, key: Mod+Left }
  - { action: app.launch terminal, key: Mod+T }
  - { action: media.play-pause, key: XF86AudioPlay }
`

func TestEmitKDL(t *testing.T) {
	km, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	want := `// ` + header + `
binds {
    Mod+D { spawn "zde" "desk" "pick"; }
    Mod+3 { focus-workspace 3; }
    Mod+Left { focus-column-left; }
    Mod+T { spawn "zde" "app" "launch" "terminal"; }
    XF86AudioPlay { spawn "zde" "media" "play-pause"; }
}
`
	if got := EmitKDL(km); got != want {
		t.Errorf("EmitKDL:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestEmitCheatsheet(t *testing.T) {
	km, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	got := EmitCheatsheet(km)
	for _, want := range []string{
		"## desk",
		"| `Mod+D` | `desk.pick` | open the desk picker |",
		"| `Mod+3` | `workspace.go 3` | focus workspace 3 |",
		"| `Mod+T` | `app.launch terminal` | launch terminal |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cheatsheet is missing %q:\n%s", want, got)
		}
	}
	// Group order must follow the action map, not the source file.
	if strings.Index(got, "## desk") > strings.Index(got, "## window") {
		t.Error("cheatsheet groups out of action-map order")
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"unknown action", `binds: [{ action: desk.frobnicate, key: Mod+D }]`, "unknown action"},
		{"duplicate chord", "binds:\n  - { action: desk.pick, key: Mod+D }\n  - { action: desk.zen, key: Mod+D }", "bound to both"},
		{"no Mod", `binds: [{ action: desk.pick, key: Ctrl+D }]`, "go through Mod"},
		{"unknown modifier", `binds: [{ action: desk.pick, key: Hyper+D }]`, "unknown modifier"},
		{"bad workspace number", `binds: [{ action: workspace.go zero, key: Mod+D }]`, "workspace number"},
		{"bad app name", `binds: [{ action: app.launch UPPER, key: Mod+D }]`, "lowercase name"},
		{"empty", `binds: []`, "no binds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want error containing %q", err, c.want)
			}
		})
	}
}

// The shipped keymap must always validate; this is the exit check.
func TestShippedKeymap(t *testing.T) {
	km, err := Load("../../common/keymap/keymap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(km.Binds) < 40 {
		t.Errorf("shipped keymap has only %d binds, expected the full Normal set", len(km.Binds))
	}
}
