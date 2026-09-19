package zded

import (
	"strconv"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/manifest"
)

func (s *Server) moveWindow(step func(rotation []string, from string) string) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	rotation := m.Rotation()
	if len(rotation) == 0 {
		return Response{Error: "no desks to move a window between yet"}
	}
	from := s.activeDesk(m)
	return s.carryTo(step(rotation, from), from)
}

func (s *Server) moveWindowTo(target string) Response {
	if !desk.ValidDesk(target) {
		return Response{Error: "not a desk name: " + target}
	}
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	return bandAdvice(target, s.carryTo(target, s.activeDesk(m)))
}

// moveWorkspaceTo renames ownership without moving focus. Declared slots are refused
// because ensureDeclared would recreate them on the next switch.
func (s *Server) moveWorkspaceTo(target string) Response {
	if !desk.ValidDesk(target) {
		return Response{Error: "not a desk name: " + target}
	}
	name, _, err := s.niri.FocusedPlace()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if name == "" {
		return Response{Error: "no workspace is focused, so there is none to move"}
	}
	from, err := desk.ParseName(name)
	if err != nil {
		return Response{Error: "the focused workspace is " + strconv.Quote(name) +
			", which is not a zde name: put something in it so it is adopted, and move that"}
	}
	if from.Desk == target {
		return Response{Error: name + " is already in " + target}
	}
	if d := s.manifestFor(from.Desk); d != nil && declaresSlot(d, from.Monitor, from.Slot) {
		return Response{Error: "desk " + from.Desk + " declares " + from.Slot + " on " + from.Monitor +
			": remove it from that manifest first, or it comes back on the next switch"}
	}
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	to, made := desk.MoveTo(m, from, target)
	if !made {
		return Response{Error: "no workspace name can be made for " + from.Slot + " in " + target}
	}
	if err := s.niri.RenameWorkspace(from.String(), to.String()); err != nil {
		return Response{Error: err.Error()}
	}
	if s.jrn != nil {
		s.jrn.Renamed(desk.Rename{From: from, To: to})
		s.jrn.SetOnDesk(target)
		s.jrn.SetActive(to)
		if from.Desk != target {
			s.jrn.SetLastDesk(from.Desk)
		}
	}
	return ok([]string{to.String()})
}

func declaresSlot(d *manifest.Desk, monitor, slot string) bool {
	for _, w := range d.Monitors[monitor].Workspaces {
		if w == slot {
			return true
		}
	}
	return false
}
