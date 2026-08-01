// Package apps resolves the logical names the keymap uses - "terminal",
// "editor" - into something to run.
//
// The keymap binds actions and not programs (docs/vision.md, principle 2):
// Mod+t is `app.launch terminal`, and which terminal that is belongs to the
// machine. Layer 1 writes the answers here, so changing your terminal is a line
// in a home-manager config rather than an edit to the keymap that everything
// else is generated from.
//
// These become zinc apps launched by zcr in a sandbox (docs/delivery.md, layer
// 2). They are host commands today because zcr is not here yet, and the shape
// does not change when it arrives: a name goes in, something runs.
package apps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Apps is what layer 1 writes: a logical name to an argv.
//
// An argv and not a command line, so nothing has to agree about quoting: a path
// with a space in it is a path with a space in it.
type Apps map[string][]string

// DefaultPath is where layer 1 writes them.
func DefaultPath() string { return Path("apps.json") }

// Path is a file in zde's config directory. There are two name-to-argv maps
// there now - the apps, and ask's tiers (internal/zded, ask.go) - and where
// zde's config lives should be one answer rather than a copy per reader.
func Path(name string) string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "zde", name)
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "zde", name)
}

// Load reads the map. A missing file is not an error here: it is a machine
// where nothing is configured, and the caller says that better than this can
// because it knows which name was asked for.
func Load(path string) (Apps, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Apps{}, nil
	}
	if err != nil {
		return nil, err
	}
	var a Apps
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

// Argv is what to run for a name.
//
// The error names what is configured, because the alternative is a key that
// does nothing and a person guessing whether they typed the name wrong or the
// machine has none.
func (a Apps) Argv(name string) ([]string, error) {
	if argv, ok := a[name]; ok && len(argv) > 0 {
		return argv, nil
	}
	if len(a.Names()) == 0 {
		return nil, fmt.Errorf("nothing is configured to run: set zde.apps in your "+
			"home-manager config, which is what writes %s", DefaultPath())
	}
	return nil, fmt.Errorf("no app called %q; there is %v", name, a.Names())
}

// Names is what is configured, sorted: for the error above, and for `zde app
// list`, which is how somebody finds out what their machine can start.
func (a Apps) Names() []string {
	out := make([]string, 0, len(a))
	for n, argv := range a {
		if len(argv) > 0 {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
