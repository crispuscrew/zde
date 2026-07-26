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
	// Holding a key repeats where that is the interaction (focus, resize) and
	// nowhere else; the lock screen sees the media key and nothing else.
	want := `// ` + header + `
binds {
    Mod+Tab repeat=false { spawn "zde" "desk" "switcher"; }
    Mod+j repeat=false { spawn "zde" "desk" "next"; }
    Mod+h { focus-column-left; }
    Mod+Ctrl+h { set-column-width "-10%"; }
    Mod+t repeat=false { spawn "zde" "app" "launch" "terminal"; }
    Mod+Shift+e repeat=false { spawn "zde" "app" "launch-at" "editor"; }
    XF86AudioPlay repeat=false allow-when-locked=true { spawn "zde" "media" "play-pause"; }
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
		{"argument to exact action", `binds: [{ action: desk.zen loudly, key: Mod+d }]`, "takes no argument"},
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

// The key half of a chord is written into KDL unquoted, so anything that can
// close a node or start a new one has to be refused at the source. Every string
// here was accepted before, and the first one emitted a working bind that ran
// an arbitrary command plus a second, Mod-less bind.
func TestChordInjection(t *testing.T) {
	for _, key := range []string{
		"Mod+q { spawn \"sh\" \"-c\" \"whoami\"; }\n    Escape",
		"Mod+a\n    Delete",
		"Mod+z { spawn \"id\"; } Escape",
		`Mod+"`,
		"Mod+q;",
		"Mod+{",
		"Mod+a b",
		"Mod+a|b",
		`Mod+\`,
	} {
		t.Run(key, func(t *testing.T) {
			_, err := parseChord(key)
			if err == nil {
				t.Fatalf("parseChord(%q) was accepted", key)
			}
			if !strings.Contains(err.Error(), "not a key name") {
				t.Errorf("got %v, want a key-name error", err)
			}
		})
	}
}

// Names niri rejects at startup, which is to say at a login. The generator
// cannot know every keysym, but it can insist on the shape of one.
func TestKeyShape(t *testing.T) {
	ok := []string{"Mod+q", "Mod+F5", "Mod+bracketleft", "Mod+Page_Up", "XF86AudioNext", "Print", "Mod+1"}
	for _, key := range ok {
		if _, err := parseChord(key); err != nil {
			t.Errorf("parseChord(%q) = %v, want accepted", key, err)
		}
	}
	// Cyrillic Ф is two bytes, so the "write it lowercase" check never saw it.
	for _, key := range []string{"Mod+ф", "Mod+Ф", "Mod+"} {
		if _, err := parseChord(key); err == nil {
			t.Errorf("parseChord(%q) was accepted", key)
		}
	}
}

// Modifier order is not meaning. Two spellings of one chord used to pass as two
// binds and collide inside niri, which then rejects the whole config.
func TestChordNormalization(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Mod+Shift+q", "Mod+Shift+q"},
		{"Shift+Mod+q", "Mod+Shift+q"},
		{"Shift+Ctrl+Mod+h", "Mod+Ctrl+Shift+h"},
	} {
		got, err := parseChord(c.in)
		if err != nil {
			t.Fatalf("parseChord(%q) = %v", c.in, err)
		}
		if got.String() != c.want {
			t.Errorf("parseChord(%q) = %q, want %q", c.in, got.String(), c.want)
		}
	}
	_, err := Parse([]byte("binds:\n  - { action: desk.switcher, key: Mod+Shift+q }\n  - { action: desk.zen, key: Shift+Mod+q }"))
	if err == nil || !strings.Contains(err.Error(), "bound to both") {
		t.Errorf("got %v, want the reordered spelling to collide", err)
	}
	if _, err := parseChord("Mod+Mod+j"); err == nil {
		t.Error("a repeated modifier was accepted")
	}
}

// keymap.yaml reserves these; the comment saying so was the only thing
// enforcing it.
func TestReservedChords(t *testing.T) {
	for _, key := range []string{"Mod+z", "Mod+x"} {
		if _, err := parseChord(key); err == nil {
			t.Errorf("parseChord(%q) was accepted, it is reserved", key)
		}
	}
	// The Shift variants are the ones that bind.
	for _, key := range []string{"Mod+Shift+z", "Mod+Shift+x"} {
		if _, err := parseChord(key); err != nil {
			t.Errorf("parseChord(%q) = %v, want accepted", key, err)
		}
	}
}

// The registry's rules live in its doc comment and in no type, so they live
// here too. Every one of these fails silently in the generated config: a bind
// that spawns nothing, a Native that wins over a Spawn, or a bind that vanishes
// from the cheatsheet because its group is misspelled.
func TestRegistryInvariants(t *testing.T) {
	inGroup := map[string]bool{}
	for _, g := range groups {
		inGroup[g] = true
	}
	// The only things a locked screen may do: change the volume, the mic, the
	// brightness, or the track.
	inLockGroup := map[string]bool{"audio": true, "media": true, "system": true}
	for id, e := range registry {
		switch {
		case e.Native == "" && len(e.Spawn) == 0:
			t.Errorf("%s: neither Native nor Spawn, would emit an empty action", id)
		case e.Native != "" && len(e.Spawn) > 0:
			t.Errorf("%s: both Native and Spawn, Spawn would be dropped", id)
		}
		if !inGroup[e.Group] {
			t.Errorf("%s: group %q is not in groups, its binds would vanish from the cheatsheet", id, e.Group)
		}
		if e.Desc == "" {
			t.Errorf("%s: no Desc, the cheatsheet row would be blank", id)
		}
		// A bind that reaches past the lock screen is a way around it, so the
		// set that does is fixed here rather than left to a code review.
		if e.WhenLocked && !inLockGroup[e.Group] {
			t.Errorf("%s: allow-when-locked on a %q bind, which the lock screen has no business running", id, e.Group)
		}
		holes := strings.Count(e.Native, argPlaceholder)
		if e.parametric() && e.Native != "" && holes != 1 {
			t.Errorf("%s: parametric Native has %d %s, want exactly 1", id, holes, argPlaceholder)
		}
		if !e.parametric() && holes != 0 {
			t.Errorf("%s: non-parametric Native contains %s", id, argPlaceholder)
		}
	}
}

// The shipped keymap must always validate, and the artifacts that actually
// reach a machine are the two the emitters produce - so emit them.
func TestShippedKeymap(t *testing.T) {
	km, err := Load("../../common/keymap/keymap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(km.Binds) < 40 {
		t.Errorf("shipped keymap has only %d binds, expected the full Normal set", len(km.Binds))
	}

	kdl := EmitKDL(km)
	cheat := EmitCheatsheet(km)
	for _, b := range km.Binds {
		// The chord, then either properties or the node - never another chord.
		if !strings.Contains(kdl, "\n    "+b.Key+" ") {
			t.Errorf("%s (%s) is missing from the generated binds", b.Key, b.Action)
		}
		if !strings.Contains(cheat, "| `"+b.Key+"` |") {
			t.Errorf("%s (%s) is missing from the cheatsheet", b.Key, b.Action)
		}
	}
	// Every group that has binds must have a section, or binds are being
	// dropped silently.
	for _, b := range km.Binds {
		if !strings.Contains(cheat, "## "+b.Entry.Group+"\n") {
			t.Errorf("cheatsheet has no section for group %q", b.Entry.Group)
		}
	}
}
