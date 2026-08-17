package desk

// SwitchPlan is the workspaces to focus to bring a desk up: one per screen the
// desk has workspaces on, in a stable order.
//
// A desk switch moves every monitor at once (docs/model.md, section 2), and
// focusing a workspace is what makes the screen it is on show it - so the plan
// is simply every screen's chosen workspace, and niri does the rest.
//
// Per screen, and not per monitor named in a workspace's name. Those are the
// same thing only while every monitor is there. When one is gone its workspaces
// are parked on a survivor, and grouping by the name would put two of them in
// the plan for one screen: both get focused, the second wins, and the workspace
// the switch just restored is scrolled off. Deterministically, on every switch.
//
// lastActive is monitor -> slot, out of the journal. Where it names a workspace
// the desk still owns on this screen, that is the one: invariant 5 restores each
// monitor's last-active workspace rather than dropping you at the top of the
// strip. Where it does not, the first of the band on that screen is the sane
// place to land.
func SwitchPlan(m *Map, target string, lastActive map[string]string) []Name {
	var plan []Name
	seen := map[string]bool{}
	for _, n := range m.Workspaces(target) {
		screen := m.at[n.String()]
		if seen[screen] {
			continue
		}
		seen[screen] = true
		if landing, ok := pick(m, target, screen, lastActive); ok {
			plan = append(plan, landing)
		}
	}
	return plan
}

// Landing is where a window carried to another desk should come down: the
// workspace that desk's band remembers on this screen, or the first of the band
// there. A window changes desks without changing screens, because the screen it
// is on is where the person looking at it is looking.
//
// False when the desk has nothing on this screen. Putting the window on another
// screen is a decision, not a fallback this can make quietly, so it says so and
// lets the caller make it. On this screen, again: a desk whose every workspace
// is parked on the one screen left answers yes, where asking about the monitors
// in the names answered no and sent the window to another screen on a machine
// that has one.
func Landing(m *Map, target, screen string, lastActive map[string]string) (Name, bool) {
	return pick(m, target, screen, lastActive)
}

// pick is the workspace a desk comes up on for one screen: the remembered one
// if it is still here, and the first of the band otherwise. False when the desk
// has nothing on that screen at all.
//
// The band is the desk's workspaces on that screen in niri's own strip order
// (Band), so "the first" is the top of the strip and a scroll from there walks
// the rest.
//
// lastActive is keyed by the monitor in the name and not by the screen, and it
// stays that way: it is what a desk remembers across sessions, and a screen a
// workspace was parked on is a fact about one unplug, while the monitor in its
// name is what the workspace belongs to. So a remembered slot is matched
// against each candidate's own monitor rather than looked up by screen. A
// workspace that is home here wins over one parked here, because the screen's
// own band is what the screen is for.
func pick(m *Map, target, screen string, lastActive map[string]string) (Name, bool) {
	band := m.Band(target, screen)
	if len(band) == 0 {
		return Name{}, false
	}
	var parked *Name
	for i, n := range band {
		if slot, remembered := lastActive[n.Monitor]; !remembered || slot != n.Slot {
			continue
		}
		if n.Monitor == screen {
			return n, true
		}
		if parked == nil {
			parked = &band[i]
		}
	}
	if parked != nil {
		return *parked, true
	}
	return band[0], true
}
