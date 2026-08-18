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
	// at is the screen a workspace is on right now, which is not always the
	// monitor its name says: a monitor that is gone - unplugged, or switched
	// off by a closing lid - leaves its workspaces parked on a survivor with
	// home still recorded in the name (invariant 1). Anything that asks "which
	// screen" has to read this and not the name; reading the name is the whole
	// of what went wrong in a desk switch focusing one screen twice.
	//
	// Empty is a workspace on no screen: niri reports no output for it and the
	// monitor in its name is switched off. It is in no band, so nothing scrolls
	// into it and no switch lands on it.
	at map[string]string
	// idx is its place in that output's strip, so a band can be walked in the
	// order the screen has it rather than the order names sort in.
	idx map[string]uint8
	// screens is what niri was drawing on when this map was built. Empty is a
	// caller who did not say, which is not a machine with no screens.
	screens   []string
	foreign   []Workspace
	renames   []Rename
	conflicts []Conflict
	displaced []Name
}

// Rebuild reconstructs the desk map from what niri reports. It is the whole
// recovery story: no state of ours is consulted, so a zded that just started
// and a zded that has been running for a week see the same map.
//
// The map is a function of the set of workspaces, not of the order niri listed
// them in. That is what the two halves below are for: the first reads each
// workspace on its own, the second decides the renames over all of them at
// once, because whether one can have the name it wants depends on what the
// others end up called.
//
// screens is the set of outputs a workspace can be on right now - the ones niri
// has a layout monitor for, not every connector that has a cable in it
// (internal/niri, Screens). The distinction is the whole of this function: an
// output niri has switched off is still a connector and is no longer a screen,
// and its workspaces have already been parked elsewhere. Reading the connector
// list here would make that migration look exactly like the user dragging every
// one of those workspaces somewhere else, and correcting those names would
// erase the home monitor of every workspace no manifest declares.
//
// Pass nil only when the caller genuinely does not know. That decides nothing:
// no rename, and no claim that a monitor has gone either. A missing rename is
// recoverable; a wrong one erases home.
func Rebuild(workspaces []Workspace, screens []string) *Map {
	m := &Map{desks: map[string][]Name{}, at: map[string]string{}, idx: map[string]uint8{}}

	isScreen := make(map[string]bool, len(screens))
	for _, o := range screens {
		isScreen[o] = true
	}
	m.screens = append([]string(nil), screens...)
	sort.Strings(m.screens)

	// In an order of this function's own, so that which of two workspaces
	// sharing a name the map keeps is settled here and not by niri. A copy: the
	// caller's slice is niri's reply and stays as it came.
	in := append([]Workspace(nil), workspaces...)
	sort.Slice(in, func(i, j int) bool { return earlier(in[i], in[j]) })

	// held counts the workspaces under each zde name. A count, because a rename
	// addresses a workspace by naming it (internal/niri, RenameWorkspace), so
	// nothing may be renamed off a name two of them share.
	held := map[string]int{}
	for _, w := range in {
		if _, err := ParseName(w.Name); err == nil {
			held[w.Name]++
		}
	}

	// reading is one workspace on its own terms: nothing here consults another
	// workspace, so nothing here depends on where in the list it turned up.
	type reading struct {
		w    Workspace
		name Name   // the name it carries now
		to   Name   // the truthful name, when a move wants one
		move bool   // whether it is asking for that name
		at   string // the screen it is on, empty for none
	}
	var read []reading
	kept := map[string]bool{}
	for _, w := range in {
		name, err := ParseName(w.Name)
		if err != nil {
			m.foreign = append(m.foreign, w)
			continue
		}
		// Two workspaces under one name would break invariant 1 inside the map
		// that reports it, and would leave Rename.From addressing either one.
		// The second one is not ours: we cannot say which workspace the name
		// owns, so it goes back with the unnamed and the foreign, where
		// adoption can give it a name of its own.
		if kept[name.String()] {
			m.conflicts = append(m.conflicts, Conflict{w, "another workspace is already named " + name.String()})
			m.foreign = append(m.foreign, w)
			continue
		}
		kept[name.String()] = true

		r := reading{w: w, name: name, at: w.Output}
		// Whether the monitor in the name is still a screen. With no screen list
		// nothing knows, and false is the honest answer.
		homeGone := len(isScreen) > 0 && !isScreen[name.Monitor]
		switch {
		case w.Output == "":
			// niri is not saying. The name answers "which screen" only while
			// that monitor is one; where it is not, the workspace is on none,
			// and saying otherwise spends a desk switch's focus on a panel niri
			// is drawing nothing on. Not at home either way, so it is reported
			// the same way and keeps its name.
			if homeGone {
				m.displaced = append(m.displaced, name)
			} else {
				r.at = name.Monitor
			}
		case w.Output == name.Monitor:
			// Where it says it is.
		case len(isScreen) == 0:
			// No screen list, so a move and a migration are indistinguishable
			// and renaming would be a guess. A missing rename is recoverable;
			// a wrong one erases home.
		case homeGone:
			// Its monitor is not a screen any more and niri parked it on a
			// survivor. The name still records home, so leave it alone and say
			// where it sits.
			m.displaced = append(m.displaced, name)
		case !isScreen[w.Output]:
			// A workspace is on a layout monitor by construction, so this is
			// the gap between two IPC replies: the screen it was on stopped
			// being one while we were asking. Renaming on a list that has
			// already moved on is how home gets erased, so decline and say so.
			m.conflicts = append(m.conflicts, Conflict{w, "niri put it on an output it is no longer showing anything on"})
		case held[name.String()] > 1:
			// A shared name addresses neither workspace, so nothing is renamed
			// off one. The duplicate above already reports the pair.
		default:
			// Both monitors are real, so this was a move: correct the name
			// (invariant 1). Through NewName, because the output is niri's
			// string - one that is not a connector would mint a name nothing
			// can read back, and the workspace would leave its desk for good.
			truthful, err := NewName(name.Desk, w.Output, name.Slot)
			if err != nil {
				m.conflicts = append(m.conflicts, Conflict{w, err.Error()})
				break
			}
			r.to, r.move = truthful, true
		}
		read = append(read, r)
	}

	// The second half: every move weighed against every other one.
	var moves []Rename
	for _, r := range read {
		if r.move {
			moves = append(moves, Rename{From: r.name, To: r.to})
		}
	}
	granted, refused := grantMoves(moves, held)
	m.renames = granted
	truthful := make(map[string]Name, len(granted))
	for _, mv := range granted {
		truthful[mv.From.String()] = mv.To
	}

	for _, r := range read {
		name := r.name
		if r.move {
			if to, made := truthful[name.String()]; made {
				name = to
			} else {
				m.conflicts = append(m.conflicts, Conflict{r.w, refused[name.String()]})
			}
		}
		m.desks[name.Desk] = append(m.desks[name.Desk], name)
		// niri's output, or the name's monitor where niri said nothing and that
		// monitor is a screen, or nothing at all.
		m.at[name.String()] = r.at
		m.idx[name.String()] = r.w.Idx
	}

	// A desk's workspaces are kept by monitor, and inside a monitor by the
	// strip. The monitor comes first because what gets written out of this list
	// is written per monitor: a manifest declares a monitor's workspaces
	// together (internal/manifest, FromMap). The strip comes second because it
	// is the only order that is a fact rather than an opinion.
	//
	// It used to sort names inside the monitor, and a snapshot wrote that order
	// into a manifest: a strip reading zsh then agent came back as agent then
	// zsh. A manifest's first workspace on a monitor is where entering the desk
	// lands (internal/zded, landingSlots), so writing a desk down moved it.
	//
	// A switch does not read this order to choose. It walks the list only to
	// find which screens the desk is on, and picks out of Band (SwitchPlan).
	// But Band sorts by the same index, so the desk a snapshot writes down and
	// the desk a switch brings up have one idea of which end is the top.
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
	// The lists that are not a band, in an order of their own: they are printed
	// and diffed, so a line should move only when something moved. Renames are
	// not here - they come back from grantMoves in the order they can be
	// applied, which a chain depends on.
	sort.Slice(m.foreign, func(i, j int) bool {
		if m.foreign[i].Output != m.foreign[j].Output {
			return m.foreign[i].Output < m.foreign[j].Output
		}
		return m.foreign[i].Name < m.foreign[j].Name
	})
	sort.Slice(m.displaced, func(i, j int) bool { return less(m.displaced[i], m.displaced[j]) })
	sort.Slice(m.conflicts, func(i, j int) bool {
		return m.conflicts[i].Workspace.Name < m.conflicts[j].Workspace.Name
	})
	return m
}

// earlier is the order Rebuild reads niri's reply in. Any total order does the
// job, which is to take "which one is met first" away from niri; by name,
// because that is what the map is keyed on. The rest of the key only makes the
// order total, and two workspaces alike in all of it are interchangeable.
func earlier(a, b Workspace) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if a.Output != b.Output {
		return a.Output < b.Output
	}
	if a.Idx != b.Idx {
		return a.Idx < b.Idx
	}
	return a.ID < b.ID
}

// grantMoves decides which moves are made and in what order they can be
// applied. held counts the workspaces under each name now.
//
// A move gets the name it wants when nothing ends up called that: nothing is
// called it, or the one that is, is moving off it - a chain. Otherwise it is
// refused and keeps a name that lies about its monitor, which is the side to
// fail on: a wrong name can be put right, a wrong rename erases home.
//
//   - Two moves onto one name: it goes to the one that sorts first, so one
//     wrong name rather than two. Only the caller can mint a free slot.
//   - A move onto a name something stays under: refused, and it cascades,
//     because a refused move keeps its own name.
//   - A cycle: renames reach niri one at a time (internal/zded, reconcile), so
//     whichever went first would be handed a name in use. All of it refused.
//
// The rest are chains, returned in the order they can be applied.
func grantMoves(moves []Rename, held map[string]int) ([]Rename, map[string]string) {
	refused := map[string]string{}
	if len(moves) == 0 {
		return nil, refused
	}
	// In an order of its own, so which of two claimants wins is decided here.
	ordered := append([]Rename(nil), moves...)
	sort.Slice(ordered, func(i, j int) bool { return less(ordered[i].From, ordered[j].From) })

	// want is the moves still standing, keyed by the name carried now. Unique:
	// Rebuild does not move a workspace whose name is shared.
	want := make(map[string]Name, len(ordered))
	claimed := map[string]string{}
	for _, mv := range ordered {
		to := mv.To.String()
		if first, already := claimed[to]; already {
			refused[mv.From.String()] = "the truthful name " + to + " is wanted by " + first + " as well"
			continue
		}
		claimed[to] = mv.From.String()
		want[mv.From.String()] = mv.To
	}

	// Refusals only grow, so the answer does not depend on the order walked,
	// and each pass either refuses one or stops.
	for changed := true; changed; {
		changed = false
		for _, mv := range ordered {
			from := mv.From.String()
			to, standing := want[from]
			if !standing || held[to.String()] == 0 {
				continue
			}
			if _, vacating := want[to.String()]; vacating {
				continue
			}
			refused[from] = "the truthful name " + to.String() + " is taken"
			delete(want, from)
			changed = true
		}
	}

	// step is how many renames must be applied before this one can be. A move
	// that never gets one is waiting, round a cycle, on itself.
	step := map[string]int{}
	for changed := true; changed; {
		changed = false
		for _, mv := range ordered {
			from := mv.From.String()
			to, standing := want[from]
			if !standing {
				continue
			}
			if _, done := step[from]; done {
				continue
			}
			switch prior, ready := step[to.String()]; {
			case held[to.String()] == 0:
				step[from] = 0
			case ready:
				step[from] = prior + 1
			default:
				continue
			}
			changed = true
		}
	}

	granted := make([]Rename, 0, len(want))
	for _, mv := range ordered {
		from := mv.From.String()
		to, standing := want[from]
		if !standing {
			continue
		}
		if _, applicable := step[from]; !applicable {
			refused[from] = "the truthful name " + to.String() + " belongs to a workspace waiting for this one, " +
				"and a rename happens one at a time"
			continue
		}
		granted = append(granted, Rename{From: mv.From, To: to})
	}
	// Stable, so moves at the same step keep the order above.
	sort.SliceStable(granted, func(i, j int) bool {
		return step[granted[i].From.String()] < step[granted[j].From.String()]
	})
	return granted, refused
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

// Screens is the outputs niri was drawing on when this map was built, in a
// stable order. Empty is the caller not having said, not a machine with no
// screens.
func (m *Map) Screens() []string { return append([]string(nil), m.screens...) }

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
	if output == "" {
		// Not a screen. A workspace niri is showing nowhere reads as this
		// (Rebuild), and a band is what a scroll clamps to and a switch lands
		// in - neither can happen there.
		return nil
	}
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
// that order into a manifest, and the manifest's first workspace on a monitor
// is where entering the desk lands (internal/zded, landingSlots), so it has to
// be the strip's order and not a sort of its own.
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

// Displaced is the workspaces that are not on the monitor their name claims,
// because that monitor is gone - unplugged, or switched off, which is what a
// closing lid does to a laptop panel. Usually parked on a survivor; where niri
// reports no output at all, on no screen.
//
// Their names are not lies and must not be corrected. The name is zde's only
// record of where the workspace belongs: a manifest records home too, but only
// for the workspaces it declares, and an adopted one has the name and nothing
// else. What actually puts a workspace back is niri's, not zde's - niri keeps
// the output each workspace was opened on and returns it there when that output
// comes back (Layout::add_output, niri 26.04), which is why leaving the name
// alone is the whole of zde's job here. See docs/roadmap.md, 0.4, for the one
// case that record cannot reach.
func (m *Map) Displaced() []Name { return append([]Name(nil), m.displaced...) }
