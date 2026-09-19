package zded

import "fmt"

func (s *Server) dispatchSystem(req Request) Response {
	switch req.Method {
	case "system.power":
		switch len(req.Args) {
		case 0:
			return s.powerMenu()
		case 1:
			return s.powerRun(req.Args[0])
		default:
			return Response{Error: "system.power takes one action name, or none to open the menu"}
		}
	case "system.lock-preset":
		if len(req.Args) != 0 {
			return Response{Error: "system.lock-preset takes no arguments: the desk is zde.lock.preset in your home-manager config"}
		}
		return s.lockPreset()
	case "system.idle":
		if len(req.Args) != 0 {
			return Response{Error: "system.idle takes no arguments"}
		}
		return s.idleHold()
	}
	return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
}

func (s *Server) dispatchNetwork(req Request) Response {
	switch req.Method {
	case "net.connections":
		if len(req.Args) != 0 {
			return Response{Error: "net.connections takes no arguments"}
		}
		return s.connections()
	case "net.status":
		if len(req.Args) != 0 {
			return Response{Error: "net.status takes no arguments"}
		}
		return s.netStatus()
	case "net.list":
		if len(req.Args) != 0 {
			return Response{Error: "net.list takes no arguments"}
		}
		return s.netList()
	case "net.connect":
		if len(req.Args) != 1 && len(req.Args) != 2 {
			return Response{Error: "net.connect takes a network name, and a password when it needs one"}
		}
		return s.netConnect(req.Args)
	case "net.forget":
		if len(req.Args) != 1 {
			return Response{Error: "net.forget takes one network name"}
		}
		return s.netForget(req.Args[0])
	case "net.disconnect":
		if len(req.Args) != 0 {
			return Response{Error: "net.disconnect takes no arguments"}
		}
		return s.netDisconnect()
	case "net.kill":
		if len(req.Args) != 0 {
			return Response{Error: "net.kill takes no arguments: it is a toggle, and `zde net status` says which way it is"}
		}
		return s.netKill()
	}
	return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
}
