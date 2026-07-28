package desk

// SwitchPlan is the workspaces to focus to bring a desk up: one per monitor
// the desk owns workspaces on, in a stable order.
//
// A desk switch moves every monitor at once (docs/model.md, section 2), and
// focusing a workspace is what makes the monitor it is on show it - so the
// plan is simply every monitor's chosen workspace, and niri does the rest.
//
// lastActive is monitor -> slot, out of the journal. Where it names a
// workspace the desk still owns, that is the one: invariant 5 restores each
// monitor's last-active workspace rather than dropping you at the top of the
// strip. Where it does not, the first in the band is the sane place to land.
//
// Nothing here consults which monitors are connected. A workspace whose home
// monitor is unplugged is sitting on a survivor, and focusing it by name is
// exactly right: it comes up wherever niri has put it, and its name still
// records where it belongs for when the monitor comes back.
func SwitchPlan(m *Map, target string, lastActive map[string]string) []Name {
	owned := m.Workspaces(target)
	if len(owned) == 0 {
		return nil
	}
	// Workspaces are already sorted by monitor then slot, so the first of each
	// monitor's run is the band's first.
	var plan []Name
	seen := map[string]bool{}
	for _, n := range owned {
		if seen[n.Monitor] {
			continue
		}
		seen[n.Monitor] = true
		plan = append(plan, pick(owned, n.Monitor, lastActive[n.Monitor]))
	}
	return plan
}

// Landing is where a window carried to another desk should come down: the
// workspace that desk's band remembers on this monitor, or the first of the
// band there. A window changes desks without changing screens, because the
// screen it is on is where the person looking at it is looking.
//
// False when the desk owns nothing on this monitor. Putting the window on
// another screen is a decision, not a fallback this can make quietly, so it
// says so and lets the caller make it.
func Landing(m *Map, target, monitor, slot string) (Name, bool) {
	owned := m.Workspaces(target)
	for _, n := range owned {
		if n.Monitor == monitor {
			return pick(owned, monitor, slot), true
		}
	}
	return Name{}, false
}

// pick is the remembered workspace on this monitor if the desk still owns it,
// and the first of the band otherwise.
func pick(owned []Name, monitor, slot string) Name {
	var first *Name
	for i, n := range owned {
		if n.Monitor != monitor {
			continue
		}
		if first == nil {
			first = &owned[i]
		}
		if slot != "" && n.Slot == slot {
			return n
		}
	}
	return *first
}
