package desk

import "testing"

func threeDesks() *Map {
	return Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "haven.DP-1.db", Output: "DP-1"},
		{Name: "misc.DP-1.notes", Output: "DP-1"},
		{Name: "regulars.DP-1.comms", Output: "DP-1"},
	}, []string{"DP-1"})
}

// The regulars are reachable from every desk by their own action and are in no
// desk's band, so rotating must not walk through them.
func TestRotationLeavesTheRegularsOut(t *testing.T) {
	if got := threeDesks().Rotation(); !equal(got, []string{"haven", "misc", "vshop"}) {
		t.Errorf("rotation = %v, want the desks without the regulars", got)
	}
}

func TestNextAndPrevWalkTheRotation(t *testing.T) {
	r := threeDesks().Rotation()
	if got := Next(r, "haven"); got != "misc" {
		t.Errorf("next from haven = %q, want misc", got)
	}
	if got := Prev(r, "misc"); got != "haven" {
		t.Errorf("prev from misc = %q, want haven", got)
	}
}

// Rotating is a loop, so the ends join rather than stop.
func TestRotationWraps(t *testing.T) {
	r := threeDesks().Rotation()
	if got := Next(r, "vshop"); got != "haven" {
		t.Errorf("next from the last desk = %q, want the first", got)
	}
	if got := Prev(r, "haven"); got != "vshop" {
		t.Errorf("prev from the first desk = %q, want the last", got)
	}
}

// From the regulars, or from a fresh workspace nothing has named yet, there is
// no place in the loop to step from - so the key still goes somewhere, and
// prev mirrors next rather than agreeing with it.
func TestFromOutsideTheRotation(t *testing.T) {
	r := threeDesks().Rotation()
	for _, from := range []string{"", Regulars, "gone"} {
		if got := Next(r, from); got != "haven" {
			t.Errorf("next from %q = %q, want the first desk", from, got)
		}
		if got := Prev(r, from); got != "vshop" {
			t.Errorf("prev from %q = %q, want the last desk", from, got)
		}
	}
}

// One desk is a loop of one, so a step comes back to where it started. What
// the caller does with that is the caller's - zded declines to switch, because
// switching to the desk you are on is not the no-op it looks like.
func TestRotationOfOne(t *testing.T) {
	m := Rebuild([]Workspace{{Name: "vshop.DP-1.code", Output: "DP-1"}}, []string{"DP-1"})
	r := m.Rotation()
	if got := Next(r, "vshop"); got != "vshop" {
		t.Errorf("next = %q, want the only desk", got)
	}
	if got := Prev(r, "vshop"); got != "vshop" {
		t.Errorf("prev = %q, want the only desk", got)
	}
}

// A step longer than the rotation still lands inside it. Nothing asks for one
// yet, but the arithmetic is the kind that is wrong quietly, and a page-step
// would be the first caller to find out.
func TestStepsLongerThanTheRotation(t *testing.T) {
	r := threeDesks() // haven, misc, vshop
	for _, c := range []struct {
		by   int
		from string
		want string
	}{
		{4, "haven", "misc"},
		{-4, "haven", "vshop"},
		{-7, "misc", "haven"},
		{9, "vshop", "vshop"},
	} {
		if got := step(r.Rotation(), c.from, c.by); got != c.want {
			t.Errorf("step %d from %s = %q, want %q", c.by, c.from, got, c.want)
		}
	}
}

// Nothing named yet at all: the rotation is empty and both ends say so rather
// than inventing a desk.
func TestEmptyRotation(t *testing.T) {
	m := Rebuild([]Workspace{{Name: "", Output: "DP-1"}}, []string{"DP-1"})
	if got := m.Rotation(); len(got) != 0 {
		t.Errorf("rotation = %v, want nothing to rotate through", got)
	}
	if got := Next(nil, "vshop"); got != "" {
		t.Errorf("next = %q, want nothing", got)
	}
	if got := Prev(nil, "vshop"); got != "" {
		t.Errorf("prev = %q, want nothing", got)
	}
}
