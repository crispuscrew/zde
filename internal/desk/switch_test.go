package desk

import "testing"

func twoMonitorDesk() *Map {
	return Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "vshop.DP-1.agent", Output: "DP-1"},
		{Name: "vshop.HDMI-A-1.aux", Output: "HDMI-A-1"},
		{Name: "haven.DP-1.db", Output: "DP-1"},
	}, []string{"DP-1", "HDMI-A-1"})
}

func names(plan []Name) []string {
	out := make([]string, 0, len(plan))
	for _, n := range plan {
		out = append(out, n.String())
	}
	return out
}

// Every monitor the desk owns something on gets exactly one workspace, because
// a desk switch moves all of them at once.
func TestSwitchPlanOnePerMonitor(t *testing.T) {
	got := names(SwitchPlan(twoMonitorDesk(), "vshop", nil))
	if !equal(got, []string{"vshop.DP-1.agent", "vshop.HDMI-A-1.aux"}) {
		t.Errorf("plan = %v, want the first of each monitor's band", got)
	}
}

// A window carried to another desk comes down on the screen it was already on,
// at the workspace that desk's band remembers there.
func TestLandingStaysOnTheScreen(t *testing.T) {
	m := twoMonitorDesk()
	got, ok := Landing(m, "vshop", "DP-1", map[string]string{"DP-1": "code"})
	if !ok || got.String() != "vshop.DP-1.code" {
		t.Errorf("landing = %v %v, want the remembered workspace on DP-1", got, ok)
	}
	// Nothing remembered there: the first of the band on that screen.
	got, ok = Landing(m, "vshop", "DP-1", nil)
	if !ok || got.String() != "vshop.DP-1.agent" {
		t.Errorf("landing = %v %v, want the first of the band on DP-1", got, ok)
	}
}

// A desk that owns nothing on this screen has nowhere here to put it, and says
// so rather than picking another monitor quietly.
func TestLandingOnADeskNotOnThisScreen(t *testing.T) {
	if got, ok := Landing(twoMonitorDesk(), "haven", "HDMI-A-1", nil); ok {
		t.Errorf("landing = %v, want none: haven owns nothing on HDMI-A-1", got)
	}
}

// The screen a workspace is parked on, not the monitor its name says. A desk
// whose every workspace belongs to a monitor that is gone is still on the one
// screen there is, and a window carried to it stays where the person is
// looking - it used to be sent to another screen on a one-screen machine.
func TestLandingOnADeskParkedHere(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "film.HDMI-A-1.player", Output: "DP-1"},
	}, []string{"DP-1"})
	got, ok := Landing(m, "film", "DP-1", nil)
	if !ok || got.String() != "film.HDMI-A-1.player" {
		t.Errorf("landing = %v %v, want the parked workspace on the screen it is on", got, ok)
	}
	// And nothing lands on the monitor it merely used to be on.
	if got, ok := Landing(m, "film", "HDMI-A-1", nil); ok {
		t.Errorf("landing = %v, want none: nothing is on HDMI-A-1, it is not a screen", got)
	}
}

// Invariant 5: coming back to a desk puts each monitor where you left it, not
// at the top of the strip.
func TestSwitchPlanRestoresLastActive(t *testing.T) {
	got := names(SwitchPlan(twoMonitorDesk(), "vshop", map[string]string{"DP-1": "code"}))
	if !equal(got, []string{"vshop.DP-1.code", "vshop.HDMI-A-1.aux"}) {
		t.Errorf("plan = %v, want the remembered workspace on DP-1", got)
	}
}

// A remembered workspace that is gone - closed, or moved to another desk - is
// not a reason to fail or to land nowhere.
func TestSwitchPlanIgnoresStaleMemory(t *testing.T) {
	got := names(SwitchPlan(twoMonitorDesk(), "vshop", map[string]string{"DP-1": "vanished"}))
	if !equal(got, []string{"vshop.DP-1.agent", "vshop.HDMI-A-1.aux"}) {
		t.Errorf("plan = %v, want the band's first where memory is stale", got)
	}
}

// A desk with nothing in it is not an error: it is a manifest that has not
// been launched yet, and the caller decides what to do about it.
func TestSwitchPlanEmptyDesk(t *testing.T) {
	if got := SwitchPlan(twoMonitorDesk(), "never-seen", nil); got != nil {
		t.Errorf("plan = %v, want nothing to focus", got)
	}
}

// A workspace whose monitor is unplugged is sitting on a survivor. Focusing it
// by name brings it up wherever niri has put it, and its name still says where
// it belongs - so it must be in the plan, not skipped for being homeless.
func TestSwitchPlanIncludesDisplaced(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "eDP-1"}, // DP-1 is gone
	}, []string{"eDP-1"})
	got := names(SwitchPlan(m, "vshop", nil))
	if !equal(got, []string{"vshop.DP-1.code"}) {
		t.Errorf("plan = %v, want the displaced workspace focused too", got)
	}
}

// One focus per screen, not per monitor named in a name. Two of a desk's
// workspaces on one screen - which is what an unplug leaves behind - used to be
// two focus calls, and the second undid the first: the screen ended on whatever
// the plan listed last, every time.
func TestSwitchPlanFocusesEachScreenOnce(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "eDP-1", Idx: 0}, // DP-1 is gone
		{Name: "vshop.eDP-1.notes", Output: "eDP-1", Idx: 1},
	}, []string{"eDP-1"})
	got := names(SwitchPlan(m, "vshop", nil))
	if !equal(got, []string{"vshop.DP-1.code"}) {
		t.Errorf("plan = %v, want one workspace for the one screen", got)
	}
}

// The screen's own workspace wins over one parked on it, when both are the
// remembered one for their own monitor. The remembered slot is a fact about a
// desk and a monitor, not about a screen, so two of them can point at one
// screen at once.
func TestSwitchPlanPrefersTheScreensOwn(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "eDP-1", Idx: 0}, // DP-1 is gone
		{Name: "vshop.eDP-1.notes", Output: "eDP-1", Idx: 1},
	}, []string{"eDP-1"})
	got := names(SwitchPlan(m, "vshop", map[string]string{"DP-1": "code", "eDP-1": "notes"}))
	if !equal(got, []string{"vshop.eDP-1.notes"}) {
		t.Errorf("plan = %v, want the screen's own remembered workspace", got)
	}
}

// A desk parked entirely on one screen still restores what it remembers there,
// even though the slot is remembered under a monitor that is not the screen.
func TestSwitchPlanRestoresAParkedWorkspace(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "eDP-1", Idx: 0},
		{Name: "vshop.DP-1.agent", Output: "eDP-1", Idx: 1},
	}, []string{"eDP-1"})
	got := names(SwitchPlan(m, "vshop", map[string]string{"DP-1": "agent"}))
	if !equal(got, []string{"vshop.DP-1.agent"}) {
		t.Errorf("plan = %v, want the remembered workspace even though it is parked", got)
	}
}

// The band's order is niri's strip order, so the switch lands where the strip
// starts and a scroll from there walks the rest. Names sort the other way here
// on purpose.
func TestSwitchPlanLandsOnTheTopOfTheStrip(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.zsh", Output: "DP-1", Idx: 0},
		{Name: "vshop.DP-1.agent", Output: "DP-1", Idx: 1},
	}, []string{"DP-1"})
	if got := names(SwitchPlan(m, "vshop", nil)); !equal(got, []string{"vshop.DP-1.zsh"}) {
		t.Errorf("plan = %v, want the top of the strip", got)
	}
}

// The regulars band is a desk key like any other, so it can be switched to -
// what it is not is part of another desk's band.
func TestSwitchPlanRegulars(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "regulars.DP-1.comms", Output: "DP-1"},
	}, []string{"DP-1"})
	if got := names(SwitchPlan(m, Regulars, nil)); !equal(got, []string{"regulars.DP-1.comms"}) {
		t.Errorf("plan = %v, want the regulars workspace", got)
	}
	if got := names(SwitchPlan(m, "vshop", nil)); !equal(got, []string{"vshop.DP-1.code"}) {
		t.Errorf("plan = %v, want regulars left out of vshop's switch", got)
	}
}

// The plan does not depend on the order niri listed workspaces in.
func TestSwitchPlanIsStable(t *testing.T) {
	first := names(SwitchPlan(twoMonitorDesk(), "vshop", nil))
	for i := 0; i < 5; i++ {
		if got := names(SwitchPlan(twoMonitorDesk(), "vshop", nil)); !equal(got, first) {
			t.Fatalf("plan changed between calls: %v then %v", first, got)
		}
	}
}
