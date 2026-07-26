package desk

import "strconv"

// Adoption is a workspace about to be named into a desk: niri's id, because it
// has no name yet, and the name it is getting.
type Adoption struct {
	ID   uint64
	Name Name
}

// AdoptPlan names the workspaces that are nobody's into the active desk
// (invariant 3: whatever you create while desk X is active belongs to desk X).
//
// A foreign workspace is one niri made, or one somebody else named. Both are
// claimed the same way - by giving them a name in the active band - because
// the name is what ownership is. What is not claimed is a workspace on no
// output at all: there is no monitor to name it onto.
//
// Slots are the next free ordinals on that monitor, so an adopted workspace
// never collides with one a manifest declared by label, and never with another
// adoption in the same pass.
func AdoptPlan(m *Map, active string) []Adoption {
	if active == "" {
		return nil
	}
	next := map[string]int{}
	for _, n := range m.Workspaces(active) {
		if i, isNum := n.Ordinal(); isNum && i >= next[n.Monitor] {
			next[n.Monitor] = i + 1
		}
	}
	var plan []Adoption
	for _, w := range m.Foreign() {
		if w.Output == "" {
			continue // on no monitor: nothing to name it onto
		}
		if next[w.Output] == 0 {
			next[w.Output] = 1
		}
		name, err := NewName(active, w.Output, strconv.Itoa(next[w.Output]))
		if err != nil {
			// The desk name or the output is not one a workspace name can
			// hold. Refusing beats minting a name nothing can read back.
			continue
		}
		next[w.Output]++
		plan = append(plan, Adoption{ID: w.ID, Name: name})
	}
	return plan
}
