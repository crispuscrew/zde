package zded

import (
	"errors"

	"github.com/crispuscrew/zde/internal/link"
)

const EventConnections = "connections"

type Connections struct {
	Shown    bool           `json:"shown"`
	Link     link.Status    `json:"link"`
	Networks []link.Network `json:"networks"`
}

func (s *Server) connections() Response {
	out := Connections{Link: link.Status{Kind: link.KindAbsent}, Networks: []link.Network{}}
	m, err := s.links()
	switch {
	case errors.Is(err, link.ErrNoManager):
	case err != nil:
		return Response{Error: err.Error()}
	default:
		st, err := m.Status()
		if err != nil {
			return Response{Error: err.Error()}
		}
		out.Link = st
		networks, err := m.List()
		if err != nil {
			return Response{Error: err.Error()}
		}
		out.Networks = append(out.Networks, networks...)
	}

	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	out.Shown = s.showSurface(Event{
		Kind:     EventConnections,
		Link:     &out.Link,
		Networks: out.Networks,
		Output:   output,
	})
	return ok(out)
}

// netConnect sends the password only to the manager; it is neither stored nor logged.
func (s *Server) netConnect(args []string) Response {
	ssid := args[0]
	secret := ""
	if len(args) == 2 {
		secret = args[1]
	}
	if ssid == "" {
		return Response{Error: "net.connect takes the name of a network"}
	}
	m, err := s.links()
	if err != nil {
		return Response{Error: err.Error()}
	}
	err = m.Connect(ssid, secret)
	switch {
	case errors.Is(err, link.ErrStillTrying):
		return ok("joining " + ssid)
	case err != nil:
		return Response{Error: "joining " + ssid + ": " + err.Error()}
	}
	return ok("joined " + ssid)
}

func (s *Server) netForget(ssid string) Response {
	if ssid == "" {
		return Response{Error: "net.forget takes the name of a network"}
	}
	m, err := s.links()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if err := m.Forget(ssid); err != nil {
		return Response{Error: err.Error()}
	}
	return ok("forgot " + ssid)
}

// netKill reads state from NetworkManager and fails when no manager can cut the link.
func (s *Server) netKill() Response {
	m, err := s.links()
	if err != nil {
		if errors.Is(err, link.ErrNoManager) {
			return Response{Error: "there is no NetworkManager on this machine, so zde has no network to cut: " +
				"what this machine is online through is something zde cannot reach"}
		}
		return Response{Error: err.Error()}
	}
	st, err := m.Status()
	if err != nil {
		return Response{Error: err.Error()}
	}
	cut := !st.Killed
	if err := m.Kill(cut); err != nil {
		if cut {
			return Response{Error: "cutting the network: " + err.Error()}
		}
		return Response{Error: "putting the network back: " + err.Error()}
	}
	if cut {
		return ok("network cut: NetworkManager is off, the radios are not, and this action puts it back")
	}
	return ok("network back")
}

func (s *Server) netDisconnect() Response {
	m, err := s.links()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if err := m.Disconnect(); err != nil {
		return Response{Error: err.Error()}
	}
	return ok("disconnected")
}
