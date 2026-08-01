package zded

import (
	"errors"
	"fmt"

	"github.com/crispuscrew/zde/internal/bt"
)

// Bluetooth is what the daemon needs from the radio (internal/bt).
//
// An interface for the same reason Compositor is one: zded has to answer while
// the thing behind it is not there, and CI has no bluetooth stack. Everything
// that talks to BlueZ is on the other side of this line.
type Bluetooth interface {
	State() (bt.State, error)
	Power(on bool) error
	Discover(on bool) error
	Pair(addr string) error
	Connect(addr string) error
	Disconnect(addr string) error
	Forget(addr string) error
	// Trust says a device may reconnect and use services without being asked
	// about again. Never a side effect of pairing: see internal/bt/agent.go.
	Trust(addr string, yes bool) error
	// Answer is the person saying yes or no to the pairing question waiting.
	Answer(yes bool) error
	Close() error
}

// bluetoothMethods is every verb the socket answers for the radio. Named in one
// place because the dispatcher has to route them and this file has to handle
// them, and a name in one list and not the other is a method that reads as
// unknown or as unroutable.
var bluetoothMethods = map[string]bool{
	"bluetooth.state":      true,
	"bluetooth.power":      true,
	"bluetooth.scan":       true,
	"bluetooth.pair":       true,
	"bluetooth.connect":    true,
	"bluetooth.disconnect": true,
	"bluetooth.forget":     true,
	"bluetooth.trust":      true,
	"bluetooth.untrust":    true,
	"bluetooth.confirm":    true,
}

// radio opens the connection to BlueZ once and keeps it.
//
// Once, because the pairing agent lives on that connection and has to stay
// registered: a connection per call would offer an agent and withdraw it again,
// and a device pairing to this machine in between would reach nobody.
//
// Lazily, and not at startup, because of the machine this is off on. A desktop
// with no radio should not hold a system bus connection open for something it
// cannot do, and zded must start on a machine with no system bus at all.
func (s *Server) radio() (Bluetooth, error) {
	s.mu.Lock()
	have, open := s.bluetooth, s.openBluetooth
	s.mu.Unlock()
	if have != nil {
		return have, nil
	}
	if open == nil {
		return nil, errors.New("this zded has no way to reach bluetooth")
	}
	r, err := open()
	if err != nil {
		// Not remembered, so that a bus which comes back later works without a
		// new session.
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bluetooth != nil {
		// Two calls raced to open one; keep the first and drop this one, or the
		// agent would be registered twice and the loser leaked.
		r.Close()
		return s.bluetooth, nil
	}
	s.bluetooth = r
	return r, nil
}

// bluetoothCall answers one bluetooth method.
//
// The state verb answers even with no radio, because "there is no bluetooth
// here" is a fact somebody asked for rather than a failure. Everything that
// acts refuses, because a person who asked to pair something and got a
// cheerful nothing has been lied to.
func (s *Server) bluetoothCall(req Request) Response {
	if req.Method == "bluetooth.state" {
		if len(req.Args) != 0 {
			return Response{Error: "bluetooth.state takes no arguments"}
		}
		r, err := s.radio()
		if err != nil {
			return ok(bt.Absent(err.Error()))
		}
		st, err := r.State()
		if err != nil {
			return Response{Error: err.Error()}
		}
		return ok(st)
	}

	r, err := s.radio()
	if err != nil {
		return Response{Error: "no bluetooth: " + err.Error()}
	}
	switch req.Method {
	case "bluetooth.power", "bluetooth.scan", "bluetooth.confirm":
		on, err := onOff(req)
		if err != nil {
			return Response{Error: err.Error()}
		}
		switch req.Method {
		case "bluetooth.power":
			return done(r.Power(on))
		case "bluetooth.scan":
			return done(r.Discover(on))
		default:
			return done(r.Answer(on))
		}
	case "bluetooth.pair", "bluetooth.connect", "bluetooth.disconnect",
		"bluetooth.forget", "bluetooth.trust", "bluetooth.untrust":
		if len(req.Args) != 1 {
			return Response{Error: req.Method + " takes one device address"}
		}
		addr := req.Args[0]
		switch req.Method {
		case "bluetooth.pair":
			return done(r.Pair(addr))
		case "bluetooth.connect":
			return done(r.Connect(addr))
		case "bluetooth.disconnect":
			return done(r.Disconnect(addr))
		case "bluetooth.forget":
			return done(r.Forget(addr))
		case "bluetooth.trust":
			return done(r.Trust(addr, true))
		default:
			return done(r.Trust(addr, false))
		}
	}
	return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
}

// onOff reads the one word these verbs take. Words rather than a flag, because
// the CLI is what a person types and `scan on` says which way it goes where
// `scan` alone does not.
func onOff(req Request) (bool, error) {
	words := map[string]bool{"on": true, "off": false, "yes": true, "no": false}
	if len(req.Args) == 1 {
		if v, known := words[req.Args[0]]; known {
			return v, nil
		}
	}
	if req.Method == "bluetooth.confirm" {
		return false, errors.New("bluetooth.confirm takes yes or no")
	}
	return false, errors.New(req.Method + " takes on or off")
}

// done is a verb with nothing to say but whether it worked. The empty list is
// what every other zde verb answers with when there is nothing to print.
func done(err error) Response {
	if err != nil {
		return Response{Error: err.Error()}
	}
	return ok([]string{})
}
