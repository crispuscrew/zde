package zded

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/desk"
)

func (s *Server) dispatchDesk(req Request) Response {
	switch req.Method {
	case "desk.switcher":
		if len(req.Args) != 0 {
			return Response{Error: "desk.switcher takes no arguments"}
		}
		return s.switcher()
	case "desk.list":
		m, err := s.niri.DeskMap()
		if err != nil {
			return Response{Error: err.Error()}
		}
		return ok(m.DeskNames())
	case "desk.switch":
		if len(req.Args) != 1 {
			return Response{Error: "desk.switch takes one desk name"}
		}
		return s.switchDesk(req.Args[0])
	case "desk.move-window":
		if len(req.Args) != 1 || (req.Args[0] != "next" && req.Args[0] != "prev") {
			return Response{Error: "desk.move-window takes next or prev"}
		}
		if req.Args[0] == "next" {
			return s.moveWindow(desk.Next)
		}
		return s.moveWindow(desk.Prev)
	case "desk.move-window-to":
		switch len(req.Args) {
		case 0:
			return s.deskPicker(EventPickerMoveWindow)
		case 1:
			return s.moveWindowTo(req.Args[0])
		default:
			return Response{Error: "desk.move-window-to takes one desk name, or none to pick one"}
		}
	case "desk.move-workspace-to":
		switch len(req.Args) {
		case 0:
			return s.deskPicker(EventPickerMoveWorkspace)
		case 1:
			return s.moveWorkspaceTo(req.Args[0])
		default:
			return Response{Error: "desk.move-workspace-to takes one desk name, or none to pick one"}
		}
	case "desk.zen":
		return s.zen(req.Args)
	case "desk.queue-jump":
		if len(req.Args) != 0 {
			return Response{Error: "desk.queue-jump takes no arguments"}
		}
		return s.queueJump()
	case "desk.regulars":
		if len(req.Args) != 0 {
			return Response{Error: "desk.regulars takes no arguments"}
		}
		return s.regulars()
	case "desk.panic":
		if len(req.Args) != 0 {
			return Response{Error: "desk.panic takes no arguments: the decoy is zde.panic.decoy in your home-manager config, and the same key comes back"}
		}
		return s.deskPanic()
	case "desk.next", "desk.prev":
		if len(req.Args) != 0 {
			return Response{Error: req.Method + " takes no arguments"}
		}
		if req.Method == "desk.next" {
			return s.rotate(desk.Next)
		}
		return s.rotate(desk.Prev)
	case "desk.apps":
		return s.deskApps(req.Args)
	case "desk.snapshot":
		return s.snapshot(req.Args)
	case "desk.reconcile":
		return s.reconcile()
	case "desk.last":
		if s.jrn == nil {
			return Response{Error: "no journal, so no desk to go back to"}
		}
		prev := s.jrn.State().LastDesk
		if prev == "" {
			return Response{Error: "no desk to go back to yet"}
		}
		return s.lastDesk(prev)
	}
	return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
}

func (s *Server) dispatchWindow(req Request) Response {
	switch req.Method {
	case "window.jump-to":
		switch len(req.Args) {
		case 0:
			return s.jumpTo()
		case 1:
			return s.focusWindow(req.Args[0])
		default:
			return Response{Error: "window.jump-to takes one window id, or none to open the picker"}
		}
	case "nav.down", "nav.up":
		if len(req.Args) != 0 {
			return Response{Error: req.Method + " takes no arguments"}
		}
		return s.nav(req.Method == "nav.down")
	case "workspace.next", "workspace.prev":
		if len(req.Args) != 0 {
			return Response{Error: req.Method + " takes no arguments"}
		}
		if req.Method == "workspace.next" {
			return s.scroll(1)
		}
		return s.scroll(-1)
	}
	return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
}
