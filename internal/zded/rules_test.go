package zded

import (
	"os"
	"os/exec"
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
	//
	// Two backslashes before each dot, because there are two escapings here and
	// they undo in the other order: KDL's parser reads `\\` and hands niri one
	// backslash, and niri's regex reads `\.` and matches one dot. One backslash
	// in the file is `\.` to KDL, which is not one of its escapes, and niri
	// refuses the whole config - dynamic.kdl included, and the binds with it.
	for _, want := range []string{
		`match app-id="^org\\.mozilla\\.firefox$"`,
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

// hostile is a desk holding one app whose id carries every character the two
// escapings disagree about. It is built here rather than parsed from YAML on
// purpose: manifest.Parse refuses a quote, a backslash and a newline in an
// app_id, and a test that can only reach this through the guard is a test of
// the guard. The two are meant to be independent (internal/manifest, the app_id
// check), so this is the one that holds up the escaping's end.
func hostile() map[string]*manifest.Desk {
	return map[string]*manifest.Desk{"vshop": {
		Name:     "vshop",
		Monitors: map[string]manifest.Monitor{"DP-1": {Workspaces: []string{"web"}}},
		Apps: []manifest.App{{
			App:       "browser",
			AppID:     "a\"b\\\nc.d+e",
			Monitor:   "DP-1",
			Workspace: "web",
		}},
	}}
}

// The two escapings, composed, byte for byte.
//
// Read the want right to left, which is the order the two parsers undo it in.
// niri wants the regex `^a"b\\` + newline + `c\.d\+e$`: a literal backslash is
// `\\` to a regex, and a literal dot is `\.`. KDL then has to carry that regex
// as a string, and to KDL every one of those backslashes is a character that
// has to be escaped in its turn - so each doubles again - while the quote
// becomes `\"` and the newline becomes `\n`.
//
// The failure this pins is not a wrong match. It is niri refusing the file, and
// dynamic.kdl is included by the config that carries the binds: a machine that
// pins one app with a dot in its id gets no keybinds at all.
func TestPlacementRulesEscapeForKDLAsWellAsForTheRegex(t *testing.T) {
	got := placementRules(hostile())
	want := `    match app-id="^a\"b\\\\\nc\\.d\\+e$"` + "\n"
	if !strings.Contains(got, want) {
		t.Errorf("rules are missing\n%s\ngot:\n%s", want, got)
	}
	// And said the same way niri's parser says it, over the whole file rather
	// than over one line somebody thought of. Every backslash has to begin an
	// escape KDL knows; `\\` is one of them and swallows the character after
	// it. `\.` and `\+` - which is exactly what QuoteMeta leaves behind - are
	// not, and either costs the whole config.
	const kdlEscapes = `"/\bfnrtu`
	for i := 0; i < len(got); i++ {
		if got[i] != '\\' {
			continue
		}
		if i+1 >= len(got) || !strings.ContainsRune(kdlEscapes, rune(got[i+1])) {
			t.Fatalf("an escape KDL cannot read (%q) survived into the rules:\n%s", got[i:min(i+3, len(got))], got)
		}
		i++ // an escaped backslash: the second one starts nothing
	}
}

func TestKDLStringEscapesWhatKDLEscapesAndNothingElse(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// The common case: nothing to do, and nothing done.
		{"firefox", `"firefox"`},
		// What QuoteMeta hands it. The backslash it added for the regex is the
		// whole of this bug.
		{`^org\.mozilla\.firefox$`, `"^org\\.mozilla\\.firefox$"`},
		{`a"b`, `"a\"b"`},
		{"a\nb", `"a\nb"`},
		{"a\rb", `"a\rb"`},
		{"a\tb", `"a\tb"`},
		{"a\bb", `"a\bb"`},
		{"a\fb", `"a\fb"`},
		// C0 and DEL have no letter of their own. Braced hex is the only form
		// KDL 1.0 takes: the bare four-hex-digit one is a parse error.
		{"a\x00b", `"a\u{0}b"`},
		{"a\x1bb", `"a\u{1b}b"`},
		{"a\x7fb", `"a\u{7f}b"`},
		// Not ASCII and not a problem: KDL is UTF-8, so this goes through whole
		// rather than as escapes nobody can read.
		{"кофе", `"кофе"`},
		// A byte that is not UTF-8 at all. It cannot be copied through - niri
		// would refuse the file it is in - so it becomes the replacement rune.
		{"a\xffb", "\"a�b\""},
	} {
		if got := kdlString(c.in); got != c.want {
			t.Errorf("kdlString(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// The check that was missing, and the only one that settles it: niri's own
// parser, on the bytes zded writes.
//
// Everything above asserts a shape somebody worked out. This asks the program
// that has to read it - and the bug it is here for is one where the shape
// looked right to two readers and to `regexp.QuoteMeta`'s documentation, and
// was refused by the parser.
//
// It skips without niri, which is a real cost: a skipped test proves nothing
// and says so quietly. Two things pay for it. niri is in the devShell CI runs
// go test inside (flake.nix), so the skip does not fire there; and the VM smoke
// test runs `niri validate` against a real generated config with no skip in it
// at all (nix/tests/smoke.nix). This one is the fast copy, next to the code, on
// the machine where the mistake gets made.
func TestPlacementRulesAreAConfigNiriAccepts(t *testing.T) {
	bin, err := exec.LookPath("niri")
	if err != nil {
		t.Skip("no niri on PATH: nix/tests/smoke.nix is the copy of this that cannot skip")
	}
	// Both: the app id somebody will really pin, and the one nothing sane will.
	rules := placementRules(hostile()) + placementRules(desks(t,
		"name: haven\nmonitors: { DP-1: { workspaces: [web] } }\n"+
			"apps: [{ app: browser, app_id: org.mozilla.firefox, monitor: DP-1, workspace: web }]\n"))
	path := filepath.Join(t.TempDir(), "dynamic.kdl")
	if err := os.WriteFile(path, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "validate", "-c", path).CombinedOutput()
	if err != nil {
		t.Errorf("niri will not load the rules zded writes, so the config that includes them is refused "+
			"and the machine has no binds:\n%s\nrules:\n%s", out, rules)
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

// The directory niri reads this from is made rather than assumed. Break this
// and the first startup on an account where nobody has ever configured niri by
// hand prints "no such file or directory" once, and then every pinned app opens
// wherever niri felt like putting it - the whole of what the manifests say about
// placement, silently not happening.
func TestWriteRulesMakesTheDirectoryItWritesInto(t *testing.T) {
	// What XDG_CONFIG_HOME points at on a fresh account: a directory with no
	// niri in it.
	path := filepath.Join(t.TempDir(), "niri", "dynamic.kdl")
	if err := writeRules(path, "one\n"); err != nil {
		t.Fatalf("writing the placement rules: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\n" {
		t.Errorf("file holds %q", got)
	}
	// Only niri reads this, and niri is the person's own compositor. It names a
	// desk per pinned app, and one of those desks can be one zde keeps out of
	// the picker (docs/vision.md, section 3).
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("the placement rules are %04o, want 0600: they name the desks, private ones included", mode)
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

// The machine that has been running zde since before the mode was narrowed.
//
// Its dynamic.kdl is 0644 and holds exactly what would be written now, so the
// "only when they changed" return above is the only path it ever takes and a
// tightening below it never arrives. This file is zde's own - the header says
// not to edit it - so the mode is zde's to set even when the bytes are not
// touched, and the file must not be replaced to set it: niri reloads its whole
// config on a write here.
func TestPlacementRulesAnEarlierZdeLeftOpenAreTightenedWithoutBeingRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dynamic.kdl")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Past whatever umask the test runs under, so this starts wide enough to
	// prove something.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := writeRules(path, "one\n"); err != nil {
		t.Fatalf("writing the placement rules: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Mode().Perm(); got != 0o600 {
		t.Errorf("rules that were already there are still %04o: the desk names in them, private ones included, stay readable on every machine that has run zde before", got)
	}
	if !os.SameFile(before, after) {
		t.Error("identical rules were written again to fix the mode, which makes niri reload for nothing")
	}
}
