// Package keymap turns the keymap source of truth (common/keymap/keymap.yaml)
// into niri binds and a cheatsheet. Actions first, physical keys a scheme on
// top (docs/vision.md, principle 2): the YAML assigns chords, the registry
// pins meaning, and everything else is validation.
package keymap

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type sourceFile struct {
	Binds []sourceBind `yaml:"binds"`
}

type sourceBind struct {
	Action string `yaml:"action"`
	Key    string `yaml:"key"`
}

// Bind is one resolved chord -> action pair.
type Bind struct {
	Action string // as written, e.g. "window.focus left"
	Key    string // niri chord, e.g. "Mod+Left"
	Arg    string // parametric argument, "" for exact actions
	Entry  Entry
}

type Keymap struct {
	Binds []Bind
}

func Load(path string) (*Keymap, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Keymap, error) {
	var src sourceFile
	if err := yaml.Unmarshal(raw, &src); err != nil {
		return nil, fmt.Errorf("keymap: %w", err)
	}
	if len(src.Binds) == 0 {
		return nil, fmt.Errorf("keymap: no binds")
	}
	km := &Keymap{}
	seen := map[string]string{} // chord -> action
	for _, b := range src.Binds {
		bind, err := resolve(b)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[bind.Key]; dup {
			return nil, fmt.Errorf("keymap: %s bound to both %q and %q", bind.Key, prev, bind.Action)
		}
		seen[bind.Key] = bind.Action
		km.Binds = append(km.Binds, bind)
	}
	return km, nil
}

var nameArg = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func resolve(src sourceBind) (Bind, error) {
	if err := checkChord(src.Key); err != nil {
		return Bind{}, fmt.Errorf("keymap: %q: %w", src.Action, err)
	}
	if e, ok := registry[src.Action]; ok && !e.parametric() {
		return Bind{Action: src.Action, Key: src.Key, Entry: e}, nil
	}
	id, arg, found := strings.Cut(src.Action, " ")
	if !found {
		return Bind{}, fmt.Errorf("keymap: unknown action %q", src.Action)
	}
	e, ok := registry[id]
	if !ok || !e.parametric() {
		return Bind{}, fmt.Errorf("keymap: unknown action %q", src.Action)
	}
	if err := checkArg(e, arg); err != nil {
		return Bind{}, fmt.Errorf("keymap: %q: %w", src.Action, err)
	}
	return Bind{Action: src.Action, Key: src.Key, Arg: arg, Entry: e}, nil
}

func checkArg(e Entry, arg string) error {
	switch e.Arg {
	case argNum:
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > 50 {
			return fmt.Errorf("argument %q: want a workspace number", arg)
		}
	case argName:
		if !nameArg.MatchString(arg) {
			return fmt.Errorf("argument %q: want a lowercase name", arg)
		}
	}
	return nil
}

var mods = map[string]bool{"Mod": true, "Ctrl": true, "Shift": true, "Alt": true}

// checkChord enforces the doctrine: every bind goes through Mod (Super is
// zde's), except hardware keys that exist for exactly one purpose.
func checkChord(chord string) error {
	parts := strings.Split(chord, "+")
	key := parts[len(parts)-1]
	if key == "" {
		return fmt.Errorf("chord %q: empty key", chord)
	}
	hasMod := false
	for _, m := range parts[:len(parts)-1] {
		if !mods[m] {
			return fmt.Errorf("chord %q: unknown modifier %q", chord, m)
		}
		hasMod = hasMod || m == "Mod"
	}
	if !hasMod && !hardwareKey(key) {
		return fmt.Errorf("chord %q: zde binds go through Mod (docs/vision.md, principle 2)", chord)
	}
	return nil
}

func hardwareKey(key string) bool {
	return key == "Print" || strings.HasPrefix(key, "XF86")
}
