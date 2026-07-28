// Package desk implements the spatial model (docs/model.md): the workspace
// name that records which desk owns what, and the desk map rebuilt from those
// names.
//
// The name is the ownership record, not a cache of one. zded holds no
// authoritative desk table it could disagree with niri about - it reads the
// workspace names back and rebuilds. That is what makes the mapping survive a
// zded crash, a compositor restart, and anything that renames a workspace
// behind our back. It also means a name that cannot be read back is data
// destroyed, so every path that mints one goes through NewName.
package desk

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Regulars is the reserved desk whose workspaces are reachable from every
// desk: comms, music, the personal browser (docs/glossary.md).
const Regulars = "regulars"

// partMax bounds each part. Nothing in niri or a manifest comes close; the
// point is that a name is a thing a person reads on a bar.
const partMax = 64

// A niri output connector: DP-1, HDMI-A-1, eDP-1, DP-2-1. Dashes separate,
// they do not trail or double.
var monitorPart = regexp.MustCompile(`^[A-Za-z]+(-[A-Za-z0-9]+)*$`)

// A desk name or a slot. A slot that is all digits is the ordinal form, which
// is what an unlabelled workspace gets.
var namePart = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Name is a workspace name: <desk>.<monitor>.<label-or-n>, for example
// vshop.DP-1.code or regulars.DP-1.1 (docs/model.md, section 3).
//
// The fields are exported to read. To build one, use NewName: a Name assembled
// by hand can hold a part with a dot in it, and String would then produce
// something ParseName cannot read back.
type Name struct {
	Desk    string
	Monitor string
	Slot    string
}

func (n Name) String() string {
	return n.Desk + "." + n.Monitor + "." + n.Slot
}

// IsRegulars reports whether this workspace is in the regulars band, which is
// in no desk's band and reachable from all of them.
func (n Name) IsRegulars() bool { return n.Desk == Regulars }

// Ordinal reads the slot back as a number. The second result is false for a
// label, which is the distinction between vshop.DP-1.code, declared by a
// manifest, and vshop.DP-1.2, which adoption minted.
func (n Name) Ordinal() (int, bool) {
	i, err := strconv.Atoi(n.Slot)
	if err != nil {
		return 0, false
	}
	return i, true
}

// NewName builds a workspace name and validates it, which is the only way to
// mint one that is certain to survive the round trip through niri.
func NewName(desk, monitor, slot string) (Name, error) {
	return ParseName(desk + "." + monitor + "." + slot)
}

// ValidDesk reports whether a string could name a desk - the same rule the
// desk part of a workspace name is held to, so that a desk asked for by name
// and a desk read off a workspace are the same kind of thing.
//
// It says nothing about whether that desk exists. Telling someone their typo
// is not a desk name is a different answer from telling them the desk is not
// there, and they want different things next.
func ValidDesk(s string) bool {
	return len(s) <= partMax && namePart.MatchString(s)
}

// ParseName reads a workspace name. A name that does not parse is not a zde
// workspace: it belongs to whoever made it, and Rebuild leaves it alone.
func ParseName(s string) (Name, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Name{}, fmt.Errorf("workspace name %q: want <desk>.<monitor>.<label-or-n>, got %d parts", s, len(parts))
	}
	n := Name{Desk: parts[0], Monitor: parts[1], Slot: parts[2]}
	for _, p := range parts {
		if len(p) > partMax {
			return Name{}, fmt.Errorf("workspace name %q: a part is longer than %d bytes", s, partMax)
		}
	}
	if !namePart.MatchString(n.Desk) {
		return Name{}, fmt.Errorf("workspace name %q: desk %q is not a lowercase name", s, n.Desk)
	}
	if !monitorPart.MatchString(n.Monitor) {
		return Name{}, fmt.Errorf("workspace name %q: monitor %q is not an output connector", s, n.Monitor)
	}
	if !namePart.MatchString(n.Slot) {
		return Name{}, fmt.Errorf("workspace name %q: slot %q is not a lowercase label or a number", s, n.Slot)
	}
	// One spelling per ordinal, or 7 and 007 are two names for one slot and
	// both can be handed out.
	if len(n.Slot) > 1 && n.Slot[0] == '0' && isDigits(n.Slot) {
		return Name{}, fmt.Errorf("workspace name %q: ordinal %q has a leading zero", s, n.Slot)
	}
	return n, nil
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
