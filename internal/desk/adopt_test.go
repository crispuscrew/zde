package desk

import "testing"

// Invariant 3: what you make while a desk is active belongs to that desk.
func TestAdoptPlanNamesForeignIntoActiveDesk(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
		{ID: 2, Name: "", Output: "DP-1"}, // niri made this one
	}, []string{"DP-1"})

	plan := AdoptPlan(m, "vshop", map[uint64]string{2: "org.mozilla.firefox"})
	if len(plan) != 1 {
		t.Fatalf("plan = %v, want the unnamed workspace claimed", plan)
	}
	if plan[0].ID != 2 {
		t.Errorf("adopting id %d, want the unnamed one", plan[0].ID)
	}
	// Named after what is in it, so the bar says something.
	if got := plan[0].Name.String(); got != "vshop.DP-1.firefox" {
		t.Errorf("name = %q, want the workspace named after its first app", got)
	}
}

// Ordinals continue past what the desk already holds, on the right monitor,
// and never collide with a label a manifest declared.
func TestAdoptPlanPicksFreeOrdinals(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.1", Output: "DP-1"},
		{ID: 2, Name: "vshop.DP-1.2", Output: "DP-1"},
		{ID: 3, Name: "vshop.HDMI-A-1.code", Output: "HDMI-A-1"},
		{ID: 4, Name: "", Output: "DP-1"},
		{ID: 5, Name: "", Output: "DP-1"},
		{ID: 6, Name: "", Output: "HDMI-A-1"},
	}, []string{"DP-1", "HDMI-A-1"})

	var got []string
	for _, a := range AdoptPlan(m, "vshop", map[uint64]string{4: "", 5: "", 6: ""}) {
		got = append(got, a.Name.String())
	}
	want := []string{"vshop.DP-1.3", "vshop.DP-1.4", "vshop.HDMI-A-1.1"}
	if !equal(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

// Two workspaces running the same app on one monitor cannot share a name.
func TestAdoptPlanMakesLabelsUnique(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.firefox", Output: "DP-1"},
		{ID: 2, Name: "", Output: "DP-1"},
		{ID: 3, Name: "", Output: "DP-1"},
	}, []string{"DP-1"})
	var got []string
	for _, a := range AdoptPlan(m, "vshop", map[uint64]string{2: "firefox", 3: "firefox"}) {
		got = append(got, a.Name.String())
	}
	if !equal(got, []string{"vshop.DP-1.firefox-2", "vshop.DP-1.firefox-3"}) {
		t.Errorf("plan = %v, want the label made unique", got)
	}
}

// niri keeps one empty workspace at the end of every strip. Claiming it would
// name the scratch space, niri would make another, and the next pass would
// claim that one: a ratchet that fills the strip with named nothing.
func TestAdoptPlanLeavesEmptyWorkspacesAlone(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
		{ID: 2, Name: "", Output: "DP-1"}, // niri's trailing empty one
	}, []string{"DP-1"})
	if got := AdoptPlan(m, "vshop", map[uint64]string{}); got != nil {
		t.Errorf("plan = %v, want the empty workspace left alone", got)
	}
}

// A workspace somebody else named is claimed the same way: the name is what
// ownership is, so taking ownership means renaming it.
func TestAdoptPlanClaimsForeignNames(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "scratch", Output: "DP-1"},
	}, []string{"DP-1"})
	plan := AdoptPlan(m, "vshop", map[uint64]string{1: "Alacritty"})
	if len(plan) != 1 || plan[0].Name.String() != "vshop.DP-1.alacritty" {
		t.Errorf("plan = %v, want the foreign name claimed", plan)
	}
}

// A workspace on no output has no monitor to be named onto.
func TestAdoptPlanSkipsWorkspacesOnNoOutput(t *testing.T) {
	m := Rebuild([]Workspace{{ID: 1, Name: "", Output: ""}}, []string{"DP-1"})
	if got := AdoptPlan(m, "vshop", map[uint64]string{1: "firefox"}); got != nil {
		t.Errorf("plan = %v, want nothing claimed onto no monitor", got)
	}
}

// With no active desk there is no band to adopt into, and guessing would put
// windows on a desk the user never chose.
func TestAdoptPlanWithoutActiveDesk(t *testing.T) {
	m := Rebuild([]Workspace{{ID: 1, Name: "", Output: "DP-1"}}, []string{"DP-1"})
	if got := AdoptPlan(m, "", map[uint64]string{1: "firefox"}); got != nil {
		t.Errorf("plan = %v, want nothing adopted with no active desk", got)
	}
}

// Applying the plan leaves nothing foreign: adoption converges.
func TestAdoptPlanConverges(t *testing.T) {
	in := []Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
		{ID: 2, Name: "", Output: "DP-1"},
		{ID: 3, Name: "", Output: "DP-1"},
	}
	apps := map[uint64]string{2: "firefox", 3: "ghostty"}
	plan := AdoptPlan(Rebuild(in, []string{"DP-1"}), "vshop", apps)
	for _, a := range plan {
		for i := range in {
			if in[i].ID == a.ID {
				in[i].Name = a.Name.String()
			}
		}
	}
	m := Rebuild(in, []string{"DP-1"})
	if got := m.Foreign(); len(got) != 0 {
		t.Errorf("still foreign after adoption: %v", got)
	}
	if got := AdoptPlan(m, "vshop", apps); got != nil {
		t.Errorf("a second pass wanted to adopt again: %v", got)
	}
}

// A lid shut: the manifest declares workspaces on eDP-1 and niri is not drawing
// on it, so no pass can make them. A different answer from a screen with no
// empty workspace spare, which the next pass fixes.
func TestMissingPlanSaysWhatItCannotMake(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
	}, []string{"DP-1"}) // eDP-1 is a connector, not a screen

	declared := []Name{
		{Desk: "vshop", Monitor: "DP-1", Slot: "code"},
		{Desk: "vshop", Monitor: "eDP-1", Slot: "mail"},
		{Desk: "vshop", Monitor: "eDP-1", Slot: "chat"},
	}
	plan, waiting := MissingPlan(m, declared, map[string][]uint64{"DP-1": {9}})
	if len(plan) != 0 {
		t.Errorf("plan = %v, want nothing: the only declared workspace on a screen is already there", plan)
	}
	if len(waiting) != 2 {
		t.Fatalf("waiting = %v, want the two workspaces on the monitor that is not a screen", waiting)
	}
	for _, n := range waiting {
		if n.Monitor != "eDP-1" {
			t.Errorf("waiting on %s, and that monitor is a screen", n.Monitor)
		}
	}
}

// With the monitor back the same manifest comes up whole, which is why the ones
// above are worth saying rather than failing on.
func TestMissingPlanMakesThemAllWhenTheMonitorIsBack(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
	}, []string{"DP-1", "eDP-1"})

	declared := []Name{
		{Desk: "vshop", Monitor: "DP-1", Slot: "code"},
		{Desk: "vshop", Monitor: "eDP-1", Slot: "mail"},
	}
	plan, waiting := MissingPlan(m, declared, map[string][]uint64{"eDP-1": {9}})
	if len(waiting) != 0 {
		t.Errorf("waiting = %v, want none: both monitors are screens", waiting)
	}
	if len(plan) != 1 || plan[0].Name.String() != "vshop.eDP-1.mail" {
		t.Errorf("plan = %v, want the declared workspace named onto the empty one", plan)
	}
}

// A screen with no empty workspace spare is what the caller's loop goes round
// again for, so saying "not made" would be a message about the imminent.
func TestMissingPlanIsQuietAboutAScreenWithNoEmptyWorkspace(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
	}, []string{"DP-1", "eDP-1"})

	declared := []Name{{Desk: "vshop", Monitor: "eDP-1", Slot: "mail"}}
	plan, waiting := MissingPlan(m, declared, nil) // niri has produced none yet
	if len(plan) != 0 || len(waiting) != 0 {
		t.Errorf("plan = %v, waiting = %v, want both empty: eDP-1 is a screen with nothing spare", plan, waiting)
	}
}

// Without a screen list nothing knows which monitors are screens, and calling
// them all parked would report a desk as unmade when niri was never asked.
func TestMissingPlanSaysNothingWithNoScreenList(t *testing.T) {
	m := Rebuild([]Workspace{{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"}}, nil)
	declared := []Name{{Desk: "vshop", Monitor: "eDP-1", Slot: "mail"}}
	if _, waiting := MissingPlan(m, declared, nil); len(waiting) != 0 {
		t.Errorf("waiting = %v, want none: which monitors are screens is not known here", waiting)
	}
}
