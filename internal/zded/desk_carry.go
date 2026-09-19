package zded

import (
	"errors"

	"github.com/crispuscrew/zde/internal/desk"
)

// carryTo creates declared destinations before moving a window, so setup failures
// cannot strand it. from is captured before the move for desk.last.
func (s *Server) carryTo(to, from string) Response {
	if to == from {
		return ok([]string{})
	}
	if _, err := s.ensureDeclared(to); err != nil {
		return Response{Error: err.Error()}
	}
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	landed, err := s.carry(m, to)
	if err != nil {
		return Response{Error: err.Error()}
	}
	resp := s.switchFrom(to, from)
	if resp.Error != "" && landed != "" {
		resp.Error = "the window is on " + landed + ", but " + resp.Error
	}
	return resp
}

// landingSlots is shared by switches and carried windows: remembered slots first,
// then manifest order. Keys name logical monitors, including unplugged ones.
func (s *Server) landingSlots(target string) map[string]string {
	slots := map[string]string{}
	if s.jrn != nil {
		for monitor, slot := range s.jrn.State().LastActive[target] {
			slots[monitor] = slot
		}
	}
	if d := s.manifestFor(target); d != nil {
		for _, n := range d.Workspaces() {
			if _, remembered := slots[n.Monitor]; !remembered {
				slots[n.Monitor] = n.Slot
			}
		}
	}
	return slots
}

// carry keeps the window on its physical screen when the target has a band there.
func (s *Server) carry(m *desk.Map, target string) (string, error) {
	window, err := s.niri.FocusedWindow()
	if err != nil {
		return "", err
	}
	if window == 0 {
		return "", nil
	}
	monitor, err := s.niri.FocusedOutput()
	if err != nil {
		return "", err
	}
	slots := s.landingSlots(target)
	landing, onThisScreen := desk.Landing(m, target, monitor, slots)
	if !onThisScreen {
		plan := desk.SwitchPlan(m, target, slots)
		if len(plan) == 0 {
			return "", errors.New(noSuchBand(target))
		}
		landing = plan[0]
	}
	if err := s.niri.MoveWindowToWorkspace(landing.String()); err != nil {
		return "", err
	}
	return landing.String(), nil
}
