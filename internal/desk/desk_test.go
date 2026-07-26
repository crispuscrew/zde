package desk

import (
	"strings"
	"testing"
)

// both monitors, for the ordinary case where nothing is unplugged.
var both = []string{"DP-1", "HDMI-A-1"}

func TestParseName(t *testing.T) {
	ok := []struct {
		in   string
		want Name
	}{
		{"vshop.DP-1.code", Name{"vshop", "DP-1", "code"}},
		{"vshop.HDMI-A-1.1", Name{"vshop", "HDMI-A-1", "1"}},
		{"regulars.eDP-1.2", Name{Regulars, "eDP-1", "2"}},
		{"film-2.DP-2.ambient", Name{"film-2", "DP-2", "ambient"}},
		{"vshop.DP-2-1.code", Name{"vshop", "DP-2-1", "code"}}, // a real niri connector
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
		"vshop.DP 1.code",   // no spaces
	} {
		if _, err := ParseName(in); err == nil {
			t.Errorf("ParseName(%q) was accepted", in)
		}
	}
}

// NewName is the only way to mint a name that is checked. Assembling a Name
// literal is possible and is how an unparseable name gets into the system.
func TestNewName(t *testing.T) {
	n, err := NewName("vshop", "DP-1", "code")
	if err != nil || n.String() != "vshop.DP-1.code" {
		t.Errorf("NewName = %v, %v", n, err)
	}
	if _, err := NewName("VSHOP", "DP-1", "code"); err == nil {
		t.Error("NewName accepted an uppercase desk")
	}
	if _, err := NewName("vshop", "DP-1", "co.de"); err == nil {
		t.Error("NewName accepted a slot containing the separator")
	}
}

func TestOrdinal(t *testing.T) {
	n, _ := ParseName("vshop.DP-1.10")
	if got, ok := n.Ordinal(); !ok || got != 10 {
		t.Errorf("Ordinal() = %d, %v, want 10, true", got, ok)
	}
	l, _ := ParseName("vshop.DP-1.code")
	if _, ok := l.Ordinal(); ok {
		t.Error("a label reported itself as an ordinal")
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
	}, both)

	if got, want := m.Names(), []string{"haven", Regulars, "vshop"}; !equal(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	if got := len(m.Desks["vshop"]); got != 3 {
		t.Errorf("vshop has %d workspaces, want 3", got)
	}
	if got := len(m.Foreign); got != 2 {
		t.Errorf("Foreign = %v, want the unnamed and the foreign one", m.Foreign)
	}
	if len(m.Renames)+len(m.Conflicts)+len(m.Displaced) != 0 {
		t.Error("a map where every name agrees with niri reported work to do")
	}
}

// Ordinals are numbers, so 2 sorts before 10. Lexical order would have put the
// strip in an order no human reads.
func TestBandOrder(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.10", Output: "DP-1"},
		{Name: "vshop.DP-1.2", Output: "DP-1"},
		{Name: "vshop.DP-1.code", Output: "DP-1"},
	}, both)
	var got []string
	for _, n := range m.Band("vshop", "DP-1") {
		got = append(got, n.Slot)
	}
	if !equal(got, []string{"code", "2", "10"}) {
		t.Errorf("Band order = %v, want labels then ordinals by number", got)
	}
}

// The case the name has to survive: a workspace dragged to another monitor
// while both monitors are present. niri knows where it is; the name still
// claims the old monitor; invariant 1 says the name gets corrected.
func TestRebuildRenamesMovedWorkspace(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "HDMI-A-1"},
	}, both)
	if len(m.Renames) != 1 {
		t.Fatalf("Renames = %v, want the moved workspace", m.Renames)
	}
	r := m.Renames[0]
	if r.From.String() != "vshop.DP-1.code" || r.To.String() != "vshop.HDMI-A-1.code" {
		t.Errorf("rename %s -> %s, want vshop.DP-1.code -> vshop.HDMI-A-1.code", r.From, r.To)
	}
	// The map holds the corrected name, not the stale one.
	if got := m.Desks["vshop"][0].Monitor; got != "HDMI-A-1" {
		t.Errorf("map kept monitor %q, want the one niri reports", got)
	}
	// Ownership does not move with the monitor. That is the whole point of
	// putting the desk in the name.
	if got := m.Desks["vshop"][0].Desk; got != "vshop" {
		t.Errorf("desk became %q, want vshop", got)
	}
}

// Unplug a monitor and niri parks its workspaces on a survivor. That looks
// exactly like a move, and treating it as one would rename every workspace of
// the departed monitor - erasing the only record of home for any workspace no
// manifest declares (invariant 5).
func TestRebuildDoesNotRenameDisplaced(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "eDP-1"},
		{Name: "vshop.DP-1.agent", Output: "eDP-1"},
		{Name: "vshop.eDP-1.notes", Output: "eDP-1"},
	}, []string{"eDP-1"}) // DP-1 is gone

	if len(m.Renames) != 0 {
		t.Errorf("Renames = %v, want none: those monitors are gone, not wrong", m.Renames)
	}
	if len(m.Displaced) != 2 {
		t.Fatalf("Displaced = %v, want the two workspaces from the departed monitor", m.Displaced)
	}
	// Home survives in the name, which is what puts them back on replug.
	for _, n := range m.Displaced {
		if n.Monitor != "DP-1" {
			t.Errorf("displaced workspace kept monitor %q, want its home DP-1", n.Monitor)
		}
	}
	for _, n := range m.Desks["vshop"] {
		if n.Slot == "code" && n.Monitor != "DP-1" {
			t.Errorf("the map lost home for %s", n)
		}
	}
}

// Replug: the monitor is back and niri has returned the workspaces to it.
// Nothing to correct, and nothing displaced.
func TestRebuildReplugIsQuiet(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "vshop.eDP-1.notes", Output: "eDP-1"},
	}, []string{"DP-1", "eDP-1"})
	if len(m.Renames)+len(m.Displaced)+len(m.Conflicts) != 0 {
		t.Errorf("replug reported work: renames %v displaced %v conflicts %v", m.Renames, m.Displaced, m.Conflicts)
	}
}

// Ordinals restart per monitor, so a move can want a name that is already
// taken. Renaming anyway would put two workspaces under one name and break
// invariant 1 inside the map itself.
func TestRebuildReportsRenameCollision(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.1", Output: "HDMI-A-1"},
		{Name: "vshop.HDMI-A-1.1", Output: "HDMI-A-1"},
	}, both)

	if len(m.Renames) != 0 {
		t.Errorf("Renames = %v, want none: the truthful name is taken", m.Renames)
	}
	if len(m.Conflicts) != 1 {
		t.Fatalf("Conflicts = %v, want the collision", m.Conflicts)
	}
	if got := m.Conflicts[0].Wanted.String(); got != "vshop.HDMI-A-1.1" {
		t.Errorf("conflict wanted %q", got)
	}
	// No two workspaces share a name in the map that comes back.
	seen := map[string]bool{}
	for _, n := range m.Desks["vshop"] {
		if seen[n.String()] {
			t.Errorf("%s appears twice in the map", n)
		}
		seen[n.String()] = true
	}
}

// With no output list the caller does not know what is connected, so renaming
// would be a guess. Declining is the safe reading: a wrong rename is
// unrecoverable, a missing one is not.
func TestRebuildWithoutConnectedDoesNotRename(t *testing.T) {
	m := Rebuild([]Workspace{{Name: "vshop.DP-1.code", Output: "HDMI-A-1"}}, nil)
	if len(m.Renames) != 0 {
		t.Errorf("Renames = %v, want none without a connected-output list", m.Renames)
	}
}

// Rebuild is the recovery path, so it must not depend on anything zded
// remembers: the same input has to give the same map every time, and a second
// pass over its own corrected world has to be a no-op.
func TestRebuildIsStableAndConverges(t *testing.T) {
	in := []Workspace{
		{Name: "haven.DP-1.db", Output: "DP-1"},
		{Name: "vshop.DP-1.code", Output: "HDMI-A-1"},
		{Name: "nope", Output: "DP-1"},
	}
	first := Rebuild(in, both)
	for i := 0; i < 5; i++ {
		again := Rebuild(in, both)
		if !equal(first.Names(), again.Names()) ||
			len(again.Renames) != len(first.Renames) ||
			len(again.Foreign) != len(first.Foreign) {
			t.Fatal("Rebuild is not stable across calls")
		}
	}
	// Apply what it asked for, feed the result back: it must ask for nothing.
	var applied []Workspace
	for _, w := range in {
		n, err := ParseName(w.Name)
		if err != nil {
			applied = append(applied, w)
			continue
		}
		for _, r := range first.Renames {
			if r.From == n {
				n = r.To
			}
		}
		applied = append(applied, Workspace{Name: n.String(), Output: w.Output})
	}
	if second := Rebuild(applied, both); len(second.Renames) != 0 {
		t.Errorf("applying the renames did not converge: %v", second.Renames)
	}
}

// Regulars are reachable from every desk, which means they are in no desk's
// band: scrolling must not cross into them (invariant 4).
func TestBandExcludesRegulars(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "regulars.DP-1.1", Output: "DP-1"},
	}, both)
	band := m.Band("vshop", "DP-1")
	if len(band) != 1 || band[0].Slot != "code" {
		t.Errorf("Band(vshop) = %v, want only vshop's own workspaces", band)
	}
	for _, n := range band {
		if n.IsRegulars() {
			t.Error("the regulars band is inside a desk's band")
		}
	}
}

// A band is per monitor because the strip is: each monitor owns its own.
func TestBandIsPerMonitor(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "vshop.HDMI-A-1.aux", Output: "HDMI-A-1"},
	}, both)
	if got := m.Band("vshop", "DP-1"); len(got) != 1 || got[0].Slot != "code" {
		t.Errorf("Band(vshop, DP-1) = %v, want only DP-1's workspaces", got)
	}
	if got := m.Workspaces("vshop"); len(got) != 2 {
		t.Errorf("Workspaces(vshop) = %v, want both monitors", got)
	}
}

// The map's slices are the map's. A caller that sorts what it got back must
// not reorder the map underneath everyone else.
func TestWorkspacesIsACopy(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.a", Output: "DP-1"},
		{Name: "vshop.DP-1.b", Output: "DP-1"},
	}, both)
	got := m.Workspaces("vshop")
	got[0] = Name{"haven", "DP-1", "hijacked"}
	if m.Desks["vshop"][0].Desk != "vshop" {
		t.Error("writing to the returned slice changed the map")
	}
}

// An unknown desk is empty, not an error: it is what a desk switch to a
// manifest with nothing running yet looks like.
func TestBandUnknownDesk(t *testing.T) {
	m := Rebuild(nil, both)
	if got := m.Band("never-seen", "DP-1"); len(got) != 0 {
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
