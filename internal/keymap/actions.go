package keymap

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Action is one thing a person can ask zde to do by name: what it is called,
// what it does, the key that would have done it, and what runs it.
//
// It is the palette's row (docs/vision.md, section 2), and the CLI's when no
// shell is up. What decides whether picking one does anything on this machine
// is not here: that takes a PATH and a compositor, and it is asked where the
// palette is built (internal/zded).
type Action struct {
	Name  string
	Group string
	Desc  string
	// Key is the chord bound to it, empty where none is. Plenty of actions have
	// none, for more than one reason: `desk.next` and `zde workspace next` work
	// and are reached by other keys, `system.bluetooth` shares a surface with
	// the key beside it, and a few say where they are registered that being
	// reachable by name is the point (system.doctor, the two net cuts). A blank
	// here is a fact about the keymap, not a fault.
	Key string
	// Native and Spawn are what the bind is made of: a niri action line, or an
	// argv, exactly one of them, with any argument already filled in.
	Native string
	Spawn  []string
	// Performs says niri will do this native when zde asks over the socket, and
	// not only when the key fires. Meaningless for a spawn, and false for the
	// eight natives niri takes from a bind and refuses from us (registry.go).
	Performs bool
	// Live says something is written behind it. Most of the keymap is bound to
	// commands nobody has written yet (README, Missing), and a palette that did
	// not say so would offer a row that does nothing when picked - which is the
	// silent key it exists to explain.
	Live bool
}

// Actions is every action a person can ask for by name, in the action map's
// order, with the keys this machine has bound to them.
//
// Two sources, and which half comes from which is the design. What actions
// there are comes from the registry, because that is the action map in code and
// it holds the ones no chord reaches; a list built from the cheatsheet alone
// would be missing exactly those, and they are the ones a palette is for.
//
// The key beside each one comes from the cheatsheet the config build wrote
// (nix/zde-config.nix), for the reason `zde keys` prints that file rather than
// rendering its own: that file and the binds niri actually loaded came out of
// one build and cannot disagree, where a registry compiled into a zded from
// another build can. So the palette answers "what can this machine do" from
// here, and "which key does it" from the file.
//
// An action the cheatsheet has and this build does not know is left out. There
// is nothing to run it with, and a row that can only fail is worse than a row
// that is missing. An action with no key is not: half the rows with no chord
// work, and the palette is how they are reached.
func Actions(cheatsheet []byte) []Action {
	keyOf := map[string]string{}
	var out []Action
	seen := map[string]bool{}
	for _, l := range parseText(cheatsheet) {
		// The first chord wins. The keymap writes the letter chord first and
		// the arrows and hardware keys after it (common/keymap/keymap.yaml), and
		// a row that names two keys names neither.
		if _, taken := keyOf[l.action]; !taken {
			keyOf[l.action] = l.key
		}
		e, arg, ok := behind(l.action)
		if !ok || arg == "" || seen[l.action] {
			continue
		}
		// An action that takes an argument exists only with the argument filled
		// in: there is no `app.launch` to run, there is `app.launch terminal`,
		// and the keymap is where the arguments live. The description is the
		// file's, which is the same one the cheatsheet shows.
		seen[l.action] = true
		out = append(out, action(l.action, e, arg, l.desc))
	}
	for id, e := range registry {
		if e.parametric() {
			continue
		}
		out = append(out, action(id, e, "", e.Desc))
	}
	order := map[string]int{}
	for i, g := range groups {
		order[g] = i
	}
	// The action map's order, then the name: a list whose rows move between two
	// presses of the key is one nobody can learn (internal/zded, sortWindows).
	// Go's map iteration above is deliberately unordered, so this is not a
	// nicety, it is what makes the answer the same twice.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return order[out[i].Group] < order[out[j].Group]
		}
		return out[i].Name < out[j].Name
	})
	for i := range out {
		out[i].Key = keyOf[out[i].Name]
	}
	return out
}

// action is one row: what the registry says, with the argument the keymap
// supplied put where the bind would have put it.
func action(name string, e Entry, arg, desc string) Action {
	a := Action{
		Name:     name,
		Group:    e.Group,
		Desc:     desc,
		Native:   e.Native,
		Spawn:    e.Spawn,
		Performs: e.performs,
		Live:     e.written || e.Native != "",
	}
	switch {
	case arg == "":
		return a
	case a.Native != "":
		// Not Sprintf, for the reason emit.go gives: niri action lines carry
		// literal percent signs.
		a.Native = strings.Replace(a.Native, argPlaceholder, arg, 1)
	default:
		// A copy with no spare capacity, so appending the argument cannot reach
		// the registry's own slice.
		a.Spawn = append(a.Spawn[:len(a.Spawn):len(a.Spawn)], arg)
	}
	return a
}

// TextPath is where the config build leaves the cheatsheet: the file `zde keys`
// prints and the palette reads its keys off (nix/zde-config.nix, nix/home.nix).
func TextPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "zde", "keymap.txt")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "zde", "keymap.txt")
}
