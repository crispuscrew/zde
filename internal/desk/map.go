package desk

import "sort"

// Workspace is what niri reports about one workspace: the name it carries and
// the output it is on right now. Those two can disagree - a workspace moved
// between monitors keeps its old name until someone fixes it - and reconciling
// that disagreement is most of what this file does.
type Workspace struct {
	Name   string
	Output string
}

// Rename is a workspace whose name no longer tells the truth about where it
// is. Invariant 1 (docs/model.md): a workspace moved between monitors gets
// renamed so the name stays truthful, because the name is the record.
type Rename struct {
	From Name
	To   Name
}

// Map is the desk map, rebuilt from workspace names alone.
type Map struct {
	// Desks holds the zde-owned workspaces, keyed by desk name. The regulars
	// band is a key like any other; what makes it special is that no desk's
	// band contains it.
	Desks map[string][]Name

	// Foreign is every workspace that is not zde-named: niri's own unnamed
	// workspaces, and anything a user or another tool named. Adoption turns
	// these into desk workspaces (invariant 3); until then they are nobody's
	// and stay untouched.
	Foreign []Workspace

	// Renames is what has to be applied to make the names truthful again.
	Renames []Rename
}

// Rebuild reconstructs the desk map from what niri reports. It is the whole
// recovery story: no state of ours is consulted, so a zded that just started
// and a zded that has been running for a week see the same map.
func Rebuild(workspaces []Workspace) *Map {
	m := &Map{Desks: map[string][]Name{}}
	for _, w := range workspaces {
		name, err := ParseName(w.Name)
		if err != nil {
			m.Foreign = append(m.Foreign, w)
			continue
		}
		// niri is the authority on where a workspace is; the name is the
		// authority on whose it is. Where they disagree about the monitor,
		// niri wins and the name gets corrected.
		if w.Output != "" && w.Output != name.Monitor {
			truthful := name
			truthful.Monitor = w.Output
			m.Renames = append(m.Renames, Rename{From: name, To: truthful})
			name = truthful
		}
		m.Desks[name.Desk] = append(m.Desks[name.Desk], name)
	}
	for desk := range m.Desks {
		sort.Slice(m.Desks[desk], func(i, j int) bool {
			a, b := m.Desks[desk][i], m.Desks[desk][j]
			if a.Monitor != b.Monitor {
				return a.Monitor < b.Monitor
			}
			return a.Slot < b.Slot
		})
	}
	return m
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

// Band is the workspaces a desk's scrolling is clamped to (invariant 4).
// Regulars are deliberately not in it: they are reachable from every desk by
// an explicit action, never by scrolling into them.
func (m *Map) Band(desk string) []Name {
	if desk == Regulars {
		return m.Desks[Regulars]
	}
	return m.Desks[desk]
}
