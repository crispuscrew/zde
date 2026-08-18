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
// the name is what ownership is.
//
// The slot is the workspace's first app: vshop.DP-1.firefox, not
// vshop.DP-1.3. A name is something you read on a bar and say out loud, so it
// says what is in there. firstApp maps niri's workspace id to an application
// id; a manifest's own label will win over it when manifests land.
//
// Two things are deliberately not adopted. A workspace on no output has no
// monitor to be named onto. And an empty one is not adopted at all: niri keeps
// one empty workspace at the end of every strip, so claiming it would name the
// scratch space, niri would make another, and the next pass would claim that
// one too.
func AdoptPlan(m *Map, active string, firstApp map[uint64]string) []Adoption {
	if active == "" {
		return nil
	}
	taken := map[string]bool{}
	next := map[string]int{}
	for _, n := range m.Workspaces(active) {
		taken[n.String()] = true
		if i, isNum := n.Ordinal(); isNum && i >= next[n.Monitor] {
			next[n.Monitor] = i + 1
		}
	}
	var plan []Adoption
	for _, w := range m.Foreign() {
		if w.Output == "" {
			continue // on no monitor: nothing to name it onto
		}
		app, hasApp := firstApp[w.ID]
		if !hasApp {
			continue // empty: niri's scratch tail, not ours to claim
		}
		name, ok := slotFor(active, w.Output, app, taken, next)
		if !ok {
			continue
		}
		taken[name.String()] = true
		plan = append(plan, Adoption{ID: w.ID, Name: name})
	}
	return plan
}

// MissingPlan is the workspaces a manifest declares that niri does not have.
// They are created by naming an empty workspace on the right monitor, which is
// what niri leaves at the end of every strip - the same mechanic adoption
// avoids on purpose, used here on purpose.
//
// empty is output -> ids of unnamed workspaces with nothing in them. niri
// keeps exactly one per strip, so a desk that declares three workspaces on one
// monitor takes three passes: name one, let niri open the next, name that.
// Callers loop until this returns nothing.
//
// The second result is the declared workspaces on a monitor that is not a
// screen. There is no strip there for niri to leave an empty workspace at the
// end of, so no pass produces one and going round again cannot help - which is
// a different answer from "none spare this time round". Not a failure either:
// the manifest still declares them and the next switch after the monitor comes
// back makes them, so the caller says it rather than failing (internal/zded,
// switchFrom).
//
// Empty when the map was built without a screen list: which monitors are
// screens is then not known (Map.Screens).
func MissingPlan(m *Map, declared []Name, empty map[string][]uint64) ([]Adoption, []Name) {
	have := map[string]bool{}
	for _, d := range declared {
		for _, n := range m.Workspaces(d.Desk) {
			have[n.String()] = true
		}
		break
	}
	onScreen := map[string]bool{}
	for _, o := range m.Screens() {
		onScreen[o] = true
	}
	used := map[string]int{}
	var plan []Adoption
	var waiting []Name
	for _, want := range declared {
		if have[want.String()] {
			continue
		}
		if len(onScreen) > 0 && !onScreen[want.Monitor] {
			waiting = append(waiting, want)
			continue
		}
		ids := empty[want.Monitor]
		if used[want.Monitor] >= len(ids) {
			continue // no empty workspace on that monitor this time round
		}
		plan = append(plan, Adoption{ID: ids[used[want.Monitor]], Name: want})
		used[want.Monitor]++
		have[want.String()] = true
	}
	return plan, waiting
}

// slotFor is the workspace's label, made unique on its monitor, and an ordinal
// when the app id leaves nothing a name can hold.
func slotFor(active, output, app string, taken map[string]bool, next map[string]int) (Name, bool) {
	if label, ok := Label(app); ok {
		for i := 1; ; i++ {
			slot := label
			if i > 1 {
				slot = label + "-" + strconv.Itoa(i)
			}
			n, err := NewName(active, output, slot)
			if err != nil {
				break // the desk or the output cannot hold a name; fall back
			}
			if !taken[n.String()] {
				return n, true
			}
		}
	}
	if next[output] == 0 {
		next[output] = 1
	}
	for {
		n, err := NewName(active, output, strconv.Itoa(next[output]))
		if err != nil {
			// The desk name or the output is not one a workspace name can
			// hold. Refusing beats minting a name nothing can read back.
			return Name{}, false
		}
		next[output]++
		if !taken[n.String()] {
			return n, true
		}
	}
}
