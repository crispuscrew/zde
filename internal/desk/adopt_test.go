package desk

import "testing"

// Invariant 3: what you make while a desk is active belongs to that desk.
func TestAdoptPlanNamesForeignIntoActiveDesk(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
		{ID: 2, Name: "", Output: "DP-1"}, // niri made this one
	}, []string{"DP-1"})

	plan := AdoptPlan(m, "vshop")
	if len(plan) != 1 {
		t.Fatalf("plan = %v, want the unnamed workspace claimed", plan)
	}
	if plan[0].ID != 2 {
		t.Errorf("adopting id %d, want the unnamed one", plan[0].ID)
	}
	if got := plan[0].Name.String(); got != "vshop.DP-1.1" {
		t.Errorf("name = %q, want the first free ordinal on its monitor", got)
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
	for _, a := range AdoptPlan(m, "vshop") {
		got = append(got, a.Name.String())
	}
	want := []string{"vshop.DP-1.3", "vshop.DP-1.4", "vshop.HDMI-A-1.1"}
	if !equal(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

// A workspace somebody else named is claimed the same way: the name is what
// ownership is, so taking ownership means renaming it.
func TestAdoptPlanClaimsForeignNames(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "scratch", Output: "DP-1"},
	}, []string{"DP-1"})
	plan := AdoptPlan(m, "vshop")
	if len(plan) != 1 || plan[0].Name.String() != "vshop.DP-1.1" {
		t.Errorf("plan = %v, want the foreign name claimed", plan)
	}
}

// A workspace on no output has no monitor to be named onto.
func TestAdoptPlanSkipsWorkspacesOnNoOutput(t *testing.T) {
	m := Rebuild([]Workspace{{ID: 1, Name: "", Output: ""}}, []string{"DP-1"})
	if got := AdoptPlan(m, "vshop"); got != nil {
		t.Errorf("plan = %v, want nothing claimed onto no monitor", got)
	}
}

// With no active desk there is no band to adopt into, and guessing would put
// windows on a desk the user never chose.
func TestAdoptPlanWithoutActiveDesk(t *testing.T) {
	m := Rebuild([]Workspace{{ID: 1, Name: "", Output: "DP-1"}}, []string{"DP-1"})
	if got := AdoptPlan(m, ""); got != nil {
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
	plan := AdoptPlan(Rebuild(in, []string{"DP-1"}), "vshop")
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
	if got := AdoptPlan(m, "vshop"); got != nil {
		t.Errorf("a second pass wanted to adopt again: %v", got)
	}
}
