package zded

import "github.com/crispuscrew/zde/internal/desk"

func (s *Server) whereWeAre() string {
	if m, err := s.niri.DeskMap(); err == nil {
		return s.activeDesk(m)
	}
	if focused, err := s.niri.FocusedName(); err == nil {
		if n, perr := desk.ParseName(focused); perr == nil {
			return n.Desk
		}
	}
	return ""
}

func (s *Server) activeDesk(m *desk.Map) string {
	focused, err := s.niri.FocusedName()
	if err != nil {
		focused = ""
	}
	return s.deskOf(m, focused)
}

// deskOf refreshes the journal from named workspaces. Only unnamed workspaces may
// use a remembered desk that still exists. The journal serializes writes; a racing
// switch can leave a stale observation until the compositor watcher reconciles.
func (s *Server) deskOf(m *desk.Map, focused string) string {
	if focused != "" {
		n, err := desk.ParseName(focused)
		if err != nil {
			return "" // named, but not by us
		}
		if s.jrn != nil && s.jrn.State().OnDesk != n.Desk {
			s.jrn.SetOnDesk(n.Desk)
		}
		return n.Desk
	}
	if s.jrn == nil {
		return ""
	}
	on := s.jrn.State().OnDesk
	if on == "" || len(m.Workspaces(on)) == 0 {
		return ""
	}
	return on
}
