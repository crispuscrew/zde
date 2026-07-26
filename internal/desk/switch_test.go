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
		{Name: "vshop.eDP-1.notes", Output: "eDP-1"},
	}, []string{"eDP-1"})
	got := names(SwitchPlan(m, "vshop", nil))
	if !equal(got, []string{"vshop.DP-1.code", "vshop.eDP-1.notes"}) {
		t.Errorf("plan = %v, want the displaced workspace focused too", got)
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
