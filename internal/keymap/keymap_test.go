package keymap

import (
	"strings"
	"testing"
)

const sample = `
binds:
  - { action: desk.switcher, key: Mod+Tab }
  - { action: desk.next, key: Mod+j }
  - { action: window.focus left, key: Mod+h }
  - { action: window.narrower, key: Mod+Ctrl+h }
  - { action: app.launch terminal, key: Mod+t }
  - { action: app.launch-at editor, key: Mod+Shift+e }
  - { action: media.play-pause, key: XF86AudioPlay }
`

func TestEmitKDL(t *testing.T) {
	km, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	want := `// ` + header + `
binds {
    Mod+Tab { spawn "zde" "desk" "switcher"; }
    Mod+j { spawn "zde" "desk" "next"; }
    Mod+h { focus-column-left; }
    Mod+Ctrl+h { set-column-width "-10%"; }
    Mod+t { spawn "zde" "app" "launch" "terminal"; }
    Mod+Shift+e { spawn "zde" "app" "launch-at" "editor"; }
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
		"| `Mod+Tab` | `desk.switcher` | open the desk switcher |",
		"| `Mod+h` | `window.focus left` | focus the column to the left |",
		"| `Mod+t` | `app.launch terminal` | launch terminal |",
		"| `Mod+Shift+e` | `app.launch-at editor` | prompt for a location, then launch editor |",
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
		{"unknown action", `binds: [{ action: desk.frobnicate, key: Mod+d }]`, "unknown action"},
		{"duplicate chord", "binds:\n  - { action: desk.switcher, key: Mod+d }\n  - { action: desk.zen, key: Mod+d }", "bound to both"},
		{"no Mod", `binds: [{ action: desk.switcher, key: Ctrl+d }]`, "go through Mod"},
		{"unknown modifier", `binds: [{ action: desk.switcher, key: Hyper+d }]`, "unknown modifier"},
		{"uppercase letter", `binds: [{ action: desk.switcher, key: Mod+J }]`, "lowercase"},
		{"bad app name", `binds: [{ action: app.launch UPPER, key: Mod+d }]`, "lowercase name"},
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

// A hardware media/volume key with no modifier is allowed; a Mod-less letter
// is not.
func TestHardwareKeysBypassMod(t *testing.T) {
	if _, err := Parse([]byte(`binds: [{ action: media.next, key: XF86AudioNext }]`)); err != nil {
		t.Errorf("hardware key should be allowed without Mod: %v", err)
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
