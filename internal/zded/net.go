package zded

import (
	"errors"
	"time"

	"github.com/crispuscrew/zde/internal/link"
)

// The network side of the daemon (docs/model.md, section 6: system.connections).
//
// The logic is here and not in the shell, which is the rule the whole shell is
// built on: it is a thin adapter over this socket with zero logic inside
// (docs/vision.md, section 2). So the shell draws rows it was handed and sends
// back an ssid; deciding what is visible, what is saved, and what a refusal
// means is the daemon's.
//
// The secret is why this is worth saying twice. A password that only ever
// travels from a surface to zded over the private socket, and from zded to
// NetworkManager over the system bus, is a password with nowhere to be read
// from: no argv (internal/link, the note on nmcli), no file zde writes, and
// nothing on the way through that keeps it.

// EventConnections asks the shell to show the connections surface. Its own
// kind, like the windows picker: a shell that has never heard of it ignores
// the line rather than drawing a desk picker with no desks in it.
const EventConnections = "connections"

// Connections is what `net.connections` answers: the link, what is visible, and
// whether a surface took the job of showing them. Shown means what it means for
// the desk switcher, and for the same reason (see Switcher).
type Connections struct {
	Shown    bool           `json:"shown"`
	Link     link.Status    `json:"link"`
	Networks []link.Network `json:"networks"`
}

// links is the network side, opened the first time something asks and kept.
//
// Lazily, because a machine with no NetworkManager has to be a state and not a
// daemon that would not start - and because zded starts before the session's
// buses are necessarily up. Dropped and reopened when the connection dies:
// NetworkManager is restarted by its own updates, and a daemon holding a dead
// socket would report an unknown link for the rest of the session.
//
// A machine with no manager pays for the dial on every question the bar asks -
// once every five seconds, and a unix socket's worth of work. Remembering the
// absence would be cheaper, and would also be a daemon that never notices
// NetworkManager arriving, which is what a rebuild does.
func (s *Server) links() (link.Manager, error) {
	s.linkMu.Lock()
	defer s.linkMu.Unlock()
	if s.link != nil {
		// Asked for rather than required of the interface, the same way a sink
		// is asked whether it can take a write deadline (events.go): a manager
		// standing in for NetworkManager in a test has no connection to lose,
		// and making every implementation say so would be a method that is
		// always true everywhere but one.
		if alive, ok := s.link.(interface{ Alive() bool }); !ok || alive.Alive() {
			return s.link, nil
		}
		s.link = nil
	}
	open := s.openLink
	if open == nil {
		open = link.Open
	}
	m, err := open()
	if err != nil {
		return nil, err
	}
	s.link = m
	return m, nil
}

// netStatus is what the bar reads: what the link is right now.
//
// A machine with no NetworkManager answers "absent" rather than an error,
// because that is a fact about the machine and not a failure of the question.
// The bar draws it as its own third thing: not connected, not offline, no
// manager to ask.
func (s *Server) netStatus() Response {
	m, err := s.links()
	if errors.Is(err, link.ErrNoManager) {
		return ok(link.Status{Kind: link.KindAbsent})
	}
	if err != nil {
		return Response{Error: err.Error()}
	}
	st, err := m.Status()
	if errors.Is(err, link.ErrNoManager) {
		return ok(link.Status{Kind: link.KindAbsent})
	}
	if err != nil {
		return Response{Error: err.Error()}
	}
	return ok(st)
}

// netList is the visible networks, strongest first.
func (s *Server) netList() Response {
	m, err := s.links()
	if err != nil {
		return Response{Error: err.Error()}
	}
	networks, err := m.List()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if networks == nil {
		// Marshalled as [] rather than null, because everything reading this is
		// about to take its length (shell/Links.qml).
		networks = []link.Network{}
	}
	return ok(networks)
}

// netConnect joins a network.
//
// The secret is the second argument and it is never put anywhere else: not in
// the answer, not in the error, not in the daemon's log. The refusal names the
// network, because that is what a person needs to read, and says what
// NetworkManager said - which is the difference between a widget that failed
// and a widget that told you the password was wrong.
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
		// Not joined and not refused. Said plainly rather than reported as
		// success: the surface keeps watching the link, and a widget that
		// claimed a join here would be the one lie this whole verb exists to
		// avoid.
		return ok("joining " + ssid)
	case err != nil:
		return Response{Error: "joining " + ssid + ": " + err.Error()}
	}
	return ok("joined " + ssid)
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

// connections opens the connections surface, or says that nothing could open
// it - the same bargain as the desk switcher, and the reason the key does
// something on a session whose shell has died.
//
// A machine with no NetworkManager still opens it, with nothing in it and the
// reason on the surface. The alternative is a key that appears broken on every
// desktop that is not a laptop.
func (s *Server) connections() Response {
	out := Connections{Link: link.Status{Kind: link.KindAbsent}, Networks: []link.Network{}}
	m, err := s.links()
	switch {
	case errors.Is(err, link.ErrNoManager):
		// Left as absent, with an empty list.
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
		// Which screen is a detail; not knowing it is not worth refusing over,
		// and the shell falls back to the screen it can see.
		output = ""
	}
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	sent := s.broadcast(Event{
		Kind:     EventConnections,
		Link:     &out.Link,
		Networks: out.Networks,
		Output:   output,
		Token:    token,
	})
	if sent == 0 {
		return ok(out)
	}
	select {
	case <-acked:
		out.Shown = true
		return ok(out)
	case <-time.After(ackWait):
		return ok(out)
	}
}
