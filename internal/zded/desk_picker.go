package zded

import (
	"sort"

	"github.com/crispuscrew/zde/internal/desk"
)

// Switcher includes fallback rows; Shown requires acknowledgement within ackWait.
type Switcher struct {
	Shown bool     `json:"shown"`
	Desks []string `json:"desks"`
	On    string   `json:"on,omitempty"`
}

func (s *Server) switcher() Response { return s.deskPicker(EventPicker) }

func (s *Server) deskPicker(kind string) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	names := m.DeskNames()
	if kind != EventPicker {
		names = withRegulars(names)
	}
	if len(names) == 0 {
		return Response{Error: "no desks yet: write a manifest, or open something and it will be adopted into one"}
	}
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	on := s.activeDesk(m)
	shown := s.showSurface(Event{
		Kind:   kind,
		Desks:  names,
		On:     on,
		Output: output,
	})
	return ok(Switcher{Shown: shown, Desks: names, On: on})
}

// withRegulars offers the undeclared band as a destination, never an empty switch target.
func withRegulars(names []string) []string {
	for _, n := range names {
		if n == desk.Regulars {
			return names
		}
	}
	out := append(append([]string{}, names...), desk.Regulars)
	sort.Strings(out)
	return out
}
