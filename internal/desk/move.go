package desk

import "strconv"

// MoveTo is the name a workspace takes when its band changes: the same label,
// on the same monitor, under a different desk.
//
// A move is a rename because the name is the ownership record (docs/model.md,
// section 3). Nothing else has to be updated for a workspace to have changed
// hands, and nothing else can disagree about it afterwards.
//
// The monitor does not change. A name's middle part is the output the workspace
// is actually on, so a name claiming otherwise would be a lie that the next
// rebuild corrects - moving a workspace to another screen is the monitor group's
// job, and a different decision.
//
// A label already taken in the target band gets -2, -3, and so on, which is the
// rule adoption uses for a second firefox. False when no name can be built at
// all, which means the band name or the output cannot hold one: refusing beats
// minting a name that cannot be read back.
func MoveTo(m *Map, from Name, band string) (Name, bool) {
	taken := map[string]bool{}
	if m != nil {
		for _, n := range m.Workspaces(band) {
			taken[n.String()] = true
		}
	}
	for i := 1; ; i++ {
		slot := from.Slot
		if i > 1 {
			slot = from.Slot + "-" + strconv.Itoa(i)
		}
		n, err := NewName(band, from.Monitor, slot)
		if err != nil {
			return Name{}, false
		}
		if !taken[n.String()] {
			return n, true
		}
	}
}
