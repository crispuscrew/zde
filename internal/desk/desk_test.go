package desk

import (
	"reflect"
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
		{"vshop.Unknown-1.0", Name{"vshop", "Unknown-1", "0"}},
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
// A desk asked for by name is held to the rule the desk part of a workspace
// name is held to, or a desk could be asked for that no workspace could ever
// belong to.
func TestValidDesk(t *testing.T) {
	for _, ok := range []string{"vshop", "haven", "regulars", "side-project", "b2b", "1"} {
		if !ValidDesk(ok) {
			t.Errorf("ValidDesk(%q) = false, want a desk name", ok)
		}
	}
	for _, bad := range []string{"", "VShop", "has space", "with.dots", "-lead", "trail-", "a--b", strings.Repeat("x", 65)} {
		if ValidDesk(bad) {
			t.Errorf("ValidDesk(%q) = true, want refused", bad)
		}
	}
}

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
		"vshop-.DP-1.code",  // trailing dash
		"vshop.DP-.code",    // trailing dash in a connector
		"vshop.D---1.code",  // a run of dashes
		"vshop.1DP.code",    // a connector starts with a letter
		"vshop.DP 1.code",   // no spaces
		"vshop.DP-1.007",    // one spelling per ordinal
		"vshop.DP-1.ф",      // ascii only
		"vshop.DP-1." + strings.Repeat("a", partMax+1), // a name is something you read
	} {
		if _, err := ParseName(in); err == nil {
			t.Errorf("ParseName(%q) was accepted", in)
		}
	}
}

// NewName is the checked way to mint one. A Name literal is not checked, which
// is how an unreadable name would get into the system.
func TestNewName(t *testing.T) {
	n, err := NewName("vshop", "DP-1", "code")
	if err != nil || n.String() != "vshop.DP-1.code" {
		t.Errorf("NewName = %v, %v", n, err)
	}
	for _, c := range [][3]string{
		{"VSHOP", "DP-1", "code"},
		{"vshop", "DP-1", "co.de"},
		{"vshop", "Dell Inc. U2515H", "code"},
	} {
		if _, err := NewName(c[0], c[1], c[2]); err == nil {
			t.Errorf("NewName%v was accepted", c)
		}
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

	if got, want := m.DeskNames(), []string{"haven", Regulars, "vshop"}; !equal(got, want) {
		t.Errorf("DeskNames() = %v, want %v", got, want)
	}
	if got := len(m.Workspaces("vshop")); got != 3 {
		t.Errorf("vshop has %d workspaces, want 3", got)
	}
	if got := len(m.Foreign()); got != 2 {
		t.Errorf("Foreign = %v, want the unnamed and the foreign one", m.Foreign())
	}
	// Foreign carries the output: adoption has to know where it is naming to.
	for _, f := range m.Foreign() {
		if f.Output == "" {
			t.Errorf("foreign workspace %+v lost its output", f)
		}
	}
	if got := len(m.Regulars()); got != 1 {
		t.Errorf("Regulars() = %v, want the one regulars workspace", m.Regulars())
	}
	if len(m.Renames())+len(m.Conflicts())+len(m.Displaced()) != 0 {
		t.Error("a map where every name agrees with niri reported work to do")
	}
}

// Monitor first, then slot. Data where the two orderings differ, because with
// the wrong data both give the same answer and the test proves nothing.
func TestBandOrderAcrossMonitors(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.HDMI-A-1.aaa", Output: "HDMI-A-1"},
		{Name: "vshop.DP-1.zzz", Output: "DP-1"},
	}, both)
	got := m.Workspaces("vshop")
	if got[0].String() != "vshop.DP-1.zzz" || got[1].String() != "vshop.HDMI-A-1.aaa" {
		t.Errorf("order = %v, want monitor before slot", got)
	}
}

// Inside one monitor the order is the strip's, which is niri's to say, and not
// the names'. Everything that reads a desk's workspaces reads which end is the
// top out of this: a switch with nothing remembered lands there, and a snapshot
// writes the order into a manifest. Sorting names here put the bottom of the
// strip first whenever the labels happened to sort that way.
func TestWorkspacesFollowTheStrip(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.zsh", Output: "DP-1", Idx: 0},
		{ID: 2, Name: "vshop.DP-1.agent", Output: "DP-1", Idx: 1},
	}, both)
	var got []string
	for _, n := range m.Workspaces("vshop") {
		got = append(got, n.Slot)
	}
	if !equal(got, []string{"zsh", "agent"}) {
		t.Errorf("Workspaces = %v, want the strip's order [zsh agent]", got)
	}
	// And the band agrees, or a switch and a scroll disagree about the top.
	var band []string
	for _, n := range m.Band("vshop", "DP-1") {
		band = append(band, n.Slot)
	}
	if !equal(band, got) {
		t.Errorf("Band = %v, Workspaces = %v, want one order", band, got)
	}
}

// The one shape where Band's own sort does work the map's cannot, and so the
// only shape that can pin it. The map keeps a desk's workspaces by monitor and
// then down the strip, so on any screen showing one monitor's run the two
// orders are the same and deleting Band's sort changes nothing. A monitor that
// is gone is the exception: its workspaces are parked on a survivor, the map
// still keeps the two runs apart by the monitor in their names, and the screen
// has them interleaved. A scroll follows what the screen has, or it steps over
// a workspace and then back to it.
func TestBandInterleavesTwoMonitorsParkedOnOneScreen(t *testing.T) {
	m := Rebuild([]Workspace{
		// eDP-1 is a connector and not a screen: the lid is shut, and niri has
		// parked its workspaces on DP-1 between DP-1's own.
		{ID: 1, Name: "vshop.DP-1.top", Output: "DP-1", Idx: 0},
		{ID: 2, Name: "vshop.eDP-1.parked", Output: "DP-1", Idx: 1},
		{ID: 3, Name: "vshop.DP-1.bottom", Output: "DP-1", Idx: 2},
	}, []string{"DP-1"})
	// The map keeps them apart, which is what makes this test say something.
	var kept []string
	for _, n := range m.Workspaces("vshop") {
		kept = append(kept, n.Slot)
	}
	if !equal(kept, []string{"top", "bottom", "parked"}) {
		t.Fatalf("Workspaces = %v, want the two monitors' runs kept apart", kept)
	}
	var band []string
	for _, n := range m.Band("vshop", "DP-1") {
		band = append(band, n.Slot)
	}
	if !equal(band, []string{"top", "parked", "bottom"}) {
		t.Errorf("Band = %v, want the order the screen has [top parked bottom]", band)
	}
}

// Ordinals are numbers, so 2 sorts before 10. Lexical order would have put the
// strip in an order no human reads.
func TestBandOrderOrdinals(t *testing.T) {
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
	if len(m.Renames()) != 1 {
		t.Fatalf("Renames = %v, want the moved workspace", m.Renames())
	}
	r := m.Renames()[0]
	if r.From.String() != "vshop.DP-1.code" || r.To.String() != "vshop.HDMI-A-1.code" {
		t.Errorf("rename %s -> %s, want vshop.DP-1.code -> vshop.HDMI-A-1.code", r.From, r.To)
	}
	// The map holds the corrected name, not the stale one.
	if got := m.Workspaces("vshop")[0].Monitor; got != "HDMI-A-1" {
		t.Errorf("map kept monitor %q, want the one niri reports", got)
	}
	// Ownership does not move with the monitor. That is the whole point of
	// putting the desk in the name.
	if got := m.Workspaces("vshop")[0].Desk; got != "vshop" {
		t.Errorf("desk became %q, want vshop", got)
	}
}

// A workspace with no output reported is not evidence of a move.
func TestRebuildIgnoresEmptyOutput(t *testing.T) {
	m := Rebuild([]Workspace{{Name: "vshop.DP-1.code", Output: ""}}, both)
	if len(m.Renames()) != 0 {
		t.Errorf("Renames = %v, want none for a workspace niri placed nowhere", m.Renames())
	}
	if got := m.Workspaces("vshop"); len(got) != 1 || got[0].Monitor != "DP-1" {
		t.Errorf("Workspaces = %v, want the name untouched", got)
	}
}

// niri's output string is niri's, not ours. One that is not a connector would
// mint a name nothing can read back, and the workspace would leave its desk
// with no way home.
func TestRebuildRefusesToMintAnUnreadableName(t *testing.T) {
	for _, output := range []string{"DP.1", "1DP", "Dell Inc. U2515H", `code"; spawn "sh`} {
		m := Rebuild([]Workspace{{Name: "vshop.DP-1.code", Output: output}}, []string{"DP-1", output})
		if len(m.Renames()) != 0 {
			t.Errorf("output %q produced a rename %v", output, m.Renames())
		}
		if len(m.Conflicts()) != 1 {
			t.Errorf("output %q was not reported as a conflict", output)
		}
		// The workspace stays where it was, under a name that still parses.
		got := m.Workspaces("vshop")
		if len(got) != 1 {
			t.Fatalf("output %q lost the workspace: %v", output, got)
		}
		if _, err := ParseName(got[0].String()); err != nil {
			t.Errorf("map holds an unreadable name %q", got[0])
		}
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

	if len(m.Renames()) != 0 {
		t.Errorf("Renames = %v, want none: those monitors are gone, not wrong", m.Renames())
	}
	if len(m.Displaced()) != 2 {
		t.Fatalf("Displaced = %v, want the two workspaces from the departed monitor", m.Displaced())
	}
	// Home survives in the name, which is what puts them back on replug.
	for _, n := range m.Displaced() {
		if n.Monitor != "DP-1" {
			t.Errorf("displaced workspace kept monitor %q, want its home DP-1", n.Monitor)
		}
	}
	for _, n := range m.Workspaces("vshop") {
		if n.Slot == "code" && n.Monitor != "DP-1" {
			t.Errorf("the map lost home for %s", n)
		}
	}
}

// Replug: the monitor is back and niri has returned the workspaces to it.
func TestRebuildReplugIsQuiet(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "vshop.eDP-1.notes", Output: "eDP-1"},
	}, []string{"DP-1", "eDP-1"})
	if len(m.Renames())+len(m.Displaced())+len(m.Conflicts()) != 0 {
		t.Errorf("replug reported work: %v %v %v", m.Renames(), m.Displaced(), m.Conflicts())
	}
}

// Ordinals restart per monitor, so a move can want a name that is already
// taken. Renaming anyway would put two workspaces under one name.
func TestRebuildReportsRenameCollision(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.1", Output: "HDMI-A-1"},
		{Name: "vshop.HDMI-A-1.1", Output: "HDMI-A-1"},
	}, both)

	if len(m.Renames()) != 0 {
		t.Errorf("Renames = %v, want none: the truthful name is taken", m.Renames())
	}
	if len(m.Conflicts()) != 1 {
		t.Fatalf("Conflicts = %v, want the collision", m.Conflicts())
	}
	if !strings.Contains(m.Conflicts()[0].Reason, "taken") {
		t.Errorf("conflict reason %q does not say what happened", m.Conflicts()[0].Reason)
	}
}

// Two workspaces arriving under one name, with no rename involved at all.
func TestRebuildReportsDuplicateNames(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: "DP-1"},
		{Name: "vshop.DP-1.code", Output: "DP-1"},
	}, both)
	if got := len(m.Workspaces("vshop")); got != 1 {
		t.Errorf("vshop has %d workspaces, want the duplicate kept out of the map", got)
	}
	if len(m.Conflicts()) != 1 {
		t.Errorf("Conflicts = %v, want the duplicate reported", m.Conflicts())
	}
	// Not dropped: it exists, it is just not ours to file until it has a
	// name of its own.
	if got := len(m.Foreign()); got != 1 {
		t.Errorf("Foreign = %v, want the duplicate handed to adoption", m.Foreign())
	}
}

// With no output list the caller does not know what is connected, so renaming
// would be a guess. A missing rename is recoverable; a wrong one is not.
func TestRebuildWithoutConnectedDoesNotRename(t *testing.T) {
	m := Rebuild([]Workspace{{Name: "vshop.DP-1.code", Output: "HDMI-A-1"}}, nil)
	if len(m.Renames()) != 0 {
		t.Errorf("Renames = %v, want none without a connected-output list", m.Renames())
	}
}

// What the map must never contain, whatever it was fed. This is the shape of
// keymap's TestRegistryInvariants: the rules live in prose and in no type, so
// they live here.
func TestMapInvariants(t *testing.T) {
	inputs := [][]Workspace{
		{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "vshop.DP-1.1", Output: "HDMI-A-1"},
			{Name: "vshop.HDMI-A-1.1", Output: "HDMI-A-1"},
			{Name: "regulars.DP-1.1", Output: "HDMI-A-1"},
			{Name: "haven.DP-9.gone", Output: "DP-1"},
			{Name: "", Output: "DP-1"},
			{Name: "scratch", Output: ""},
		},
		nil,
	}
	for _, in := range inputs {
		for _, connected := range [][]string{both, nil, {"DP-1"}} {
			m := Rebuild(in, connected)
			seen := map[string]bool{}
			counted := len(m.Foreign())
			for _, d := range m.DeskNames() {
				for _, n := range m.Workspaces(d) {
					if n.Desk != d {
						t.Errorf("%s is filed under desk %q", n, d)
					}
					if seen[n.String()] {
						t.Errorf("%s appears twice in the map", n)
					}
					seen[n.String()] = true
					if _, err := ParseName(n.String()); err != nil {
						t.Errorf("map holds an unreadable name %q: %v", n, err)
					}
					counted++
				}
			}
			// Every workspace is accounted for exactly once: owned by a
			// desk, or foreign. Nothing is dropped in silence, whatever
			// Rebuild decided about it.
			if counted != len(in) {
				t.Errorf("connected=%v: %d workspaces in, %d accounted for",
					connected, len(in), counted)
			}
			for _, r := range m.Renames() {
				if r.From.Desk != r.To.Desk || r.From.Slot != r.To.Slot {
					t.Errorf("rename %s -> %s changed more than the monitor", r.From, r.To)
				}
			}
		}
	}
}

// A chain: one workspace wants the name another is on its way off. It used to
// resolve differently depending on which end niri listed first, and the losing
// order left a desk declaring a monitor it has nothing on.
func TestRebuildResolvesAChainWhicheverEndArrivesFirst(t *testing.T) {
	screens := []string{"DP-1", "eDP-1", "HDMI-A-1"}
	onEDP := Workspace{ID: 1, Name: "vshop.DP-1.zsh", Output: "eDP-1"}
	onDP := Workspace{ID: 2, Name: "vshop.HDMI-A-1.zsh", Output: "DP-1"}

	want := []string{"vshop.DP-1.zsh", "vshop.eDP-1.zsh"}
	for _, in := range [][]Workspace{{onEDP, onDP}, {onDP, onEDP}} {
		m := Rebuild(in, screens)
		var got []string
		for _, n := range m.Workspaces("vshop") {
			got = append(got, n.String())
		}
		if !equal(got, want) {
			t.Errorf("listed %s %s, the desk is %v, want %v", in[0].Name, in[1].Name, got, want)
		}
		if len(m.Renames()) != 2 || len(m.Conflicts()) != 0 {
			t.Errorf("listed %s %s: renames %v, conflicts %v, want both names corrected",
				in[0].Name, in[1].Name, m.Renames(), m.Conflicts())
		}
	}
}

// Renames go to niri one at a time (internal/zded, reconcile), so a chain has
// to start with the workspace that frees a name. Here that one sorts second.
func TestRenamesComeBackInAnOrderThatCanBeApplied(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.zsh", Output: "eDP-1"},
		{ID: 2, Name: "vshop.eDP-1.zsh", Output: "HDMI-A-1"},
	}, []string{"DP-1", "eDP-1", "HDMI-A-1"})

	renames := m.Renames()
	if len(renames) != 2 {
		t.Fatalf("Renames = %v, want the chain corrected", renames)
	}
	// Applied in the order given, each rename must find its new name free.
	inUse := map[string]bool{"vshop.DP-1.zsh": true, "vshop.eDP-1.zsh": true}
	for _, r := range renames {
		if inUse[r.To.String()] {
			t.Fatalf("renaming %s -> %s hands niri a name still in use; order was %v", r.From, r.To, renames)
		}
		delete(inUse, r.From.String())
		inUse[r.To.String()] = true
	}
}

// Two workspaces swapping monitors want each other's names. There is no first
// rename that does not collide, so neither is made and both are told why.
func TestRebuildRefusesASwap(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.zsh", Output: "eDP-1"},
		{ID: 2, Name: "vshop.eDP-1.zsh", Output: "DP-1"},
	}, []string{"DP-1", "eDP-1"})

	if len(m.Renames()) != 0 {
		t.Errorf("Renames = %v, want none: a swap cannot be done one rename at a time", m.Renames())
	}
	if len(m.Conflicts()) != 2 {
		t.Fatalf("Conflicts = %v, want both halves of the swap reported", m.Conflicts())
	}
	for _, c := range m.Conflicts() {
		if !strings.Contains(c.Reason, "one at a time") {
			t.Errorf("conflict reason %q does not say why the swap cannot be made", c.Reason)
		}
	}
	// Both keep their own names, so nothing has been erased.
	if got := len(m.Workspaces("vshop")); got != 2 {
		t.Errorf("vshop has %d workspaces, want both still there", got)
	}
}

// A rename names the workspace it means (internal/niri, RenameWorkspace), so a
// name two of them share addresses neither - including the one the map keeps.
func TestRebuildDoesNotRenameOffASharedName(t *testing.T) {
	m := Rebuild([]Workspace{
		{ID: 1, Name: "vshop.DP-1.code", Output: "HDMI-A-1"},
		{ID: 2, Name: "vshop.DP-1.code", Output: "eDP-1"},
	}, []string{"DP-1", "HDMI-A-1", "eDP-1"})
	if len(m.Renames()) != 0 {
		t.Errorf("Renames = %v, want none: that name means two workspaces", m.Renames())
	}
	if len(m.Conflicts()) != 1 {
		t.Errorf("Conflicts = %v, want the duplicate named once", m.Conflicts())
	}
}

// The property the two halves of Rebuild exist for: one arrangement is one map,
// however niri listed it. Slots repeat across monitors so that the shapes make
// chains, which is what a hand-written case keeps missing.
func TestRebuildDoesNotDependOnTheOrderNiriListedThings(t *testing.T) {
	screens := []string{"DP-1", "eDP-1", "HDMI-A-1"}
	slots := []string{"zsh", "code", "1", "2"}
	rnd := uint64(20260818) // a fixed seed: a failing shape has to be reproducible

	next := func(n int) int {
		// xorshift, because this needs a deterministic sequence and not a good
		// one, and math/rand's default source is neither pinned nor ours.
		rnd ^= rnd << 13
		rnd ^= rnd >> 7
		rnd ^= rnd << 17
		return int(rnd % uint64(n))
	}

	for shape := 0; shape < 3000; shape++ {
		var in []Workspace
		for i := 0; i < 1+next(5); i++ {
			in = append(in, Workspace{
				ID:     uint64(i + 1),
				Idx:    uint8(next(3)),
				Name:   "vshop." + screens[next(len(screens))] + "." + slots[next(len(slots))],
				Output: screens[next(len(screens))],
			})
		}
		want := Rebuild(in, screens)
		for shuffle := 0; shuffle < 6; shuffle++ {
			mixed := append([]Workspace(nil), in...)
			for i := len(mixed) - 1; i > 0; i-- {
				j := next(i + 1)
				mixed[i], mixed[j] = mixed[j], mixed[i]
			}
			if got := Rebuild(mixed, screens); !reflect.DeepEqual(got, want) {
				t.Fatalf("one arrangement, two maps.\nlisted %v\n  gives %+v\nlisted %v\n  gives %+v",
					in, want, mixed, got)
			}
		}
	}
}

// With no screen list Rebuild decides nothing: no rename, and no displacement
// either, since displacement is the claim that a monitor has gone and nothing
// here is evidence for one. A missing rename is recoverable; a wrong one erases
// home.
func TestRebuildWithNoScreenListDecidesNothing(t *testing.T) {
	for _, screens := range [][]string{nil, {}} {
		m := Rebuild([]Workspace{
			{Name: "vshop.DP-1.code", Output: "HDMI-A-1"},
			{Name: "vshop.HDMI-A-1.notes", Output: "DP-1"},
			{Name: "vshop.eDP-1.mail", Output: ""},
		}, screens)
		if len(m.Renames()) != 0 {
			t.Errorf("screens=%v: Renames = %v, want none", screens, m.Renames())
		}
		if len(m.Displaced()) != 0 {
			t.Errorf("screens=%v: Displaced = %v, want none: nothing here says a monitor is gone",
				screens, m.Displaced())
		}
		if len(m.Conflicts()) != 0 {
			t.Errorf("screens=%v: Conflicts = %v, want none", screens, m.Conflicts())
		}
		if got := len(m.Workspaces("vshop")); got != 3 {
			t.Errorf("screens=%v: vshop has %d workspaces, want all three untouched", screens, got)
		}
	}
}

// niri reporting no output is niri not saying, not the workspace being nowhere.
// The name is the only other answer, and without it the workspace falls out of
// its band and out of every switch.
func TestBandHoldsAWorkspaceNiriGaveNoOutput(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.code", Output: ""},
		{Name: "vshop.DP-1.notes", Output: "DP-1"},
	}, both)

	band := m.Band("vshop", "DP-1")
	if len(band) != 2 {
		t.Fatalf("Band(vshop, DP-1) = %v, want both: the one niri placed and the one it said nothing about", band)
	}
	if plan := SwitchPlan(m, "vshop", nil); len(plan) != 1 {
		t.Errorf("SwitchPlan = %v, want the desk brought up on DP-1", plan)
	}
}

// The other half: the name answers only while the monitor it names is a screen.
// With the lid shut it is not, so the workspace is on none - and putting it in
// the switched-off panel's band spends a switch's focus there. The focus that
// lands last keeps the keyboard, and connectors sort, so it can be that one.
func TestAWorkspaceOnNoScreenIsInNoBand(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.eDP-1.mail", Output: ""}, // the shut lid
		{Name: "vshop.DP-1.code", Output: "DP-1"},
	}, []string{"DP-1"})

	if got := m.Band("vshop", "eDP-1"); len(got) != 0 {
		t.Errorf("Band(vshop, eDP-1) = %v, want nothing: eDP-1 is not a screen", got)
	}
	plan := SwitchPlan(m, "vshop", nil)
	if len(plan) != 1 || plan[0].Monitor != "DP-1" {
		t.Errorf("SwitchPlan = %v, want the one screen there is", plan)
	}
	// Its name still records home, which is what puts it back on the next
	// reconcile after the lid opens, and the map says it is not at home.
	if got := m.Displaced(); len(got) != 1 || got[0].String() != "vshop.eDP-1.mail" {
		t.Errorf("Displaced = %v, want the workspace whose monitor is switched off", got)
	}
	if got := m.Workspaces("vshop"); len(got) != 2 {
		t.Errorf("Workspaces = %v, want the workspace kept with its name", got)
	}
}

// Rebuild is the recovery path: it must not depend on anything zded remembers,
// must not touch what it was given, and must not report churn just because
// niri listed things in a different order.
func TestRebuildIsPure(t *testing.T) {
	in := []Workspace{
		{Name: "haven.DP-1.db", Output: "DP-1"},
		{Name: "vshop.DP-1.code", Output: "HDMI-A-1"},
		{Name: "nope", Output: "DP-1"},
		{Name: "regulars.HDMI-A-1.1", Output: "HDMI-A-1"},
	}
	untouched := append([]Workspace(nil), in...)

	if !reflect.DeepEqual(Rebuild(in, both), Rebuild(in, both)) {
		t.Error("two rebuilds of one input differ")
	}
	if !reflect.DeepEqual(in, untouched) {
		t.Error("Rebuild modified its input")
	}
	// Same set, different order in: same map out.
	shuffled := []Workspace{in[3], in[1], in[0], in[2]}
	if !reflect.DeepEqual(Rebuild(in, both), Rebuild(shuffled, both)) {
		t.Error("the map depends on the order niri listed workspaces in")
	}
}

// Applying what Rebuild asked for and feeding the result back must ask for
// nothing: a converging loop, not an oscillating one.
func TestRebuildConverges(t *testing.T) {
	in := []Workspace{
		{Name: "vshop.DP-1.code", Output: "HDMI-A-1"},
		{Name: "haven.DP-1.db", Output: "DP-1"},
	}
	first := Rebuild(in, both)
	var applied []Workspace
	for _, w := range in {
		n, err := ParseName(w.Name)
		if err != nil {
			applied = append(applied, w)
			continue
		}
		for _, r := range first.Renames() {
			if r.From == n {
				n = r.To
			}
		}
		applied = append(applied, Workspace{Name: n.String(), Output: w.Output})
	}
	if second := Rebuild(applied, both); len(second.Renames()) != 0 {
		t.Errorf("applying the renames did not converge: %v", second.Renames())
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
	// And they are still reachable, by name.
	if got := m.Regulars(); len(got) != 1 {
		t.Errorf("Regulars() = %v, want the regulars workspace", got)
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

// The map's slices are the map's. A caller that sorts or overwrites what it
// got back must not reorder the map underneath everyone else.
func TestAccessorsReturnCopies(t *testing.T) {
	m := Rebuild([]Workspace{
		{Name: "vshop.DP-1.a", Output: "DP-1"},
		{Name: "vshop.DP-1.b", Output: "DP-1"},
		{Name: "nope", Output: "DP-1"},
	}, both)

	m.Workspaces("vshop")[0] = Name{"haven", "DP-9", "hijacked"}
	m.Band("vshop", "DP-1")[0] = Name{"haven", "DP-9", "hijacked"}
	m.Foreign()[0] = Workspace{Name: "hijacked"}
	if got := m.Workspaces("vshop")[0]; got.Desk != "vshop" || got.Slot != "a" {
		t.Errorf("writing to a returned slice changed the map: %v", got)
	}
	if got := m.Foreign()[0].Name; got != "nope" {
		t.Errorf("writing to Foreign changed the map: %v", got)
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
