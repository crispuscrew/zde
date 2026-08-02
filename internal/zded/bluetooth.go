package zded

import (
	"errors"
	"fmt"
	"strconv"

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
	// Answer is the person saying yes or no to one named pairing question. The
	// name is not decoration: a bare yes lands on whatever holds the slot when
	// it arrives, which is not always the question that was read
	// (internal/bt/agent.go, Answer).
	Answer(id string, yes bool) error
	// Alive is whether the connection behind this is still usable. Asked of the
	// interface rather than probed for, because everything that can implement
	// this holds a bus connection, and one that has died is the difference
	// between a session with bluetooth and one where every verb fails until the
	// next login.
	Alive() bool
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
// One dial at a time, under bluetoothDial, and that is the point of that lock.
// Two callers that each dialled would each get their own bus connection, each
// export an agent, and each register one: BlueZ keys agents by the sender's
// unique name together with the path, so two connections are two different
// agents and the AlreadyExists check never fires. Closing the loser afterwards
// does not undo it either. If the loser is the one that won
// RequestDefaultAgent, what survives is an agent that is registered and is not
// the default, and then a phone pairing to this machine is refused by BlueZ
// with no question reaching anybody - the incoming path the default agent
// exists for, failing silently.
//
// The field has a second lock, and it is held around the field and nowhere
// else. One lock doing both jobs is what made Close wait for a bus: closeRadio
// wanted the same mutex this held across the dial, so a signal arriving while
// somebody's first bluetooth question was still connecting sat in Mutex.Lock -
// zded went on answering the socket through SIGTERM, left its socket file
// behind, and needed SIGKILL. The dial is bounded now (internal/bus), which
// shortens that window; splitting the locks is what closes it.
//
// Neither of them is s.mu, which also covers the listener, the subscribers and
// the token table: none of those has anything to say to the radio, and a dial
// held under s.mu would put every event broadcast behind a bus round trip.
func (s *Server) radio() (Bluetooth, error) {
	s.bluetoothDial.Lock()
	defer s.bluetoothDial.Unlock()

	s.bluetoothMu.Lock()
	have, gone := s.bluetooth, s.bluetoothGone
	if have != nil && !have.Alive() {
		// The system bus is restarted by its own updates and everything on it
		// goes with it, including the exported agent. Kept, this would be a
		// session where every bluetooth verb fails and no pairing question
		// reaches anybody until the next login; dropped, the dial below puts the
		// agent back.
		s.bluetooth = nil
		s.bluetoothMu.Unlock()
		have.Close()
		have = nil
	} else {
		s.bluetoothMu.Unlock()
	}
	if have != nil {
		return have, nil
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
	s.bluetoothMu.Lock()
	if s.bluetoothGone != gone {
		s.bluetoothMu.Unlock()
		// The session ended while this was connecting. Keeping it would be the
		// exact thing closeRadio exists to prevent: a pairing agent exported on
		// a connection nobody is going to answer through.
		r.Close()
		return nil, errors.New("zded gave up the radio while this was connecting")
	}
	s.bluetooth = r
	s.bluetoothMu.Unlock()
	return r, nil
}

// closeRadio gives up the connection, and the agent exported on it.
//
// It runs when the daemon stops answering. An exported pairing agent that
// outlives the session it belongs to is one nothing can answer through:
// bluetoothd would go on calling it, and every question would time out into a
// refusal nobody was asked for.
//
// Under the field's own lock and never inside s.mu, and never behind the dial
// either, so that this stays what the rest of the daemon is: one lock at a
// time, and none of them held across somebody else's round trip. It waits for
// nothing, which is what a signal handler needs of it.
//
// The count is what tells a dial still in flight that its connection is nobody's
// (see radio). Without it, a radio dialled after this ran would be stored into a
// server that has stopped, and the agent it exports would outlive the session
// after all.
func (s *Server) closeRadio() {
	s.bluetoothMu.Lock()
	r := s.bluetooth
	s.bluetooth = nil
	s.bluetoothGone++
	s.bluetoothMu.Unlock()
	if r == nil {
		return
	}
	// The error is dropped on purpose. This runs on the way out, the only thing
	// it can report is that a connection which is going away was already gone,
	// and Close answers with the listener's error - the one a caller can act on.
	r.Close()
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
	case "bluetooth.confirm":
		// The question first, then the answer: `bluetooth.confirm 7 yes`. Two
		// arguments rather than one because an answer that does not say what it
		// is answering is an answer to whatever is waiting - see
		// internal/bt/agent.go, Answer.
		if len(req.Args) != 2 {
			return Response{Error: "bluetooth.confirm takes the question's id and yes or no"}
		}
		yes, err := yesNo(req.Args[1])
		if err != nil {
			return Response{Error: "bluetooth.confirm takes yes or no, not " + strconv.Quote(req.Args[1])}
		}
		return done(r.Answer(req.Args[0], yes))
	case "bluetooth.power", "bluetooth.scan":
		on, err := onOff(req)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if req.Method == "bluetooth.power" {
			return done(r.Power(on))
		}
		return done(r.Discover(on))
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
	if len(req.Args) == 1 {
		switch req.Args[0] {
		case "on":
			return true, nil
		case "off":
			return false, nil
		}
	}
	return false, errors.New(req.Method + " takes on or off")
}

// yesNo is the same for an answer to a question. Its own words, and not the
// same set as on and off: "scan yes" and "confirm on" are both somebody typing
// past the thing they meant.
func yesNo(word string) (bool, error) {
	switch word {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	return false, errors.New("not yes or no")
}

// done is a verb with nothing to say but whether it worked. The empty list is
// what every other zde verb answers with when there is nothing to print.
func done(err error) Response {
	if err != nil {
		return Response{Error: err.Error()}
	}
	return ok([]string{})
}
