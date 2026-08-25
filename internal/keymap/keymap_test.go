package keymap

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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
  - { action: desk.panic, key: Mod+Shift+Escape }
`

func TestEmitKDL(t *testing.T) {
	km, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	// Holding a key repeats where that is the interaction (focus, resize) and
	// nowhere else; the lock screen sees the media key and nothing else; and
	// panic is the one an application cannot take off you.
	want := `// ` + header + `
binds {
    Mod+Tab repeat=false { spawn "zde" "desk" "switcher"; }
    Mod+j repeat=false { spawn "zde" "desk" "next"; }
    Mod+h { focus-column-left; }
    Mod+Ctrl+h { set-column-width "-10%"; }
    Mod+t repeat=false { spawn "zde" "app" "launch" "terminal"; }
    Mod+Shift+e repeat=false { spawn "zde" "app" "launch-at" "editor"; }
    XF86AudioPlay repeat=false allow-when-locked=true { spawn "zde" "media" "play-pause"; }
    Mod+Shift+Escape repeat=false allow-inhibiting=false { spawn "zde" "desk" "panic"; }
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

// The text rendering is what a key shows somebody, so what it must not do is
// arrive with markdown in it. Tab separated for the same reason every other zde
// list is: one grep answers "what is that key".
func TestEmitText(t *testing.T) {
	km, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	got := EmitText(km)
	for _, want := range []string{
		"desk\n",
		"  Mod+Tab\tdesk.switcher\topen the desk switcher\n",
		"  Mod+t\tapp.launch terminal\tlaunch terminal\n",
		"  Mod+Shift+e\tapp.launch-at editor\tprompt for a location, then launch editor\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text keymap is missing %q:\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "|`") {
		t.Errorf("text keymap carries markdown:\n%s", got)
	}
	// Same order as the action map and the markdown, so the two readings of one
	// list cannot disagree about where a thing is.
	if strings.Index(got, "desk\n") > strings.Index(got, "window\n") {
		t.Error("text keymap groups out of action-map order")
	}
	// The first group starts the file: a leading blank line is what a naive
	// separator produces, and it costs a screen line in a pager.
	if strings.HasPrefix(got, "\n") {
		t.Error("text keymap starts with a blank line")
	}
}

// The palette reads its keys off the file EmitText writes, so the writer and
// the reader are one format with two halves. Change a tab, an indent or the
// column order in either and every key in the palette goes missing - silently,
// because a row with no key still draws.
func TestTheCheatsheetSurvivesBeingReadBack(t *testing.T) {
	km, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	got := parseText([]byte(EmitText(km)))
	if len(got) != len(km.Binds) {
		t.Fatalf("wrote %d binds and read back %d: %+v", len(km.Binds), len(got), got)
	}
	for _, b := range km.Binds {
		found := false
		for _, l := range got {
			if l.key == b.Key && l.action == b.Action && strings.HasPrefix(l.desc, b.Entry.Desc) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s (%s) did not survive the round trip: %+v", b.Key, b.Action, got)
		}
	}
}

// What the palette lists, and which key it says runs each row. Both halves are
// load-bearing: an action with no chord must still be listed (that is what
// makes `zde doctor` reachable by name at all), and an action with several must
// show the first, because the keymap writes the letter chord first and a row
// that names two keys names neither.
func TestActionsListEverythingAndSayWhichKeyRunsIt(t *testing.T) {
	// A second chord on one action, which is the ordinary case - the keymap
	// gives the letter chord and the arrows to the same action - and the reason
	// the first one has to win.
	km, err := Parse([]byte(sample + "  - { action: desk.switcher, key: Mod+F5 }\n"))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Action{}
	for _, a := range Actions([]byte(EmitText(km))) {
		by[a.Name] = a
	}
	if got := by["desk.switcher"].Key; got != "Mod+Tab" {
		t.Errorf("desk.switcher key = %q, want the chord the cheatsheet has", got)
	}
	// Bound to nothing in this keymap, and still an action a person can ask
	// for: the whole reason the list is the registry and not the file.
	unbound, ok := by["system.doctor"]
	if !ok {
		t.Fatal("system.doctor is not in the list, so nothing can run it by name")
	}
	if unbound.Key != "" {
		t.Errorf("system.doctor key = %q, and this keymap binds no chord to it", unbound.Key)
	}
	// The argument is part of the row: there is no `app.launch` to run.
	if _, bare := by["app.launch"]; bare {
		t.Error("app.launch is listed without an argument, and running it would refuse")
	}
	launch, ok := by["app.launch terminal"]
	if !ok {
		t.Fatal("app.launch terminal is not in the list, so Mod+t has no row")
	}
	if got := strings.Join(launch.Spawn, " "); got != "zde app launch terminal" {
		t.Errorf("app.launch terminal spawns %q, want what the bind spawns", got)
	}
	if !launch.Live {
		t.Error("app.launch terminal reads as not written, and it is what Mod+t does")
	}
	// A niri native is live because niri does it, whatever zde has written.
	if !by["window.focus left"].Live {
		t.Error("a niri native reads as not written")
	}
}

// The order is the action map's, then the name. Without it the list comes out
// of a Go map, which means a different order every time it is asked for - and a
// palette whose third row moves between two presses of the key is one nobody
// can learn.
func TestActionsComeOutInOneOrder(t *testing.T) {
	first := Actions(nil)
	for range 5 {
		got := Actions(nil)
		if len(got) != len(first) {
			t.Fatalf("two calls, %d rows and %d", len(first), len(got))
		}
		for i := range got {
			if got[i].Name != first[i].Name {
				t.Fatalf("row %d is %q one time and %q the next", i, first[i].Name, got[i].Name)
			}
		}
	}
	// desk before window before system, the way docs/model.md, section 6 has
	// them, rather than alphabetically.
	at := func(group string) int {
		for i, a := range first {
			if a.Group == group {
				return i
			}
		}
		t.Fatalf("no %s group in the list", group)
		return 0
	}
	if at("desk") > at("window") || at("window") > at("system") {
		t.Error("the groups are not in the action map's order")
	}
}

// Which niri natives the palette may ask for over the socket, and which only a
// key can do. Enumerated here and not derived, because it is a fact about niri
// and nothing in Go can see it: an action line with an argument looks the same
// whether niri wants a string or a type of its own.
//
// Derivation is what this replaced, and what it got wrong was the three
// screenshots - the palette offered them, niri answered "error parsing request",
// and the key worked the whole time. Those are zde's own now (registry.go,
// capture), so the list here is the four size changes and the layout switch;
// the rule they were the evidence for is the reason it is still a list.
// Changing which rows the palette offers takes two edits and a reason.
//
// If this fails for a native somebody has just added, the question to answer is
// whether `niri msg action <name>` works with nothing after it. If it does, mark
// it; if it wants an argument or a flag, leave it and let the row say the key is
// the way.
func TestOnlyTheNativesNiriTakesOverIPCAreMarked(t *testing.T) {
	keyOnly := map[string]string{
		"window.narrower":      `set-column-width takes a SizeChange, not the string "-10%"`,
		"window.wider":         "same",
		"window.shorter":       "set-window-height, same",
		"window.taller":        "same",
		"system.layout-switch": "switch-layout takes a LayoutSwitchTarget",
	}
	natives := 0
	for id, e := range registry {
		if e.Native == "" {
			continue
		}
		natives++
		why, listed := keyOnly[id]
		switch {
		case listed && e.performs:
			t.Errorf("%s is marked performs and niri will not take it: %s", id, why)
		case !listed && !e.performs:
			t.Errorf("%s is a native the palette will not run, and no reason is written down for it", id)
		}
	}
	for id := range keyOnly {
		if _, ok := registry[id]; !ok {
			t.Errorf("%s is listed here and is not in the registry", id)
		}
	}
	// The count, so that a native quietly disappearing does not leave this
	// passing over a shorter list than the one it was written for.
	if natives != 20 {
		t.Errorf("the registry has %d natives and this list was written for 20", natives)
	}
}

// The keys a focused application cannot take, and the reason each one is on the
// list. Enumerated here rather than derived, because it is a security decision
// and not a property of anything in the registry: every entry looks the same to
// Go, and the difference between `desk.panic` and `desk.zen` is a judgement
// about what has to work while something on the screen is hostile.
//
// The other half of it matters as much. A bind on this list is a chord no
// application can ever receive, and zwp_keyboard_shortcuts_inhibit exists for
// the applications with a real claim on one - a VM, a nested compositor, a
// remote desktop. So the list failing in the "not listed and marked" direction
// is a bug too: somebody widened the set without writing down what it costs.
//
// If this fails for a bind somebody has just marked, the question to answer is
// whether a person who cannot press it is unsafe or merely inconvenienced. Only
// the first belongs here.
func TestTheKeysAnAppCannotSuppress(t *testing.T) {
	why := map[string]string{
		"desk.panic":           "vision principle 2 names panic; it is the key for when the screen is wrong",
		"desk.block":           "panic one notch harder - a hard lock - and the same reflex",
		"system.lock":          "vision principle 2 names lock; a screen that will not lock is a lost machine",
		"system.lock-preset":   "the same lock over a chosen desk, so the same exception covers it",
		"system.shortcut-grab": "the key that ends a grab, which a grab must not be able to eat",
		"modes.menu":           "vision principle 2 names mode exit, and leaving a mode is opening the picker",
		"modes.window":         "or entering another mode, so the whole group is the exit",
		"modes.kb-mouse":       "same",
		"modes.one-hand":       "same",
		"modes.passthrough":    "the mode that hands the keyboard over on purpose, so the way back must hold",
	}
	for id, e := range registry {
		reason, listed := why[id]
		switch {
		case listed && !e.Unsuppressible:
			t.Errorf("%s is suppressible, and an app that grabs the keyboard takes it: %s", id, reason)
		case !listed && e.Unsuppressible:
			t.Errorf("%s is marked unsuppressible and no reason is written down: it is now a chord no app can ever have", id)
		}
	}
	for id := range why {
		if _, ok := registry[id]; !ok {
			t.Errorf("%s is listed here and is not in the registry", id)
		}
	}

	// And the property reaches the config, which is the only place it does
	// anything. niri's default is true, so a bind that says nothing is one the
	// focused surface swallows.
	km, err := Load("../../common/keymap/keymap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	kdl := EmitKDL(km)
	protected := 0
	for _, b := range km.Binds {
		line := ""
		for _, l := range strings.Split(kdl, "\n") {
			if strings.HasPrefix(l, "    "+b.Key+" ") {
				line = l
			}
		}
		if line == "" {
			t.Fatalf("%s (%s) is missing from the generated binds", b.Key, b.Action)
		}
		got := strings.Contains(line, "allow-inhibiting=false")
		if got != b.Entry.Unsuppressible {
			t.Errorf("%s (%s): allow-inhibiting=false is %v and the registry says %v: %s",
				b.Key, b.Action, got, b.Entry.Unsuppressible, line)
		}
		if got {
			protected++
		}
	}
	// The shipped keymap binds four of them - panic, lock, the mode picker and
	// the way back - and a count here is what notices the day one loses its
	// chord, which is the same thing as losing the protection.
	if protected != 4 {
		t.Errorf("%d shipped binds are unsuppressible, and panic, lock, the mode picker and the escape are four", protected)
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
		// A niri native is live by construction: niri performs it, and there is
		// no zde command behind it to be written or missing. Saying so as well
		// would be a second answer to one question, and the palette would show
		// working keys as dead.
		if e.Native != "" && e.written {
			t.Errorf("%s: a niri native marked written, which is a claim about a zde command it does not have", id)
		}
		// And the mirror of it: performs is a claim about niri's socket, which
		// a spawn never reaches.
		if e.Native == "" && e.performs {
			t.Errorf("%s: a spawn marked performs, which says nothing about a command zde runs", id)
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

// A FIFO where the cheatsheet should be is refused rather than waited on.
//
// This path is $XDG_CONFIG_HOME/zde/keymap.txt, a name in a directory the
// account can write, and until this it was read with a plain os.ReadFile.
// Opening a FIFO for reading does not fail: it waits in the kernel for a writer
// that never comes. Two things read it and both of them are keys - the palette
// on Mod+p, which builds its rows from here on every call, and `zde keys` on
// Mod+slash. So one `mkfifo` there meant the palette never opened, `zde keys`
// hung, and inside the daemon every palette.list parked a goroutine and a
// descriptor that closing the socket could not take back, because nothing can
// wake a goroutine blocked in the kernel on a read.
//
// The deadline is the test. Without the fix this does not fail, it hangs, and a
// CI job that hangs is a regression nobody gets told about.
func TestAFifoAtTheCheatsheetIsRefusedRatherThanWaitedOn(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "zde", "keymap.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)

	done := make(chan error, 1)
	go func() { _, err := ReadText(); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read a FIFO as the keymap")
		}
		// Named by what it is, because whoever has to fix this is looking at a
		// path that exists and a key that does nothing.
		if !strings.Contains(err.Error(), "named pipe") {
			t.Errorf("error is %q, want it to say what is at that path", err)
		}
	case <-time.After(10 * time.Second):
		// Leaked on purpose: it is blocked in the kernel with nothing to
		// unblock it, and the test binary is on its way out.
		t.Fatal("ReadText did not return in 10s, which is the hang the palette key shipped with")
	}
}

// A missing cheatsheet is still not an error, because that is a machine whose
// layer 1 has never been activated, and both callers say so in their own words.
func TestNoCheatsheetReadsAsNotExisting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := ReadText(); !os.IsNotExist(err) {
		t.Errorf("a missing keymap answered %v, want it to read as not existing", err)
	}
}

// And the ceiling, because a file this size at this path is not a cheatsheet.
//
// Text that parses on purpose: the reader takes any bytes at all, so a test
// built out of rubbish would pass with the bound taken back out.
func TestSomethingFarTooBigIsNotACheatsheet(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	line := "Mod+t\tapp.launch terminal\ta terminal\n"
	body := strings.Repeat(line, textMax/len(line)+1)
	if len(body) <= textMax {
		t.Fatalf("the test file is %d bytes and the ceiling is %d", len(body), textMax)
	}
	if err := os.WriteFile(filepath.Join(dir, "zde", "keymap.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	if _, err := ReadText(); err == nil {
		t.Errorf("read %d bytes as a cheatsheet", len(body))
	}
}

// The generator's source is read plainly, and a store path is what it reads.
//
// The opposite of the test above, on purpose, because the two reads in this
// package are not the same read (see Load). This one is the input of a
// build-time tool, named by the derivation that runs it, and it must not be
// subject to an ownership check: inside nix's sandbox the builder is in a user
// namespace with a single uid mapping, so every root-owned store path reads as
// the overflow uid and every one of them would be refused. That is what took
// the flake red - `zde-keymap: ...keymap.yaml belongs to another account` - and
// it is not a thing a test on this machine can see, because a check run outside
// a sandbox reads the store's real uids.
//
// So what is pinned here is the decision rather than the mechanism: this call
// does not consult ownership, which is checkable by handing it a file that
// belongs to somebody else. /etc/os-release is root's on every machine this
// builds on, and it is not YAML - so the read reaching the parser is the whole
// assertion, and the parse failing afterwards is the proof it got that far.
func TestTheKeymapSourceIsReadWithoutAskingWhoOwnsIt(t *testing.T) {
	const foreign = "/etc/os-release"
	fi, err := os.Stat(foreign)
	if err != nil {
		t.Skipf("no %s on this machine to read as somebody else's file", foreign)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Getuid() {
		t.Skipf("%s belongs to this account here, so it cannot stand in for a store path", foreign)
	}
	_, err = Load(foreign)
	if err == nil {
		t.Fatalf("%s parsed as a keymap, so this test proves nothing", foreign)
	}
	// Anchored on the tail every ownership refusal in internal/plainfile shares,
	// rather than on one of their two wordings. Which one comes back depends on
	// where the test runs - in a builder a store path reads as the overflow uid
	// and gets "belongs to another account", while on this machine the same file
	// reads as root's and gets the other one - and a test that named a single
	// wording passed on the very code that took the flake red.
	if strings.Contains(err.Error(), "not zde's to act on") {
		t.Errorf("Load = %v, and a store path inside a nix builder is refused exactly like this", err)
	}
	// And positively: the error is the parser's, which is only reachable once
	// the bytes have been read.
	if !strings.Contains(err.Error(), "keymap:") {
		t.Errorf("Load = %v, want the parser's complaint, which means the read got that far", err)
	}
	if !strings.Contains(err.Error(), foreign) {
		t.Errorf("Load = %v, want the path in it: the generator runs on a path the caller chose", err)
	}
}
