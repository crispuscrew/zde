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
// Lazily, and not at startup, because zded has to start on a machine with no
// system bus at all, and because a machine that never asks about bluetooth
// should not have a connection opened for it. What it does cost is a connection
// with the agent exported on it, from the first question until the daemon stops
// - which is what Close gives up again.
//
// The lock is held across the dial and not only around the field, and that is
// the point of it. Two callers that each dialled would each get their own bus
// connection, each export an agent, and each register one: BlueZ keys agents by
// the sender's unique name together with the path, so two connections are two
// different agents and the AlreadyExists check never fires. Closing the loser
// afterwards does not undo it either. If the loser is the one that won
// RequestDefaultAgent, what survives is an agent that is registered and is not
// the default, and then a phone pairing to this machine is refused by BlueZ
// with no question reaching anybody - the incoming path the default agent
// exists for, failing silently.
//
// Its own mutex rather than s.mu, which also covers the listener, the
// subscribers and the token table: none of those has anything to say to the
// radio, and a dial held under s.mu would put every event broadcast behind a
// bus round trip.
func (s *Server) radio() (Bluetooth, error) {
	s.bluetoothMu.Lock()
	defer s.bluetoothMu.Unlock()
	if s.bluetooth != nil {
		return s.bluetooth, nil
	}
	if s.openBluetooth == nil {
		return nil, errors.New("this zded has no way to reach bluetooth")
	}
	r, err := s.openBluetooth()
	if err != nil {
		// Not remembered, so that a bus which comes back later works without a
		// new session.
		return nil, err
	}
	s.bluetooth = r
	return r, nil
}

// closeRadio gives up the connection, and the agent exported on it.
//
// It runs when the daemon stops answering. An exported pairing agent that
// outlives the session it belongs to is one nothing can answer through:
// bluetoothd would go on calling it, and every question would time out into a
// refusal nobody was asked for.
//
// Under the radio's own lock and never inside s.mu, so that this stays what the
// rest of the daemon is: one lock at a time, and none of them held across
// somebody else's round trip.
func (s *Server) closeRadio() {
	s.bluetoothMu.Lock()
	defer s.bluetoothMu.Unlock()
	if s.bluetooth == nil {
		return
	}
	// The error is dropped on purpose. This runs on the way out, the only thing
	// it can report is that a connection which is going away was already gone,
	// and Close answers with the listener's error - the one a caller can act on.
	s.bluetooth.Close()
	s.bluetooth = nil
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
