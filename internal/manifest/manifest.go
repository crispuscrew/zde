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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/crispuscrew/zde/internal/desk"
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

// App is one application the desk runs. Nothing here launches it yet.
type App struct {
	App        string            `yaml:"app"`
	Instance   string            `yaml:"instance"`
	Mounts     map[string]string `yaml:"mounts"`
	Monitor    string            `yaml:"monitor"`
	Workspace  string            `yaml:"workspace"`
	Background string            `yaml:"background"`
}

// Policies is the desk's own settings.
type Policies struct {
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
func FromMap(m *desk.Map, name string) (*Desk, error) {
	workspaces := m.Workspaces(name)
	if len(workspaces) == 0 {
		return nil, fmt.Errorf("desk %q has no workspaces to write down", name)
	}
	d := &Desk{Name: name, Monitors: map[string]Monitor{}}
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
	if err := os.MkdirAll(string(dir), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(string(dir), d.Name+".yaml")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists: remove it to take a new snapshot of %q", path, d.Name)
	}
	out, err := yaml.Marshal(d)
	if err != nil {
		return "", err
	}
	header := "# Written by zde desk snapshot. Apps are not captured yet - add\n" +
		"# them by hand (docs/model.md, section 5).\n"
	if err := os.WriteFile(path, append([]byte(header), out...), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Load reads one manifest from a file.
func Load(path string) (*Desk, error) {
	data, err := os.ReadFile(path)
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
