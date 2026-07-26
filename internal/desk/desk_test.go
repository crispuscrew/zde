package desk

import (
	"strings"
	"testing"
)

func TestParseName(t *testing.T) {
	ok := []struct {
		in   string
		want Name
	}{
		{"vshop.DP-1.code", Name{"vshop", "DP-1", "code"}},
		{"vshop.HDMI-A-1.1", Name{"vshop", "HDMI-A-1", "1"}},
		{"regulars.eDP-1.2", Name{Regulars, "eDP-1", "2"}},
		{"film-2.DP-2.ambient", Name{"film-2", "DP-2", "ambient"}},
	}
	for _, c := range ok {
		got, err := ParseName(c.in)
		if err != nil {
			t.Errorf("ParseName(%q) = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseName(%q) = %+v, want %+v", c.in, got, c.want)
		}
		if got.String() != c.in {
			t.Errorf("round trip: %q -> %q", c.in, got.String())
		}
	}
}

// A name that does not parse is not an error to report at the user, it is a
// workspace that is not ours - so what matters is that the boundary is drawn
// in the right place.
func TestParseNameRejects(t *testing.T) {
	for _, in := range []string{
		"",                  // niri's unnamed workspaces
		"code",              // a user's own name
		"vshop.DP-1",        // no slot
		"vshop.DP-1.code.2", // one dot too many
		"VSHOP.DP-1.code",   // desks are lowercase
		"vshop..code",       // empty monitor
		"vshop.DP-1.",       // empty slot
		"vshop.DP-1.Code",   // slots are lowercase
		"-vshop.DP-1.code",  // leading dash
		"vshop.1DP.code",    // a connector starts with a letter
	} {
		if _, err := ParseName(in); err == nil {
			t.Errorf("ParseName(%q) was accepted", in)
		}
	}
}

func TestRebuild(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "vshop.DP-1.agent", Output: "DP-1"},
		{Name: "vshop.HDMI-A-1.aux", Output: "HDMI-A-1"},
		{Name: "haven.DP-1.db", Output: "DP-1"},
		{Name: "regulars.DP-1.1", Output: "DP-1"},
		{Name: "", Output: "DP-1"},        // niri's own empty workspace
		{Name: "scratch", Output: "DP-1"}, // somebody else's
	})

	if got, want := m.Names(), []string{"haven", Regulars, "vshop"}; !equal(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	if got := len(m.Desks["vshop"]); got != 3 {
		t.Errorf("vshop has %d workspaces, want 3", got)
	}
	if got := len(m.Foreign); got != 2 {
		t.Errorf("Foreign = %v, want the unnamed and the foreign one", m.Foreign)
	}
	if len(m.Renames) != 0 {
		t.Errorf("Renames = %v, want none: every name agrees with niri", m.Renames)
	}
	// Sorted by monitor then slot, so the map is stable to compare and to show.
	if got := m.Desks["vshop"][0].Slot; got != "agent" {
		t.Errorf("vshop[0] = %q, want the DP-1 workspaces first and sorted", got)
	}
}

// The case the name has to survive: a workspace dragged to another monitor.
// niri knows where it is; the name still claims the old monitor; invariant 1
// says the name gets corrected, not the other way round.
func TestRebuildRenamesMovedWorkspace(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "HDMI-A-1"},
	})
	if len(m.Renames) != 1 {
		t.Fatalf("Renames = %v, want the moved workspace", m.Renames)
	}
	r := m.Renames[0]
	if r.From.String() != "vshop.DP-1.code" || r.To.String() != "vshop.HDMI-A-1.code" {
		t.Errorf("rename %s -> %s, want vshop.DP-1.code -> vshop.HDMI-A-1.code", r.From, r.To)
	}
	// The map holds the corrected name, not the stale one: everything
	// downstream reads the map, and it must not see a lie.
	if got := m.Desks["vshop"][0].Monitor; got != "HDMI-A-1" {
		t.Errorf("map kept monitor %q, want the one niri reports", got)
	}
	// Ownership does not move with the monitor. That is the whole point of
	// putting the desk in the name.
	if got := m.Desks["vshop"][0].Desk; got != "vshop" {
		t.Errorf("desk became %q, want vshop", got)
	}
}

// Rebuild is the recovery path, so it must not depend on anything zded
// remembers: the same input has to give the same map every time.
func TestRebuildIsPure(t *testing.T) {
	in := []Workspace{
		{Name: "haven.DP-1.db", Output: "DP-1"},
		{Name: "vshop.DP-1.code", Output: "HDMI-A-1"},
		{Name: "nope", Output: "DP-1"},
	}
	first := Rebuild(in)
	for i := 0; i < 5; i++ {
		again := Rebuild(in)
		if !equal(first.Names(), again.Names()) {
			t.Fatalf("Rebuild is not stable: %v then %v", first.Names(), again.Names())
		}
		if len(again.Renames) != len(first.Renames) || len(again.Foreign) != len(first.Foreign) {
			t.Fatal("Rebuild is not stable in renames or foreign workspaces")
		}
	}
}

// Regulars are reachable from every desk, which means they are in no desk's
// band: scrolling must not cross into them (invariant 4).
func TestBandExcludesRegulars(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "regulars.DP-1.1", Output: "DP-1"},
	})
	band := m.Band("vshop")
	if len(band) != 1 || band[0].Slot != "code" {
		t.Errorf("Band(vshop) = %v, want only vshop's own workspaces", band)
	}
	for _, n := range band {
		if n.IsRegulars() {
			t.Error("the regulars band is inside a desk's band")
		}
	}
}

// An unknown desk is empty, not an error: it is what a desk switch to a
// manifest with nothing running yet looks like.
func TestBandUnknownDesk(t *testing.T) {
	m := Rebuild(nil)
	if got := m.Band("never-seen"); len(got) != 0 {
		t.Errorf("Band of an unknown desk = %v, want empty", got)
	}
}

func TestNameErrorsSayWhy(t *testing.T) {
	_, err := ParseName("VSHOP.DP-1.code")
	if err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Errorf("got %v, want an error naming the rule", err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
