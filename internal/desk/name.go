// Package desk implements the spatial model (docs/model.md): the workspace
// name that records which desk owns what, and the desk map rebuilt from those
// names.
//
// The name is the ownership record, not a cache of one. zded holds no
// authoritative desk table it could disagree with niri about - it reads the
// workspace names back and rebuilds. That is what makes the mapping survive a
// zded crash, a compositor restart, and anything that renames a workspace
// behind our back.
package desk

import (
	"fmt"
	"regexp"
	"strings"
)

// Regulars is the reserved desk whose workspaces are reachable from every
// desk: comms, music, the personal browser (docs/glossary.md).
const Regulars = "regulars"

// A niri output connector: DP-1, HDMI-A-1, eDP-1.
var monitorPart = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// A desk name or a slot. A slot that is all digits is the ordinal form, which
// is what an unlabelled workspace gets.
var namePart = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Name is a workspace name: <desk>.<monitor>.<label-or-n>, for example
// vshop.DP-1.code or regulars.DP-1.1 (docs/model.md, section 3).
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

// ParseName reads a workspace name. A name that does not parse is not a zde
// workspace: it belongs to whoever made it, and Rebuild leaves it alone.
func ParseName(s string) (Name, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Name{}, fmt.Errorf("workspace name %q: want <desk>.<monitor>.<label-or-n>, got %d parts", s, len(parts))
	}
	n := Name{Desk: parts[0], Monitor: parts[1], Slot: parts[2]}
	if !namePart.MatchString(n.Desk) {
		return Name{}, fmt.Errorf("workspace name %q: desk %q is not a lowercase name", s, n.Desk)
	}
	if !monitorPart.MatchString(n.Monitor) {
		return Name{}, fmt.Errorf("workspace name %q: monitor %q is not an output connector", s, n.Monitor)
	}
	if !namePart.MatchString(n.Slot) {
		return Name{}, fmt.Errorf("workspace name %q: slot %q is not a lowercase label or a number", s, n.Slot)
	}
	return n, nil
}
