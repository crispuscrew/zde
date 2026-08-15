package desk

import "sort"

// Workspace is what niri reports about one workspace: the name it carries and
// the output it is on right now. Those two can disagree, and telling apart the
// two reasons they disagree is most of what this file does.
type Workspace struct {
	// ID is niri's, stable within a session and not across one. It is not
	// what the map keys on - names are - but a workspace with no name can be
	// addressed no other way, which is what adoption needs.
	ID uint64
	// Idx is where it sits in its output's strip, which is niri's to say and
	// the only order a scroll can honestly follow. It is the order the map
	// keeps a desk's workspaces in, so that everything reading one - a switch,
	// a snapshot, a scroll - agrees about which end is the top.
	Idx    uint8
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

// Conflict is a workspace Rebuild noticed and would not touch, with the reason.
// Rebuild cannot fail - a recovery path that refuses to run is not one - but
// declining to act is not the same as noticing nothing, and everything here is
// a case where acting would have made the map wrong.
type Conflict struct {
	Workspace Workspace
	Reason    string
}

// Map is the desk map, rebuilt from workspace names alone. Its fields are
// unexported: the IPC layer and the journal will both hold one, and a shared
// read model that any caller can reach into and reorder is not one.
type Map struct {
	desks map[string][]Name
	// at is the output a workspace is on right now, which is not always the
	// one its name says: an unplugged monitor leaves its workspaces parked on
	// a survivor with home still recorded in the name (invariant 1).
	at map[string]string
	// idx is its place in that output's strip, so a band can be walked in the
	// order the screen has it rather than the order names sort in.
	idx       map[string]uint8
	foreign   []Workspace
	renames   []Rename
	conflicts []Conflict
	displaced []Name
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
	m := &Map{desks: map[string][]Name{}, at: map[string]string{}, idx: map[string]uint8{}}

	isConnected := make(map[string]bool, len(connected))
	for _, o := range connected {
		isConnected[o] = true
	}
	// Every name in play, so a rename is never handed a name already in use.
	taken := map[string]bool{}
	for _, w := range workspaces {
		if _, err := ParseName(w.Name); err == nil {
			taken[w.Name] = true
		}
	}
	placed := map[string]bool{}

	for _, w := range workspaces {
		name, err := ParseName(w.Name)
		if err != nil {
			m.foreign = append(m.foreign, w)
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
			m.displaced = append(m.displaced, name)
		case !isConnected[w.Output]:
			m.conflicts = append(m.conflicts, Conflict{w, "niri reports an output it does not have"})
		default:
			// Both monitors are real, so this was a move: correct the name.
			// Through NewName, because the output is niri's string and not
			// ours - one that is not a connector would mint a name nothing can
			// read back, and the workspace would leave its desk for good.
			truthful, err := NewName(name.Desk, w.Output, name.Slot)
			if err != nil {
				m.conflicts = append(m.conflicts, Conflict{w, err.Error()})
				break
			}
			if taken[truthful.String()] {
				// Ordinals restart per monitor, so this is ordinary rather
				// than exotic. Only the caller can mint a free slot.
				m.conflicts = append(m.conflicts, Conflict{w, "the truthful name " + truthful.String() + " is taken"})
				break
			}
			delete(taken, name.String())
			taken[truthful.String()] = true
			m.renames = append(m.renames, Rename{From: name, To: truthful})
			name = truthful
		}
		// Two workspaces under one name would break invariant 1 inside the map
		// that reports it, and would leave Rename.From addressing either one.
		// The second one is not ours: we cannot say which workspace the name
		// owns, so it goes back with the unnamed and the foreign, where
		// adoption can give it a name of its own.
		if placed[name.String()] {
			m.conflicts = append(m.conflicts, Conflict{w, "another workspace is already named " + name.String()})
			m.foreign = append(m.foreign, w)
			continue
		}
		placed[name.String()] = true
		m.desks[name.Desk] = append(m.desks[name.Desk], name)
		// Where it is and where it sits, as niri has it. An output niri did
		// not say falls back to the name, which is the only other answer.
		where := w.Output
		if where == "" {
			where = name.Monitor
		}
		m.at[name.String()] = where
		m.idx[name.String()] = w.Idx
	}

	// A desk's workspaces are kept by monitor, and inside a monitor by the
	// strip. The monitor comes first because a caller walking one monitor's run
	// is what a switch does (SwitchPlan), and the strip second because that is
	// the only order that is a fact rather than an opinion.
	//
	// It used to sort names inside the monitor too, and that made two answers
	// disagree with the screen: a switch with nothing remembered landed on
	// whichever label sorted first, which is the bottom of the strip as often
	// as the top - and `zde workspace next` from the bottom does nothing. A
	// snapshot wrote the same order into a manifest, so a strip reading zsh
	// then agent came back as agent then zsh.
	for d := range m.desks {
		band := m.desks[d]
		sort.Slice(band, func(i, j int) bool {
			a, b := band[i], band[j]
			if a.Monitor != b.Monitor {
				return a.Monitor < b.Monitor
			}
			if ai, bi := m.idx[a.String()], m.idx[b.String()]; ai != bi {
				return ai < bi
			}
			// Same index: workspaces niri gave none, which is every workspace
			// in a map a caller built by hand. Names order those, so the map
			// still does not depend on the order niri listed things in.
			return less(a, b)
		})
	}
	// Sorted so that the map does not depend on the order niri happened to
	// list things in: a shell diffing two polls should see churn only when
	// something changed.
	sort.Slice(m.foreign, func(i, j int) bool {
		if m.foreign[i].Output != m.foreign[j].Output {
			return m.foreign[i].Output < m.foreign[j].Output
		}
		return m.foreign[i].Name < m.foreign[j].Name
	})
	sort.Slice(m.renames, func(i, j int) bool { return less(m.renames[i].From, m.renames[j].From) })
	sort.Slice(m.displaced, func(i, j int) bool { return less(m.displaced[i], m.displaced[j]) })
	sort.Slice(m.conflicts, func(i, j int) bool {
		return m.conflicts[i].Workspace.Name < m.conflicts[j].Workspace.Name
	})
	return m
}

// less orders workspaces by monitor, then labels before ordinals, then
// ordinals by number so that 2 comes before 10. This is a stable order for
// comparing and showing a map, and for the lists that are not a strip: what has
// to be renamed, what is displaced. Inside a band it is only the tie-break for
// workspaces niri gave the same index, because the strip's vertical order is
// niri's to say and the index is how it says it.
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

// DeskNames lists every desk in the map, regulars included, in a stable order.
func (m *Map) DeskNames() []string {
	out := make([]string, 0, len(m.desks))
	for d := range m.desks {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// Band is the workspaces a desk's scrolling is clamped to on one monitor
// (invariant 4). It is per monitor because the strip is: each monitor owns its
// own vertical strip (docs/model.md, section 1), so a band spanning monitors
// would clamp a scroll against workspaces that are not on the screen doing the
// scrolling.
//
// Regulars are in no desk's band: reachable from every desk by an explicit
// action, never by scrolling into them.
//
// The screen is where the workspaces are now, not where their names say they
// belong. Those disagree exactly when a monitor was unplugged and niri parked
// its workspaces on a survivor: home stays in the name so it can go back, and
// a band that read the name would be empty on the only screen left.
//
// The order is niri's, because the strip is niri's. The map's own order is
// niri's too, and the sort below is still not redundant: this groups by the
// screen a workspace is on, and an unplugged monitor puts two monitors' runs on
// one screen - which the map keeps apart and a scroll must not.
func (m *Map) Band(desk, output string) []Name {
	var out []Name
	for _, n := range m.desks[desk] {
		if m.at[n.String()] == output {
			out = append(out, n)
		}
	}
	// Stable, so that workspaces niri gave the same index - or none at all,
	// which is every workspace when the caller built the map by hand - keep
	// the map's own order instead of an arbitrary one.
	sort.SliceStable(out, func(i, j int) bool {
		return m.idx[out[i].String()] < m.idx[out[j].String()]
	})
	return out
}

// Workspaces is every workspace a desk owns, across monitors: by monitor, and
// down each monitor's strip in the order the screen has it. A snapshot writes
// that order into a manifest and a switch enters at the front of it, so it is
// the strip's and not a sort of its own.
func (m *Map) Workspaces(desk string) []Name {
	return append([]Name(nil), m.desks[desk]...)
}

// Regulars is the band reachable from every desk.
func (m *Map) Regulars() []Name { return m.Workspaces(Regulars) }

// Foreign is every workspace that is not zde-named: niri's own unnamed
// workspaces, and anything a user or another tool named. Until adoption claims
// one it is nobody's, and it stays untouched. The output comes with it, since
// naming one into a desk needs to know where it is.
func (m *Map) Foreign() []Workspace { return append([]Workspace(nil), m.foreign...) }

// Renames is what has to be applied to make the names truthful again.
func (m *Map) Renames() []Rename { return append([]Rename(nil), m.renames...) }

// Conflicts is what Rebuild noticed and would not touch.
func (m *Map) Conflicts() []Conflict { return append([]Conflict(nil), m.conflicts...) }

// Displaced is the workspaces sitting somewhere other than the monitor their
// name claims, because that monitor is gone. Their names are not lies and must
// not be corrected: the name is the only record of where the workspace
// belongs, and invariant 5 puts it back there on replug. A manifest records
// home too, but only for the workspaces a manifest declares - an adopted one
// has the name and nothing else.
func (m *Map) Displaced() []Name { return append([]Name(nil), m.displaced...) }
