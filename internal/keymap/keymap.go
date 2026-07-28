// Package keymap turns the keymap source of truth (common/keymap/keymap.yaml)
// into niri binds and a cheatsheet. Actions first, physical keys a scheme on
// top (docs/vision.md, principle 2): the YAML assigns chords, the registry
// pins meaning, and everything else is validation.
package keymap

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type sourceFile struct {
	Binds []sourceBind `yaml:"binds"`
}

type sourceBind struct {
	Action string `yaml:"action"`
	Key    string `yaml:"key"`
	// Via is the chord a person presses, when it is not the one niri sees.
	// The input daemon turns it into Key, because niri binds one modifier set
	// plus one key and cannot express a held Tab or a sequence.
	Via string `yaml:"via"`
}

// Bind is one resolved chord -> action pair.
type Bind struct {
	Action string // as written, e.g. "window.focus left"
	Key    string // niri chord, e.g. "Mod+Left"
	Via    string // what is pressed to produce Key, "" when they are the same
	Arg    string // parametric argument, "" for exact actions
	Entry  Entry
}

// Pressed is the chord to tell someone about: what their fingers do, which is
// the input daemon's chord when there is one and niri's otherwise.
func (b Bind) Pressed() string {
	if b.Via != "" {
		return b.Via
	}
	return b.Key
}

type Keymap struct {
	Binds []Bind
}

func Load(path string) (*Keymap, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	km, err := Parse(raw)
	if err != nil {
		// Which file, since the generator is run on a path the caller chose.
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return km, nil
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
	seen := map[string]string{}    // canonical chord -> action
	pressed := map[string]string{} // what fingers do -> action
	for i, b := range src.Binds {
		bind, err := resolve(b)
		if err != nil {
			return nil, fmt.Errorf("keymap: bind %d: %w", i+1, err)
		}
		// bind.Key is canonical, so a chord spelled two ways collides here
		// rather than in niri.
		if prev, dup := seen[bind.Key]; dup {
			return nil, fmt.Errorf("keymap: bind %d: %s bound to both %q and %q", i+1, bind.Key, prev, bind.Action)
		}
		seen[bind.Key] = bind.Action
		// And two chords a hand cannot tell apart are one chord, whatever the
		// input layer turns them into: the daemon can map a physical chord to
		// one key, so a second claim on it is a cheatsheet row that lies.
		if prev, dup := pressed[bind.Pressed()]; dup {
			return nil, fmt.Errorf("keymap: bind %d: %s is pressed for both %q and %q", i+1, bind.Pressed(), prev, bind.Action)
		}
		pressed[bind.Pressed()] = bind.Action
		km.Binds = append(km.Binds, bind)
	}
	return km, nil
}

var nameArg = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func resolve(src sourceBind) (Bind, error) {
	c, err := parseChord(src.Key)
	if err != nil {
		return Bind{}, fmt.Errorf("%q: %w", src.Action, err)
	}
	key := c.String()
	via, err := checkVia(src.Via, key)
	if err != nil {
		return Bind{}, fmt.Errorf("%q: %w", src.Action, err)
	}
	if e, ok := registry[src.Action]; ok && !e.parametric() {
		return Bind{Action: src.Action, Key: key, Via: via, Entry: e}, nil
	}
	id, arg, found := strings.Cut(src.Action, " ")
	if !found {
		return Bind{}, fmt.Errorf("unknown action %q", src.Action)
	}
	e, ok := registry[id]
	if !ok {
		return Bind{}, fmt.Errorf("unknown action %q", src.Action)
	}
	if !e.parametric() {
		return Bind{}, fmt.Errorf("action %q takes no argument, got %q", id, arg)
	}
	if err := checkArg(e, arg); err != nil {
		return Bind{}, fmt.Errorf("%q: %w", src.Action, err)
	}
	return Bind{Action: src.Action, Key: key, Via: via, Arg: arg, Entry: e}, nil
}

// checkVia validates the pressed chord. It is never written into KDL - niri
// only ever sees Key - so what it has to survive is the cheatsheet's table,
// where a pipe or a newline would break the row it sits in.
//
// A via that is not on an input-layer key is a mistake worth catching: it says
// the daemon rewrites a chord, and niri would be listening for the wrong one.
func checkVia(via, key string) (string, error) {
	if via == "" {
		if inputLayerKey(key) {
			// Nobody can press it. The key exists so the input layer has
			// something to emit, and without the chord that produces it the
			// cheatsheet prints a key no keyboard has.
			return "", fmt.Errorf("key %s is the input layer's: say which chord produces it with via", key)
		}
		return "", nil
	}
	if strings.ContainsAny(via, "|\n`") {
		return "", fmt.Errorf("via %q: no pipes, backticks or newlines, it goes in the cheatsheet table", via)
	}
	if !inputLayerKey(key) {
		return "", fmt.Errorf("via %q: only for keys the input daemon emits, not %s", via, key)
	}
	return via, nil
}

func checkArg(e Entry, arg string) error {
	if e.Arg == argName && !nameArg.MatchString(arg) {
		return fmt.Errorf("argument %q: want a lowercase name", arg)
	}
	return nil
}

var mods = map[string]bool{"Mod": true, "Ctrl": true, "Shift": true, "Alt": true}

// modOrder is how a chord gets spelled when several spellings mean the same
// thing. Without it Mod+Shift+q and Shift+Mod+q are two strings, pass as two
// binds, and collide inside niri - which rejects the whole config, not the one
// bind.
var modOrder = []string{"Mod", "Ctrl", "Alt", "Shift"}

// keyName is what niri takes as the key half of a bind: an xkb keysym name.
// Every one of those is letters, digits and underscore, and holding the key to
// that is also what keeps the generated KDL structural. The key is written into
// the config unquoted, so a key carrying a quote, a brace, a semicolon or a
// newline would close the bind node and open whatever came after it.
var keyName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// reserved chords stay unbound on purpose (common/keymap/keymap.yaml): they sit
// under the resting hand beside the modifier cluster and get brushed, so only
// their Shift variants bind.
var reserved = map[string]bool{"Mod+z": true, "Mod+x": true}

// chord is a parsed chord: a canonical modifier set plus the key it applies to.
type chord struct {
	mods []string
	key  string
}

func (c chord) String() string {
	return strings.Join(append(append([]string{}, c.mods...), c.key), "+")
}

// parseChord validates a chord and returns it in canonical spelling. It
// enforces the doctrine - every bind goes through Mod (Super is zde's), except
// hardware keys that exist for exactly one purpose - and rejects anything that
// would not survive being written into KDL.
func parseChord(s string) (chord, error) {
	parts := strings.Split(s, "+")
	key := parts[len(parts)-1]
	if key == "" {
		return chord{}, fmt.Errorf("chord %q: empty key", s)
	}
	if !keyName.MatchString(key) {
		return chord{}, fmt.Errorf("chord %q: %q is not a key name (niri wants an xkb keysym: letters, digits, underscore)", s, key)
	}
	// Vim notation: a bare letter is the unshifted key. "J" already implies
	// Shift, so spell it Mod+Shift+j and keep the base key lowercase.
	if len(key) == 1 && key[0] >= 'A' && key[0] <= 'Z' {
		return chord{}, fmt.Errorf("chord %q: write the base key lowercase (Mod+Shift+%s, not %s)", s, strings.ToLower(key), key)
	}
	held := map[string]bool{}
	for _, m := range parts[:len(parts)-1] {
		if !mods[m] {
			return chord{}, fmt.Errorf("chord %q: unknown modifier %q", s, m)
		}
		if held[m] {
			return chord{}, fmt.Errorf("chord %q: modifier %q repeated", s, m)
		}
		held[m] = true
	}
	if !held["Mod"] && !hardwareKey(key) {
		return chord{}, fmt.Errorf("chord %q: zde binds go through Mod (docs/vision.md, principle 2)", s)
	}
	c := chord{key: key}
	for _, m := range modOrder {
		if held[m] {
			c.mods = append(c.mods, m)
		}
	}
	if reserved[c.String()] {
		return chord{}, fmt.Errorf("chord %q: reserved, it gets brushed by the resting hand (common/keymap/keymap.yaml)", s)
	}
	return c, nil
}

func hardwareKey(key string) bool {
	return key == "Print" || strings.HasPrefix(key, "XF86") || inputLayerKey(key)
}

// inputLayerKey is one of the keys no keyboard has. The input daemon emits
// them for chords niri cannot express - a held Tab, a sequence - so niri sees
// an ordinary key press and binds it like any other.
//
// They are exempt from going through Mod for the same reason the media keys
// are: no keyboard has them, so nothing else presses them. The chord a person
// actually presses is the bind's `via`, and that is what the cheatsheet shows.
//
// Reaching niri as themselves takes the fkeys:basic_13-24 xkb option, which
// niri/config.kdl sets. Without it the default layout gives these keycodes
// XF86Tools, XF86Launch5 and the like - and F20 gives XF86AudioMicMute, which
// this keymap binds. The bind would load and never fire.
func inputLayerKey(key string) bool {
	switch key {
	case "F13", "F14", "F15", "F16", "F17", "F18", "F19", "F20":
		return true
	}
	return false
}
