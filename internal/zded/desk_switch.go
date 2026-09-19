package zded

import "github.com/crispuscrew/zde/internal/desk"

func (s *Server) switchDesk(target string) Response { return s.switchFrom(target, "") }

// switchFrom captures prior journal state before naming workspaces changes focus.
// It records the destination only after all monitors move; entry policy and launches
// run only on a desk change, preserving a mode chosen by hand on re-entry.
func (s *Server) switchFrom(target, from string) Response {
	was := ""
	if s.jrn != nil {
		was = s.jrn.State().OnDesk
	}
	waiting, err := s.ensureDeclared(target)
	if err != nil {
		return Response{Error: err.Error()}
	}
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	plan := desk.SwitchPlan(m, target, s.landingSlots(target))
	if len(plan) == 0 {
		return Response{Error: noSuchBand(target)}
	}

	if from == "" {
		from = s.activeDesk(m)
	}

	for _, n := range plan {
		if err := s.niri.FocusWorkspace(n.String()); err != nil {
			return Response{Error: "switching to " + target + ": " + err.Error()}
		}
	}
	if s.jrn != nil {
		s.jrn.SetOnDesk(target)
		for _, n := range plan {
			s.jrn.SetActive(n)
		}
		if from != "" && from != target {
			s.jrn.SetLastDesk(from)
		}
	}
	if was != target {
		s.enterDesk(target)
		s.startApps(target)
	}
	focused := make([]string, 0, len(plan))
	for _, n := range plan {
		focused = append(focused, n.String())
	}
	resp := ok(focused)
	resp.Note = waitingNote(waiting)
	return resp
}
