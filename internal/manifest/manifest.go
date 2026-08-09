// Package manifest reads desk manifests (docs/model.md, section 5): what a
// desk is made of, before any of it is running.
//
// A manifest is the second record of where a workspace belongs. The name on a
// live workspace is the ownership record, and it moves when a monitor does; a
// manifest is what says where things go back to, and it is why an unplugged
// monitor is recoverable for the workspaces it declares.
//
// Apps are parsed and validated here and launched nowhere: launching is zcr's
// (docs/vision.md, section 2), which does not exist in this repo yet. Reading
// them now is what lets a manifest be written and checked before then.
package manifest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/plainfile"
)

// Desk is one manifest.
type Desk struct {
	Name     string             `yaml:"name"`
	Private  bool               `yaml:"private"`
	Monitors map[string]Monitor `yaml:"monitors"`
	Apps     []App              `yaml:"apps"`
	Policies Policies           `yaml:"policies"`
	OnEnter  []string           `yaml:"on_enter"`
	OnExit   []string           `yaml:"on_exit"`
}

// Monitor is what a desk puts on one output.
type Monitor struct {
	Workspaces []string `yaml:"workspaces"`
}

// App is one application the desk runs.
type App struct {
	App      string `yaml:"app"`
	Instance string `yaml:"instance"`
	// AppID is the Wayland application id its window arrives with, which is
	// not the app name above: `app` is what zinc runs, and this is what the
	// program inside calls itself. They differ often enough to matter - a zinc
	// app called browser opens a window that says org.mozilla.firefox.
	//
	// It is here because it is the only thing a window can be recognised by
	// before it is on the screen: niri matches a window rule on app-id and
	// title, and zinc's per-instance identity - which would be the better
	// answer - reaches the compositor and stops there (docs/vision.md, ask 1).
	// Without it the app still launches; it just lands where niri puts new
	// windows rather than where the desk says.
	AppID      string            `yaml:"app_id"`
	Mounts     map[string]string `yaml:"mounts"`
	Monitor    string            `yaml:"monitor"`
	Workspace  string            `yaml:"workspace"`
	Background string            `yaml:"background"`
}

// Policies is the desk's own settings.
type Policies struct {
	// Attn is the attn mode entering this desk puts the session in, and empty
	// is a desk with no opinion - which is not the same as `work`. A desk that
	// says nothing leaves the mode as it is, so writing `attn: work` here is an
	// instruction to turn the notifications back on and leaving it out is not.
	Attn string `yaml:"attn"`
	Zen  bool   `yaml:"zen"`
}

// DefaultDir is where manifests live.
func DefaultDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "zde", "desks")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "zde", "desks")
	}
	return filepath.Join(home, ".config", "zde", "desks")
}

// Parse reads one manifest and checks it.
//
// Everything a manifest can get wrong is caught here rather than at a desk
// switch: a name that cannot become a workspace name, a workspace declared
// twice on one monitor, or an app pinned to a workspace the desk does not
// have. A manifest is written by hand or by an agent (model.md, section 5),
// and the difference between a typo and a broken desk should be a message.
func Parse(data []byte) (*Desk, error) {
	var d Desk
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // a misspelled key is a mistake, not a comment
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	// And nothing after it. A YAML file may hold several documents separated by
	// `---`, and this decoder reads one: a second desk written under the first
	// was not a desk that failed to load, it was a desk that was never
	// mentioned again. Somebody who wrote two and got one would have every
	// reason to think zde had read both, because the file it refused to read
	// half of is the file `zde status` reports no problem with.
	//
	// Refused rather than read, because one file is one desk everywhere else:
	// LoadDir keys the map by desk name and reports a second file claiming a
	// name as a problem, so a file holding two would be the one place that rule
	// does not apply.
	var rest yaml.Node
	if err := dec.Decode(&rest); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("manifest %q: there is more than one document in this file, and a manifest is "+
			"one desk: put the second after a `---` in a file of its own", d.Name)
	}
	if err := d.check(); err != nil {
		return nil, err
	}
	return &d, nil
}

func (d *Desk) check() error {
	if d.Name == desk.Regulars {
		return fmt.Errorf("manifest: %q is the reserved band reachable from every desk, not a desk", desk.Regulars)
	}
	// The name has to survive becoming a workspace name, so it is checked as
	// one rather than by a rule of its own that could drift from it.
	if _, err := desk.NewName(d.Name, "DP-1", "check"); err != nil {
		return fmt.Errorf("manifest: desk name %q: %w", d.Name, err)
	}
	if len(d.Monitors) == 0 {
		return fmt.Errorf("manifest %q: no monitors, so the desk has nowhere to be", d.Name)
	}
	for output, mon := range d.Monitors {
		if len(mon.Workspaces) == 0 {
			return fmt.Errorf("manifest %q: monitor %q declares no workspaces", d.Name, output)
		}
		seen := map[string]bool{}
		for _, label := range mon.Workspaces {
			name, err := desk.NewName(d.Name, output, label)
			if err != nil {
				return fmt.Errorf("manifest %q: %w", d.Name, err)
			}
			if _, isOrdinal := name.Ordinal(); isOrdinal {
				return fmt.Errorf("manifest %q: workspace %q on %q is a number, which is the space adoption mints into - give it a name", d.Name, label, output)
			}
			if seen[label] {
				return fmt.Errorf("manifest %q: monitor %q declares workspace %q twice", d.Name, output, label)
			}
			seen[label] = true
		}
	}
	// The mode is checked by the package that owns what a mode is, rather than
	// against a list kept here that would drift from it the day a fourth mode
	// exists. Empty is skipped before parsing: ParseMode reads it as work so
	// that a journal which has never been told replays into the default, and a
	// manifest that declares nothing means the opposite of that.
	//
	// It is a refusal rather than a shrug because this is now read. A desk that
	// declared `focus` and got no policy at all was a manifest field nobody
	// consulted; one that declares `focussed` and gets no policy at all is a
	// desk that goes quiet for a reason the person cannot see - and every other
	// thing a manifest can get wrong is caught here.
	if d.Policies.Attn != "" {
		if _, err := attn.ParseMode(d.Policies.Attn); err != nil {
			return fmt.Errorf("manifest %q: policies.attn: %w", d.Name, err)
		}
	}
	for i, app := range d.Apps {
		if app.App == "" {
			return fmt.Errorf("manifest %q: app %d has no app", d.Name, i+1)
		}
		// The app and the instance name a directory: zinc keeps per-instance
		// state under one, and these are the two parts of the path a manifest
		// supplies. A manifest is a file somebody edits by hand, so
		// `instance: ../../../etc` is a thing that can be typed - and it must
		// be refused here rather than by whoever ends up joining the path.
		//
		// Checked as a desk name is checked, which is the same shape zinc's own
		// app names take: lowercase, digits, dashes between them. Nothing that
		// can be a dot, a slash, or a surprise.
		// `browser@work` is how a person writes one instance of an app, and
		// zinc 0.8.1 documents it, so it is what somebody will type here.
		// This file keeps the two halves apart, and the generic name error
		// below would answer a reasonable thing to write with a rule about
		// dashes.
		if base, instance, isAddress := strings.Cut(app.App, "@"); isAddress {
			return fmt.Errorf("manifest %q: app %q is an address: write the two halves in their own fields "+
				"(app: %s, instance: %s)", d.Name, app.App, base, instance)
		}
		if !desk.ValidDesk(app.App) {
			return fmt.Errorf("manifest %q: app name %q is not a name: lowercase letters, digits and dashes, "+
				"because it becomes part of a path", d.Name, app.App)
		}
		if app.Instance != "" && !desk.ValidDesk(app.Instance) {
			return fmt.Errorf("manifest %q: app %q: instance %q is not a name: lowercase letters, digits and "+
				"dashes, because it becomes part of a path", d.Name, app.App, app.Instance)
		}
		// It becomes a string in a KDL file the compositor parses. A quote or a
		// backslash from a hand-edited manifest would end the string early and
		// take niri's whole config down with it - including the binds - which
		// is a worse day than a window in the wrong place.
		if strings.ContainsAny(app.AppID, "\"\\\n\r") {
			return fmt.Errorf("manifest %q: app %q: app_id %q contains a quote, a backslash or a newline, "+
				"and it becomes a string in the compositor's config", d.Name, app.App, app.AppID)
		}
		for slot := range app.Mounts {
			// The slot is a name the app declares and this manifest fills; the
			// value is a path on this machine, which is the person's own and
			// deliberately not our business.
			if !desk.ValidDesk(slot) {
				return fmt.Errorf("manifest %q: app %q: mount slot %q is not a name", d.Name, app.App, slot)
			}
		}
		switch app.Background {
		case "", "keep", "pause":
		default:
			return fmt.Errorf("manifest %q: app %q: background %q is not keep or pause", d.Name, app.App, app.Background)
		}
		if app.Monitor == "" && app.Workspace == "" {
			continue // unpinned: adoption places it (model.md, launch placement)
		}
		mon, ok := d.Monitors[app.Monitor]
		if !ok {
			return fmt.Errorf("manifest %q: app %q is pinned to monitor %q, which the desk does not use", d.Name, app.App, app.Monitor)
		}
		if !contains(mon.Workspaces, app.Workspace) {
			return fmt.Errorf("manifest %q: app %q is pinned to workspace %q, which %q does not have", d.Name, app.App, app.Workspace, app.Monitor)
		}
	}
	return nil
}

// Workspaces is every workspace the desk declares, as workspace names, in a
// stable order. This is the desk as it should be, against which what niri
// actually has can be compared.
func (d *Desk) Workspaces() []desk.Name {
	var out []desk.Name
	for _, output := range sortedKeys(d.Monitors) {
		for _, label := range d.Monitors[output].Workspaces {
			// check() already proved these parse.
			n, err := desk.NewName(d.Name, output, label)
			if err != nil {
				continue
			}
			out = append(out, n)
		}
	}
	return out
}

// FromMap writes down a desk that already exists: the workspaces niri has,
// under the name they carry. That is what snapshot is (docs/model.md, section
// 5) - you arrange a desk by hand, adoption names it, and this makes it
// something you can ask for again.
//
// Apps are deliberately not captured. A manifest's app is a zinc app name and
// what niri reports is an application id; writing one where the other belongs
// would produce a manifest that reads fine and launches nothing. Capturing
// them needs zcr, which is not in this repo.
//
// Private is carried rather than read off the screen, because nothing on the
// screen says it: it is a declaration, and the only place it exists is the
// manifest this desk already has. The caller supplies it (internal/zded, the
// snapshot verb). Written down again rather than dropped, because a snapshot
// that quietly left it out would be zde writing a second manifest for a
// private desk that does not say the desk is private - which is exactly the
// file that un-declares one.
func FromMap(m *desk.Map, name string, private bool) (*Desk, error) {
	workspaces := m.Workspaces(name)
	if len(workspaces) == 0 {
		return nil, fmt.Errorf("desk %q has no workspaces to write down", name)
	}
	d := &Desk{Name: name, Private: private, Monitors: map[string]Monitor{}}
	for _, n := range workspaces {
		mon := d.Monitors[n.Monitor]
		mon.Workspaces = append(mon.Workspaces, n.Slot)
		d.Monitors[n.Monitor] = mon
	}
	// Written through its own checks: a snapshot that cannot be read back is
	// not a snapshot, and an adopted ordinal is one way to get there.
	if err := d.check(); err != nil {
		return nil, err
	}
	return d, nil
}

// Dir is a directory of manifests, and the two things zded does with one.
type Dir string

// All reads every manifest in the directory.
func (dir Dir) All() (map[string]*Desk, []Problem, error) { return LoadDir(string(dir)) }

// Problem is one manifest that could not be used, and why. A manifest is a file
// a person edits, so one of them being wrong is ordinary; the rest of the desks
// going with it is not.
type Problem struct {
	Path string
	Err  error
}

func (p Problem) String() string { return p.Path + ": " + p.Err.Error() }

// Save writes a manifest, refusing to overwrite one that is already there.
// A snapshot is a record of an arrangement someone made; quietly replacing an
// existing desk with the current shape of the screen is not what anybody means
// by taking one.
func (dir Dir) Save(d *Desk) (string, error) {
	if err := d.check(); err != nil {
		return "", err
	}
	// 0700, and 0600 on the file below. A manifest is config a person edits and
	// not a secret, but it is the file that says which desk is the private one
	// and which directories the work on each desk is mounted from - and the
	// point of a private desk is that it is not advertised (docs/vision.md,
	// section 3). Nothing but zded reads these, and zded is the session.
	if err := os.MkdirAll(string(dir), 0o700); err != nil {
		return "", err
	}
	// MkdirAll's mode is only used for a directory it creates, so on its own the
	// 0700 above reaches every machine except the ones that need it: one that
	// has taken a snapshot before this was written already has the directory, at
	// the 0755 the old code asked for, and MkdirAll leaves it as it found it.
	// The chmod on every Save is what reaches those.
	//
	// The manifests already in it are deliberately left alone. A manifest is a
	// file a person writes by hand, and silently rewriting its mode is zde
	// changing their file behind their back; under a 0700 directory a 0644
	// manifest is unreadable by anybody else anyway. That is also why a refusal
	// here is an error where the journal's equivalent is best effort: there the
	// file's own 0600 carries the privacy, here the directory is what stands
	// between another account and a manifest zde will not chmod.
	//
	// Only for the directory zde picked for itself, cleaned so that a trailing
	// slash makes no difference. `zded -desks /tmp` would otherwise take the
	// machine's temp directory private on its way past, which is the restraint
	// internal/journal already keeps for `-journal`.
	if filepath.Clean(string(dir)) == DefaultDir() {
		if err := os.Chmod(string(dir), 0o700); err != nil {
			return "", fmt.Errorf("%s cannot be made 0700, and it says which of your desks is the private one: %w", dir, err)
		}
	}
	path := filepath.Join(string(dir), d.Name+".yaml")
	out, err := yaml.Marshal(d)
	if err != nil {
		return "", err
	}
	header := "# Written by zde desk snapshot. Apps are not captured yet - add\n" +
		"# them by hand (docs/model.md, section 5).\n"
	if err := writeNew(path, append([]byte(header), out...)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%s already exists: remove it to take a new snapshot of %q", path, d.Name)
		}
		return "", err
	}
	return path, nil
}

// writeNew puts content at path, or refuses because something is already there.
//
// Through a temp file and a link, which is the shape internal/zded/rules.go
// already uses for niri's dynamic config, and for the same reasons plus one.
// What it replaces was a Stat and then an os.WriteFile, which got three things
// wrong:
//
//   - The refusal was a Stat and the write was a separate call, so what it
//     tested and what it wrote to were two different moments. Anything
//     appearing at the name in between was written over.
//   - os.WriteFile follows a symlink, including a dangling one, so a link at
//     the name was a Stat that said "nothing there" followed by a create of
//     whatever the link pointed at, somewhere else entirely.
//   - It was neither atomic nor fsynced: a manifest half on the disk is a desk
//     that will not load, and the machine losing power is exactly when
//     somebody wants their arrangement written down.
//
// os.Link and not os.Rename, because refusing an existing file is this
// function's contract and a rename would replace one. link(2) fails with EEXIST
// and does it in the kernel, which is the same refusal with no window in it,
// and it does not follow a symlink at the new name either.
func writeNew(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	// 0600, which os.CreateTemp already gives it, and the link carries over.
	// Said out loud because it is a promise this file makes above.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), path)
}

// bytesMax bounds one manifest, and manifestsMax bounds how many are read.
//
// The arithmetic, so that the numbers are checkable rather than round. A
// manifest is a name, a monitor block per output, and an app block per app. The
// largest desk anybody has described is a handful of outputs with a dozen
// workspaces each and a dozen apps with a few mounts apiece, which is a couple
// of kilobytes of YAML; 64 KiB is thirty times that. And a person navigates
// desks by pressing a key per desk, so 256 of them is already more than the
// keyboard can reach - the cap is not there to stop somebody with a lot of
// desks, it is there so that a directory somebody pointed `-desks` at by
// mistake is a message instead of a daemon reading a filesystem.
//
// Both are refusals with a name attached rather than a truncation, because
// LoadDir already has somewhere to put those: they come back as Problems and
// end up in `zde status`, in front of the person who can move the file.
const (
	bytesMax     = 64 << 10
	manifestsMax = 256
)

// Load reads one manifest from a file.
//
// Bounded, and through internal/plainfile: this runs at startup and on every
// reconcile, over a directory whose contents are whatever is in it, so a FIFO
// named work.yaml used to stop zded dead - after the socket was bound and
// before anything was answering it, which is the worst moment there is to stop.
func Load(path string) (*Desk, error) {
	data, err := plainfile.Read(path, bytesMax)
	if err != nil {
		return nil, err
	}
	d, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// LoadDir reads every manifest in a directory, keyed by desk name, and says
// which ones it could not read.
//
// A directory that is not there is not an error: it is a machine with no desks
// declared yet, which is where everyone starts. The returned error is only for
// a directory that cannot be read at all.
//
// One file failing takes only that file. It used to take the whole read, and
// then the caller that mattered most swallowed the error and carried on with
// nothing - so a typo in one manifest silently undeclared every desk on the
// machine. A file a person edits by hand will be wrong sometimes; that is
// ordinary, and it should cost them that desk and no more. What is not
// acceptable is the silence, which is why the problems come back rather than
// being logged here: they end up in `zde status`, in front of somebody.
func LoadDir(dir string) (map[string]*Desk, []Problem, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]*Desk{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var problems []Problem
	out := map[string]*Desk{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if len(out)+len(problems) >= manifestsMax {
			// Said once, about the directory, rather than once per file: a
			// thousand lines in `zde status` is the same as no lines.
			problems = append(problems, Problem{
				Path: dir,
				Err:  fmt.Errorf("more than %d manifests here, so the rest are not read", manifestsMax),
			})
			break
		}
		d, err := Load(path)
		if err != nil {
			problems = append(problems, Problem{Path: path, Err: err})
			continue
		}
		if prev, dup := out[d.Name]; dup {
			// Both files are suspect and neither is obviously the intruder, so
			// the second one loses and says so. Dropping both would lose a desk
			// that works over a duplicate somebody probably made by copying it.
			problems = append(problems, Problem{
				Path: path,
				Err:  fmt.Errorf("desk %q is already declared by another manifest", prev.Name),
			})
			continue
		}
		out[d.Name] = d
	}
	// Deterministic, because this is printed: os.ReadDir is sorted, so the
	// problems are already in filename order and stay that way.
	return out, problems, nil
}

func contains(all []string, s string) bool {
	for _, v := range all {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
