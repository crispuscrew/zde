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
