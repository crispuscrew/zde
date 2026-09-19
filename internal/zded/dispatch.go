package zded

import "fmt"

// Dispatch answers one request and echoes its optional ID on every return path.
func (s *Server) Dispatch(req Request) (resp Response) {
	defer func() { resp.ID = req.ID }()
	switch req.Method {
	case "ask.oneshot", "ask.panel":
		return s.dispatchAsk(req)
	case "queue.add", "queue.list", "queue.done", "queue.clear", "attn.mode", "attn.quiet", "attn.center", "attn.reach", "attn.invoke":
		return s.dispatchAttention(req)
	case MethodClip, "clip.clear":
		return s.dispatchClipboard(req)
	case "desk.switcher", "desk.list", "desk.switch", "desk.move-window", "desk.move-window-to", "desk.move-workspace-to", "desk.zen", "desk.queue-jump", "desk.regulars", "desk.panic", "desk.next", "desk.prev", "desk.apps", "desk.snapshot", "desk.reconcile", "desk.last":
		return s.dispatchDesk(req)
	case "net.connections", "net.status", "net.list", "net.connect", "net.forget", "net.disconnect", "net.kill":
		return s.dispatchNetwork(req)
	case "palette.list", "palette.run":
		return s.dispatchPalette(req)
	case "system.power", "system.lock-preset", "system.idle":
		return s.dispatchSystem(req)
	case "window.jump-to", "nav.down", "nav.up", "workspace.next", "workspace.prev":
		return s.dispatchWindow(req)
	case "status":
		return ok(s.status())
	case MethodShown:
		if len(req.Args) != 1 {
			return Response{Error: MethodShown + " takes the token from the event"}
		}
		return s.acknowledge(req.Args[0])
	case MethodEvents:
		return Response{Error: "events is asked of a connection, and this one is not keeping it open"}
	case MethodAskRun:
		return Response{Error: MethodAskRun + " is asked of a connection that keeps it open, and this one is not"}
	default:
		if bluetoothMethods[req.Method] {
			return s.bluetoothCall(req)
		}
		return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
	}
}
