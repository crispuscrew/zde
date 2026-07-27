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

// LoadDir reads every manifest in a directory, keyed by desk name.
//
// A directory that is not there is not an error: it is a machine with no desks
// declared yet, which is where everyone starts. A manifest that does not parse
// is an error naming the file - the rest of the desks working while one is
// quietly missing is worse than being told.
func LoadDir(dir string) (map[string]*Desk, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]*Desk{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]*Desk{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		d, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if prev, dup := out[d.Name]; dup {
			return nil, fmt.Errorf("%s: desk %q is already declared by another manifest", filepath.Join(dir, e.Name()), prev.Name)
		}
		out[d.Name] = d
	}
	return out, nil
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
