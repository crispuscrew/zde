package desk

// BandStep is the workspace one place along a band, and the whole of
// invariant 4: scrolling is clamped to the desk you are on, so crossing to
// another desk is always something you asked for by name or by rotation, never
// something you arrived at by holding a key down.
//
// The clamp is why this reports whether it moved rather than answering with
// where you already are. At the end of a band nothing happens, and a caller
// that cannot tell the difference would report a workspace change that never
// took place.
//
// From outside the band - a workspace nothing has named, or one belonging to
// somebody else - the step lands on the near end. There is no position to
// count from there, and the edge is the way back in.
func BandStep(band []Name, from Name, by int) (Name, bool) {
	if len(band) == 0 {
		return Name{}, false
	}
	for i, n := range band {
		if n == from {
			next := i + by
			if next < 0 || next >= len(band) {
				return Name{}, false // the band ends here, and so does this
			}
			return band[next], true
		}
	}
	if by > 0 {
		return band[0], true
	}
	return band[len(band)-1], true
}
