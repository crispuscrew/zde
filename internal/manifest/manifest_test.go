package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/desk"
)

// The manifest from docs/model.md, section 5, trimmed to what this package
// reads. If the shipped example stops parsing, the doc and the code have
// drifted.
const vshop = `
name: vshop
private: false
monitors:
  DP-1:      { workspaces: [code, agent] }
  HDMI-A-1:  { workspaces: [aux] }
apps:
  - { app: nvim, instance: vshop, mounts: { work: ~/git/vshop },
      monitor: DP-1, workspace: code }
  - { app: browser-vshop, monitor: HDMI-A-1, workspace: aux, background: pause }
policies: { attn: work, zen: false }
on_enter: []
on_exit: []
`

func TestParse(t *testing.T) {
	d, err := Parse([]byte(vshop))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "vshop" || len(d.Monitors) != 2 || len(d.Apps) != 2 {
		t.Errorf("parsed %+v", d)
	}
	if d.Apps[1].Background != "pause" {
		t.Errorf("background = %q", d.Apps[1].Background)
	}
	var got []string
	for _, n := range d.Workspaces() {
		got = append(got, n.String())
	}
	want := []string{"vshop.DP-1.code", "vshop.DP-1.agent", "vshop.HDMI-A-1.aux"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Workspaces() = %v, want %v", got, want)
	}
}

// Everything a manifest can get wrong should be a message, not a broken desk
// discovered at a switch.
func TestParseRejects(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"reserved name", "name: regulars\nmonitors: { DP-1: { workspaces: [a] } }", "reserved band"},
		{"bad desk name", "name: VSHOP\nmonitors: { DP-1: { workspaces: [a] } }", "desk name"},
		{"no monitors", "name: vshop\nmonitors: {}", "no monitors"},
		{"empty monitor", "name: vshop\nmonitors: { DP-1: { workspaces: [] } }", "declares no workspaces"},
		{"bad output", "name: vshop\nmonitors: { 1DP: { workspaces: [a] } }", "not an output connector"},
		{"bad label", "name: vshop\nmonitors: { DP-1: { workspaces: [Code] } }", "not a lowercase label"},
		{"numeric label", "name: vshop\nmonitors: { DP-1: { workspaces: [\"2\"] } }", "adoption mints into"},
		{"duplicate workspace", "name: vshop\nmonitors: { DP-1: { workspaces: [a, a] } }", "twice"},
		{"app with no app", "name: vshop\nmonitors: { DP-1: { workspaces: [a] } }\napps: [{ instance: x }]", "has no app"},
		{"app on unknown monitor", "name: vshop\nmonitors: { DP-1: { workspaces: [a] } }\napps: [{ app: nvim, monitor: DP-9, workspace: a }]", "does not use"},
		{"app on unknown workspace", "name: vshop\nmonitors: { DP-1: { workspaces: [a] } }\napps: [{ app: nvim, monitor: DP-1, workspace: b }]", "does not have"},
		{"bad background", "name: vshop\nmonitors: { DP-1: { workspaces: [a] } }\napps: [{ app: nvim, background: freeze }]", "keep or pause"},
		{"misspelled key", "name: vshop\nmonitorz: { DP-1: { workspaces: [a] } }", "field monitorz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// An app with no pin is placed by adoption, not rejected (model.md, launch
// placement).
func TestParseAllowsUnpinnedApps(t *testing.T) {
	_, err := Parse([]byte("name: vshop\nmonitors: { DP-1: { workspaces: [a] } }\napps: [{ app: nvim }]"))
	if err != nil {
		t.Errorf("an unpinned app was rejected: %v", err)
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vshop.yaml"), []byte(vshop), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "haven.yml"),
		[]byte("name: haven\nmonitors: { DP-1: { workspaces: [db] } }"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Not a manifest, and not an error either.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	all, problems, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Errorf("nothing here is broken, and LoadDir reported %v", problems)
	}
	if len(all) != 2 || all["vshop"] == nil || all["haven"] == nil {
		t.Errorf("loaded %v", all)
	}
}

// No directory is a machine with no desks declared, which is where everyone
// starts.
func TestLoadDirMissing(t *testing.T) {
	all, problems, err := LoadDir(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(all) != 0 || len(problems) != 0 {
		t.Errorf("LoadDir on a missing directory = %v, %v, %v", all, problems, err)
	}
}

// One broken manifest costs that desk and no other. It used to cost all of
// them: LoadDir returned on the first bad file, and the caller that mattered
// dropped the error and carried on with an empty map, so a typo in one file
// silently undeclared every desk on the machine.
func TestLoadDirKeepsTheDesksThatWork(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "vshop.yaml"), []byte(vshop), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("name: VSHOP\nmonitors: {}"), 0o644)

	all, problems, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("one bad file failed the whole read: %v", err)
	}
	if all["vshop"] == nil {
		t.Errorf("the manifest that parses was lost with the one that does not: %v", all)
	}
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want the one broken file", problems)
	}
	// Named, because the point of reporting is telling somebody which file to
	// open.
	if !strings.Contains(problems[0].String(), "broken.yaml") {
		t.Errorf("problem %q does not name the file", problems[0])
	}
}

// Two manifests claiming one desk name is still a problem, and now it is that
// file's problem: the first one read wins and the second is reported, rather
// than both of them and every other desk being lost to the argument.
func TestLoadDirReportsDuplicateDesks(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(vshop), 0o644)
	os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(vshop), 0o644)

	all, problems, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("a duplicate failed the whole read: %v", err)
	}
	if all["vshop"] == nil {
		t.Error("both copies of the desk were dropped, leaving nothing declared")
	}
	if len(problems) != 1 || !strings.Contains(problems[0].String(), "already declared") {
		t.Errorf("problems = %v, want one about a duplicate", problems)
	}
	if !strings.Contains(problems[0].Path, "b.yaml") {
		t.Errorf("the second file should lose, and %q did", problems[0].Path)
	}
}

// Snapshot writes down a desk that exists, and what it writes has to be
// something this package can read back - a snapshot that does not load is not
// a snapshot.
func TestFromMapRoundTrips(t *testing.T) {
	m := desk.Rebuild([]desk.Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
		{ID: 2, Name: "vshop.DP-1.firefox", Output: "DP-1"},
		{ID: 3, Name: "vshop.HDMI-A-1.aux", Output: "HDMI-A-1"},
		{ID: 4, Name: "haven.DP-1.db", Output: "DP-1"},
	}, []string{"DP-1", "HDMI-A-1"})

	d, err := FromMap(m, "vshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Monitors) != 2 || len(d.Monitors["DP-1"].Workspaces) != 2 {
		t.Fatalf("snapshot = %+v", d)
	}

	dir := Dir(t.TempDir())
	path, err := dir.Save(d)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("what snapshot wrote does not load: %v", err)
	}
	var got []string
	for _, n := range back.Workspaces() {
		got = append(got, n.String())
	}
	want := []string{"vshop.DP-1.code", "vshop.DP-1.firefox", "vshop.HDMI-A-1.aux"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("round trip = %v, want %v", got, want)
	}
}

// A desk with nothing in it is not a desk to write down.
func TestFromMapEmptyDesk(t *testing.T) {
	m := desk.Rebuild(nil, []string{"DP-1"})
	if _, err := FromMap(m, "vshop"); err == nil {
		t.Error("wrote down a desk with no workspaces")
	}
}

// An adopted workspace that fell back to an ordinal cannot be declared, since
// ordinals are the space adoption mints into. Better to refuse than to write a
// manifest that fights its own adoption.
func TestFromMapRefusesOrdinals(t *testing.T) {
	m := desk.Rebuild([]desk.Workspace{
		{ID: 1, Name: "vshop.DP-1.1", Output: "DP-1"},
	}, []string{"DP-1"})
	if _, err := FromMap(m, "vshop"); err == nil {
		t.Error("wrote an ordinal into a manifest")
	}
}

// Overwriting someone's desk with the current shape of the screen is not what
// anybody means by taking a snapshot.
func TestSaveRefusesToOverwrite(t *testing.T) {
	dir := Dir(t.TempDir())
	d, err := Parse([]byte(vshop))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dir.Save(d); err != nil {
		t.Fatal(err)
	}
	_, err = dir.Save(d)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("got %v, want a refusal to overwrite", err)
	}
}

// A manifest is config and not a secret, but it is the file that says which
// desk is the private one and where the work on each desk is mounted from - and
// a private desk that is kept out of the picker should not be announced in a
// file every account on the machine can read (docs/vision.md, section 3).
func TestASavedManifestIsReadableOnlyByYou(t *testing.T) {
	dir := t.TempDir()
	d, err := Parse([]byte(vshop))
	if err != nil {
		t.Fatal(err)
	}
	path, err := Dir(filepath.Join(dir, "desks")).Save(d)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("a saved manifest is %04o, want 0600", got)
	}
	made, err := os.Stat(filepath.Join(dir, "desks"))
	if err != nil {
		t.Fatal(err)
	}
	if got := made.Mode().Perm(); got != 0o700 {
		t.Errorf("the directory zde made for them is %04o, want 0700", got)
	}
}

// The app and the instance become a directory: zinc keeps per-instance state
// under one, and these are the two parts of the path a manifest supplies. A
// manifest is a file somebody edits, so a name that climbs out of that
// directory is a thing that can be typed, and it has to be refused where it is
// typed rather than by whoever joins the path later.
func TestAppAndInstanceAreNamesAndNotPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "an instance that climbs out",
			yaml: "name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\n" +
				"apps:\n  - { app: nvim, instance: ../../../etc }\n",
			want: "instance",
		},
		{
			name: "an instance with a slash",
			yaml: "name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\n" +
				"apps:\n  - { app: nvim, instance: work/personal }\n",
			want: "instance",
		},
		{
			name: "an app name that is a path",
			yaml: "name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\n" +
				"apps:\n  - { app: ../nvim, instance: work }\n",
			want: "app name",
		},
		{
			name: "a mount slot that is a path",
			yaml: "name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\n" +
				"apps:\n  - { app: nvim, mounts: { ../out: /tmp } }\n",
			want: "mount slot",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("accepted a name that becomes a path")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say which field is wrong", err)
			}
		})
	}
}

// And the ordinary ones still pass, or this rule has made manifests unwritable
// rather than safe. A mount value is a path on somebody's own machine, which is
// deliberately not checked.
func TestOrdinaryAppEntriesAreAccepted(t *testing.T) {
	d, err := Parse([]byte("name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\n" +
		"apps:\n  - { app: browser-vshop, instance: vshop-2, mounts: { work: ~/git/vshop } }\n"))
	if err != nil {
		t.Fatalf("a manifest anybody would write was refused: %v", err)
	}
	if len(d.Apps) != 1 || d.Apps[0].Instance != "vshop-2" {
		t.Errorf("parsed %+v", d.Apps)
	}
}

// A manifest keeps the app and the instance in two fields; zinc's own docs
// spell an instance as browser@work, so that is what somebody will type into
// the one field. The generic name check would answer it with a rule about
// dashes, which reads as "no instances here" rather than "not in that field".
func TestParseRejectsAddressInAppField(t *testing.T) {
	_, err := Parse([]byte("name: vshop\nmonitors: { DP-1: { workspaces: [web] } }\napps: [{ app: browser@work }]"))
	if err == nil {
		t.Fatal("an address in the app field was accepted")
	}
	if !strings.Contains(err.Error(), "instance: work") {
		t.Fatalf("the error should show which halves go where, got: %v", err)
	}
}
