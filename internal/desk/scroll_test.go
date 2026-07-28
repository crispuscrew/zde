package desk

import "testing"

func band(t *testing.T, slots ...string) []Name {
	t.Helper()
	out := make([]Name, 0, len(slots))
	for _, s := range slots {
		n, err := NewName("vshop", "DP-1", s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// The band is walked in the order the screen has it, which is not the order
// names sort in. A workspace adopted later sits at the end of the strip
// whatever it ended up called, and a scroll that read the map's own order
// would step over its neighbour and then back to it.
func TestBandFollowsTheStrip(t *testing.T) {
	m := Rebuild([]Workspace{
		{Idx: 1, Name: "vshop.DP-1.code", Output: "DP-1"},
		{Idx: 2, Name: "vshop.DP-1.notes", Output: "DP-1"},
		{Idx: 3, Name: "vshop.DP-1.foot", Output: "DP-1"}, // adopted last, sits last
	}, []string{"DP-1"})
	got := names(m.Band("vshop", "DP-1"))
	if !equal(got, []string{"vshop.DP-1.code", "vshop.DP-1.notes", "vshop.DP-1.foot"}) {
		t.Errorf("band = %v, want the strip's order", got)
	}
}

// A monitor unplugged leaves its workspaces parked on a survivor, with home
// still in their names so they can go back. The band is where they are now, or
// scrolling would find nothing at all on the only screen there is.
func TestBandIsWhereTheWorkspacesAre(t *testing.T) {
	m := Rebuild([]Workspace{
		{Idx: 1, Name: "vshop.DP-1.code", Output: "eDP-1"},
		{Idx: 2, Name: "vshop.DP-1.notes", Output: "eDP-1"},
	}, []string{"eDP-1"}) // DP-1 is gone
	if got := names(m.Band("vshop", "eDP-1")); !equal(got, []string{"vshop.DP-1.code", "vshop.DP-1.notes"}) {
		t.Errorf("band on the survivor = %v, want both workspaces", got)
	}
	if got := m.Band("vshop", "DP-1"); len(got) != 0 {
		t.Errorf("band on the unplugged monitor = %v, want nothing: they are not there", got)
	}
}

func TestBandStepWalksTheBand(t *testing.T) {
	b := band(t, "code", "notes", "logs")
	got, moved := BandStep(b, b[0], 1)
	if !moved || got != b[1] {
		t.Errorf("next = %v %v, want the one after it", got, moved)
	}
	got, moved = BandStep(b, b[2], -1)
	if !moved || got != b[1] {
		t.Errorf("prev = %v %v, want the one before it", got, moved)
	}
}

// Invariant 4: the band is the whole range a scroll reaches. At its ends
// nothing happens, rather than the next desk's workspaces arriving.
func TestBandStepStopsAtTheEnds(t *testing.T) {
	b := band(t, "code", "notes")
	if got, moved := BandStep(b, b[1], 1); moved {
		t.Errorf("next past the end = %v, want nothing", got)
	}
	if got, moved := BandStep(b, b[0], -1); moved {
		t.Errorf("prev before the start = %v, want nothing", got)
	}
}

// A workspace nothing has named is in no band, so there is no position to
// count from. The near end is the way back in.
func TestBandStepFromOutsideTheBand(t *testing.T) {
	b := band(t, "code", "notes", "logs")
	for _, from := range []Name{{}, {Desk: "haven", Monitor: "DP-1", Slot: "db"}} {
		got, moved := BandStep(b, from, 1)
		if !moved || got != b[0] {
			t.Errorf("next from %v = %v %v, want the first of the band", from, got, moved)
		}
		got, moved = BandStep(b, from, -1)
		if !moved || got != b[len(b)-1] {
			t.Errorf("prev from %v = %v %v, want the last of the band", from, got, moved)
		}
	}
}

// A band of one is both ends at once, and a band of none is nowhere to go.
func TestBandStepDegenerateBands(t *testing.T) {
	one := band(t, "code")
	if got, moved := BandStep(one, one[0], 1); moved {
		t.Errorf("next in a band of one = %v, want nothing", got)
	}
	if got, moved := BandStep(nil, one[0], 1); moved {
		t.Errorf("next in an empty band = %v, want nothing", got)
	}
}
