package zded

import "github.com/crispuscrew/zde/internal/desk"

// nav lets niri decide stacking; unchanged focus means the desk boundary.
func (s *Server) nav(down bool) Response {
	before, err := s.niri.FocusedWindow()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if err := s.niri.FocusWindowVertically(down); err != nil {
		return Response{Error: err.Error()}
	}
	after, err := s.niri.FocusedWindow()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if after != before {
		return ok([]string{})
	}
	if down {
		return s.rotate(desk.Next)
	}
	return s.rotate(desk.Prev)
}

// scroll stays within the current desk's band, using one focused-place snapshot.
func (s *Server) scroll(by int) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	focused, monitor, err := s.niri.FocusedPlace()
	if err != nil {
		return Response{Error: err.Error()}
	}
	from, _ := desk.ParseName(focused)
	on := s.deskOf(m, focused)
	if on == "" {
		return Response{Error: "no desk to scroll inside: nothing here belongs to one yet"}
	}
	band := m.Band(on, monitor)
	if len(band) == 0 {
		return Response{Error: "desk " + on + " has no workspaces on this screen"}
	}
	to, moved := desk.BandStep(band, from, by)
	if !moved {
		return ok([]string{})
	}
	if err := s.niri.FocusWorkspace(to.String()); err != nil {
		return Response{Error: err.Error()}
	}
	if s.jrn != nil {
		s.jrn.SetActive(to)
	}
	return ok([]string{to.String()})
}

// rotate skips the regulars; a one-desk rotation must not restore an older position.
func (s *Server) rotate(step func(rotation []string, from string) string) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	rotation := m.Rotation()
	if len(rotation) == 0 {
		return Response{Error: "no desks to rotate through yet"}
	}
	from := s.activeDesk(m)
	to := step(rotation, from)
	if to == from {
		return ok([]string{})
	}
	return s.switchDesk(to)
}

func (s *Server) regulars() Response {
	return bandAdvice(desk.Regulars, s.switchDesk(desk.Regulars))
}

// bandAdvice changes only the missing-band error: regulars cannot be declared.
func bandAdvice(target string, resp Response) Response {
	if target != desk.Regulars || resp.Error != noSuchBand(target) {
		return resp
	}
	return Response{Error: "no regulars yet: stand on a workspace you want to keep and run `zde desk move-workspace-to " + desk.Regulars + "`"}
}

func noSuchBand(target string) string {
	return "desk " + target + " has no workspaces and no manifest that declares any"
}

// lastDesk clears a vanished destination, but preserves it for transient failures.
func (s *Server) lastDesk(prev string) Response {
	resp := s.switchDesk(prev)
	if resp.Error != noSuchBand(prev) {
		return resp
	}
	s.jrn.SetLastDesk("")
	return Response{Error: "where you came from (" + prev + ") is not there any more, so there is nowhere to go back to"}
}
