package desk

// Rotation is the desks that desk.next and desk.prev walk, in the order they
// walk them.
//
// The regulars are not in it. They are reachable from every desk by their own
// action and belong to no desk's band (docs/model.md, invariant 4), so
// rotating into them would be arriving somewhere nothing is supposed to
// scroll, and leaving them would be ambiguous about where back is.
//
// The order is the map's own, which is alphabetical. niri's strip order is
// niri's to say and does not reach us yet (docs/roadmap.md, verify list), so
// this is the one order both ends can agree on without asking.
func (m *Map) Rotation() []string {
	all := m.DeskNames()
	out := make([]string, 0, len(all))
	for _, d := range all {
		if d == Regulars {
			continue
		}
		out = append(out, d)
	}
	return out
}

// Next is the desk after from, wrapping past the end.
//
// A from that is not in the rotation - no active desk yet, or the regulars,
// which are in none - answers with the first desk. A navigation key that does
// nothing is worse than one that goes somewhere it can explain.
func Next(rotation []string, from string) string { return step(rotation, from, 1) }

// Prev is the desk before from, wrapping past the start. An unknown from
// answers with the last desk, so that arriving from nowhere mirrors Next.
func Prev(rotation []string, from string) string { return step(rotation, from, -1) }

func step(rotation []string, from string, by int) string {
	if len(rotation) == 0 {
		return ""
	}
	for i, d := range rotation {
		if d == from {
			// Reduced before the shift, not after: Go's remainder keeps the
			// sign of its left side, so a step bigger than the rotation would
			// otherwise index backwards off the front of it.
			n := len(rotation)
			return rotation[((i+by)%n+n)%n]
		}
	}
	if by > 0 {
		return rotation[0]
	}
	return rotation[len(rotation)-1]
}
