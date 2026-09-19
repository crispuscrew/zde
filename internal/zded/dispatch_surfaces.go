package zded

import (
	"fmt"
	"strings"
)

func (s *Server) dispatchAsk(req Request) Response {
	switch req.Method {
	case "ask.oneshot":
		if len(req.Args) != 0 {
			return Response{Error: "ask.oneshot takes no arguments: the question is typed into the window"}
		}
		return s.askSurface(EventAsk, "")
	case "ask.panel":
		if len(req.Args) > 1 {
			return Response{Error: "ask.panel takes the question to open with, or nothing to open a panel to type into"}
		}
		question := ""
		if len(req.Args) == 1 {
			question = strings.TrimSpace(req.Args[0])
			if question == "" {
				return Response{Error: "nothing to ask: say what the question is"}
			}
		}
		return s.askSurface(EventAskPanel, question)
	}
	return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
}

func (s *Server) dispatchClipboard(req Request) Response {
	switch req.Method {
	case MethodClip:
		switch len(req.Args) {
		case 0:
			return s.clipHistory()
		case 1:
			return s.clipPut(req.Args[0])
		default:
			return Response{Error: "clip.history takes one entry id, or none to open the history"}
		}
	case "clip.clear":
		if len(req.Args) != 0 {
			return Response{Error: "clip.clear takes no arguments: it forgets the lot"}
		}
		return s.clipClear()
	}
	return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
}

func (s *Server) dispatchPalette(req Request) Response {
	switch req.Method {
	case "palette.list":
		if len(req.Args) != 0 {
			return Response{Error: "palette.list takes no arguments"}
		}
		return s.palette()
	case "palette.run":
		if len(req.Args) != 1 {
			return Response{Error: "palette.run takes one action name"}
		}
		return s.runAction(req.Args[0])
	}
	return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
}
