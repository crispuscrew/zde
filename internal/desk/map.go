package desk

import (
	"sort"
	"strconv"
)

// Workspace is what niri reports about one workspace: the name it carries and
// the output it is on right now. Those two can disagree, and telling apart the
// two reasons they disagree is most of what this file does.
type Workspace struct {
	Name   string
	Output string
}

// Rename is a workspace whose name no longer tells the truth about where it
// is, because someone moved it. Invariant 1 (docs/model.md): a workspace moved
// between monitors gets renamed so the name stays truthful, since the name is
// the record.
type Rename struct {
	From Name
	To   Name
}

// Conflict is a rename that cannot be applied because the truthful name is
// taken. Ordinals restart per monitor, so vshop.DP-1.1 arriving on a monitor
// that already has vshop.HDMI-A-1.1 is ordinary rather than exotic. Renaming
// anyway would put two workspaces under one name and break invariant 1 inside
// the map itself, so the collision comes back for the caller to resolve: only
// it knows whether to mint a free ordinal or leave the workspace alone.
type Conflict struct {
	Workspace Name
	Wanted    Name
}

// Map is the desk map, rebuilt from workspace names alone.
type Map struct {
	// Desks holds the zde-owned workspaces, keyed by desk name. The regulars
	// band is a key like any other; what makes it special is that no desk's
	// band contains it.
	Desks map[string][]Name

	// Foreign is every workspace that is not zde-named: niri's own unnamed
	// workspaces, and anything a user or another tool named. Until adoption
	// claims one it is nobody's, and it stays untouched.
	Foreign []Workspace

	// Renames is what has to be applied to make the names truthful again.
	Renames []Rename

	// Conflicts are the renames that would collide with a name in use.
	Conflicts []Conflict

	// Displaced is a workspace sitting somewhere other than the monitor its
	// name claims, because that monitor is gone. Its name is not a lie and
	// must not be corrected: it is the only record of where the workspace
	// belongs, and invariant 5 puts it back there on replug. A manifest
	// records home too, but only for workspaces a manifest declares - an
	// adopted one has the name and nothing else.
	Displaced []Name
}

// Rebuild reconstructs the desk map from what niri reports. It is the whole
// recovery story: no state of ours is consulted, so a zded that just started
// and a zded that has been running for a week see the same map.
//
// connected is the set of outputs niri currently has. It is what separates a
// move from a migration: without it, an unplugged monitor looks exactly like
// the user dragging every one of its workspaces somewhere else, and correcting
// those names would erase the home monitor of every workspace no manifest
// declares. Pass nil only when the caller genuinely does not know, which
// disables renaming rather than guessing.
func Rebuild(workspaces []Workspace, connected []string) *Map {
	m := &Map{Desks: map[string][]Name{}}

	isConnected := make(map[string]bool, len(connected))
	for _, o := range connected {
		isConnected[o] = true
	}
	// Every name in play, so a rename cannot be handed a name already in use.
	taken := map[string]bool{}
	for _, w := range workspaces {
		if _, err := ParseName(w.Name); err == nil {
			taken[w.Name] = true
		}
	}

	for _, w := range workspaces {
		name, err := ParseName(w.Name)
		if err != nil {
			m.Foreign = append(m.Foreign, w)
			continue
		}
		switch {
		case w.Output == "" || w.Output == name.Monitor:
			// Where it says it is, or niri is not saying.
		case len(isConnected) == 0:
			// No output list, so a move and a migration are indistinguishable
			// and renaming would be a guess. A missing rename is recoverable;
			// a wrong one erases home.
		case !isConnected[name.Monitor]:
			// Its monitor is gone and niri parked it on a survivor. The name
			// still records home, so leave it alone and say where it sits.
			m.Displaced = append(m.Displaced, name)
		case !isConnected[w.Output]:
			// niri reports an output it does not have. Nothing sane to do.
		default:
			// Both monitors are real, so this was a move: correct the name.
			truthful := name
			truthful.Monitor = w.Output
			if taken[truthful.String()] {
				m.Conflicts = append(m.Conflicts, Conflict{Workspace: name, Wanted: truthful})
				break
			}
			delete(taken, name.String())
			taken[truthful.String()] = true
			m.Renames = append(m.Renames, Rename{From: name, To: truthful})
			name = truthful
		}
		m.Desks[name.Desk] = append(m.Desks[name.Desk], name)
	}
	for desk := range m.Desks {
		sort.Slice(m.Desks[desk], func(i, j int) bool {
			return less(m.Desks[desk][i], m.Desks[desk][j])
		})
	}
	return m
}

// less orders workspaces by monitor, then labels before ordinals, then
// ordinals by number so that 2 comes before 10. This is a stable order for
// comparing and showing a map. It is not the strip's vertical order, which is
// niri's to say and reaches us only as a workspace index we do not yet carry.
func less(a, b Name) bool {
	if a.Monitor != b.Monitor {
		return a.Monitor < b.Monitor
	}
	an, aIsNum := a.Ordinal()
	bn, bIsNum := b.Ordinal()
	switch {
	case aIsNum && bIsNum:
		return an < bn
	case aIsNum != bIsNum:
		return bIsNum
	default:
		return a.Slot < b.Slot
	}
}

// Names lists every desk in the map, regulars included, in a stable order.
func (m *Map) Names() []string {
	out := make([]string, 0, len(m.Desks))
	for d := range m.Desks {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// Band is the workspaces a desk's scrolling is clamped to on one monitor
// (invariant 4). It is per monitor because the strip is: each monitor owns its
// own vertical strip (docs/model.md, section 1), so a band spanning monitors
// would clamp a scroll to workspaces that are not on the screen doing the
// scrolling.
//
// Regulars are in no desk's band: reachable from every desk by an explicit
// action, never by scrolling into them.
func (m *Map) Band(desk, monitor string) []Name {
	var out []Name
	for _, n := range m.Desks[desk] {
		if n.Monitor == monitor {
			out = append(out, n)
		}
	}
	return out
}

// Workspaces is every workspace a desk owns, across monitors, as a copy: the
// map's own slices stay its own, so a caller that sorts or appends cannot
// reorder the map underneath everyone else.
func (m *Map) Workspaces(desk string) []Name {
	return append([]Name(nil), m.Desks[desk]...)
}

// Ordinal reads the slot back as a number. The second result is false for a
// label, which is the distinction between vshop.DP-1.code, declared by a
// manifest, and vshop.DP-1.2, which adoption minted.
func (n Name) Ordinal() (int, bool) {
	i, err := strconv.Atoi(n.Slot)
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}
