package zded

import "fmt"

func (s *Server) dispatchAttention(req Request) Response {
	switch req.Method {
	case "queue.add":
		if len(req.Args) != 1 {
			return Response{Error: "queue.add takes one line of text"}
		}
		return s.queueAdd(req.Args[0])
	case "queue.list":
		if len(req.Args) != 0 {
			return Response{Error: "queue.list takes no arguments"}
		}
		if s.jrn == nil {
			return Response{Error: "no journal, so nothing is waiting"}
		}
		return ok(s.jrn.Waiting())
	case "queue.done":
		if len(req.Args) != 1 {
			return Response{Error: "queue.done takes one id"}
		}
		return s.queueDone(req.Args[0])
	case "queue.clear":
		if len(req.Args) != 0 {
			return Response{Error: "queue.clear takes no arguments: it empties the whole queue"}
		}
		return s.queueClear()
	case "attn.mode":
		switch len(req.Args) {
		case 0:
			return ok(Attn{Mode: string(s.mode())})
		case 1:
			return s.setMode(req.Args[0])
		default:
			return Response{Error: "attn.mode takes one mode name, or none to read it back"}
		}
	case "attn.quiet":
		if len(req.Args) != 0 {
			return Response{Error: "attn.quiet takes no arguments: it is a toggle"}
		}
		return s.toggleQuiet()
	case "attn.center":
		if len(req.Args) != 0 {
			return Response{Error: "attn.center takes no arguments"}
		}
		return s.center()
	case "attn.reach":
		if len(req.Args) != 0 {
			return Response{Error: "attn.reach takes no arguments: it puts the keyboard on the newest popup"}
		}
		return s.reach()
	case "attn.invoke":
		if len(req.Args) != 2 {
			return Response{Error: "attn.invoke takes a notification id and the key of the action to press"}
		}
		return s.invoke(req.Args[0], req.Args[1])
	}
	return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
}
