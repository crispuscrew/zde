package bt

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// The pairing agent is the security core of bluetooth, so the decisions it
// makes are written down here rather than left to be read out of the code.
//
// Pairing is the moment a device stops being a stranger. Everything after it -
// a keyboard that can type into this machine, a headset that can hear the
// room's audio, a phone that can browse the file system if a profile allows it
// - follows from somebody having said yes once. So:
//
//  1. The capability is DisplayYesNo. NoInputNoOutput is the one every quick
//     example uses, and it means "no person here, accept whatever asks": it is
//     how a stranger's device pairs itself to a laptop sitting in a cafe. This
//     machine has a screen and a person in front of it, and says so.
//  2. Nothing proceeds without an explicit yes. The passkey goes to the person,
//     the person compares it with what the other device shows, and the answer
//     comes back through zded.
//  3. A question nobody answers is refused, not left standing. Fail-closed is
//     the house rule (docs/vision.md, principle 9), and a request left hanging
//     is a yes waiting for somebody to lean on the keyboard.
//  4. Pairing does not trust. Trusted is a separate property and a separate
//     verb, because trusted means "reconnect and use services without asking
//     again" - which is a decision a person makes about a device they own, not
//     a side effect of having once agreed to pair it.
const (
	// AgentPath is where zded's agent sits. Under a zde name and not a bluez
	// one: bluetoothd is told this path when the agent registers, and the
	// object is ours.
	AgentPath = dbus.ObjectPath("/org/zde/bluetooth/agent")

	// Capability is what zde tells BlueZ this machine can do. See 1 above.
	Capability = "DisplayYesNo"

	// The errors BlueZ documents for an agent. Rejected is what a refusal is,
	// whether the person said no or nobody said anything: both mean the pairing
	// was not agreed to.
	errRejected = "org.bluez.Error.Rejected"
)

// The kinds of question. They differ in what the person is being shown and
// whether an answer is even possible, which is the whole of what a surface has
// to know to draw one.
const (
	// KindConfirm is numeric comparison: both ends show six digits and the
	// person says whether they match. This is what pairing a phone looks like.
	KindConfirm = "confirm"
	// KindAuthorize is a pairing with nothing to compare - "just works". There
	// is no number, so the only thing a person can check is that they are the
	// one who started it.
	KindAuthorize = "authorize"
	// KindService is a paired but untrusted device asking to use a profile.
	// This is what not auto-trusting costs: a headset that reconnects asks.
	// Saying yes to it once is not trust; `trust` is.
	KindService = "service"
	// KindDisplay is a passkey to type on the other device - what pairing a
	// keyboard looks like. Nothing to answer: it ends when the person has typed
	// it, or when the attempt gives up.
	KindDisplay = "display"
)

// answerWait is how long a question stands before it is refused.
//
// Long enough to pick up a phone, read six digits and press a key; short enough
// that a request nobody is there for dies rather than waiting for whoever walks
// past next. BlueZ's own pairing procedure gives up around a minute, so this
// sits inside it: the refusal that reaches the other device should be ours and
// say so, rather than the connection timing out underneath us.
const answerWait = 45 * time.Second

// Request is a pairing question waiting for a person.
//
// The device is an address and not a name, because the agent is handed an
// object path by bluetoothd and nothing else. Whatever the device calls itself
// is in the device list, which every surface asking this question already has,
// and looking it up here would mean another bus call inside a callback
// bluetoothd is blocked on.
type Request struct {
	Device string `json:"device"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind"`
	// Passkey is six digits, zero padded, or empty where there is nothing to
	// compare. Padded because that is how the other device shows it: a person
	// comparing 12345 with 012345 is a person being asked a different question
	// than the one that matters.
	Passkey string `json:"passkey,omitempty"`
	// UUID is the service being asked for, on a KindService question.
	UUID string `json:"uuid,omitempty"`
}

// Agent is org.bluez.Agent1: what bluetoothd calls when a pairing needs a
// person. It holds at most one question at a time and answers nothing by
// itself.
type Agent struct {
	// wait is how long a question stands. A field rather than the constant so
	// that a test can hold the timeout path without waiting three quarters of a
	// minute for it.
	wait time.Duration

	mu sync.Mutex
	q  *question
}

type question struct {
	req Request
	// answered carries the person's yes or no, and is nil for a question that
	// has no answer (KindDisplay). Buffered by one, so an answer arriving as
	// the wait expires does not block whoever gave it.
	answered chan bool
}

func NewAgent() *Agent { return &Agent{wait: answerWait} }

// Pending is the question waiting, if there is one. What the surface draws and
// what the CLI prints.
func (a *Agent) Pending() (Request, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.q == nil {
		return Request{}, false
	}
	return a.q.req, true
}

// Answer is the person saying yes or no.
//
// With nothing waiting it is an error rather than a note kept for later, and
// that is deliberate: a yes stored in advance is a yes that would be spent on
// whatever asks next, which is exactly the pairing nobody looked at.
func (a *Agent) Answer(yes bool) error {
	// Held across the send, so that an answer cannot be dropped into a question
	// that is being taken off the slot at that moment. The channel is buffered
	// and the send never blocks, so the lock is held for no longer than a copy.
	a.mu.Lock()
	defer a.mu.Unlock()
	q := a.q
	if q == nil {
		return errors.New("no pairing question is waiting for an answer")
	}
	if q.answered == nil {
		return errors.New("that question has no yes or no: " + q.req.Device +
			" is waiting for the passkey to be typed on it")
	}
	select {
	case q.answered <- yes:
	default:
		// Already answered, or the wait expired between the read and here.
		return errors.New("that question has already been answered")
	}
	return nil
}

// ask parks a question and waits for the person. Everything about refusal
// lives here: a second question while one is standing, a no, and a silence are
// all the same answer to BlueZ.
func (a *Agent) ask(req Request) *dbus.Error {
	q, err := a.open(req, true)
	if err != nil {
		return rejected(err.Error())
	}
	defer a.close(q)
	select {
	case yes := <-q.answered:
		if yes {
			return nil
		}
		return rejected("refused here")
	case <-time.After(a.wait):
		// Nobody was there. Refused rather than left standing: an unanswered
		// pairing request that waits for ever is a yes with a long fuse.
		return rejected("nobody answered, so it is refused")
	}
}

// show parks a question that has no answer - a passkey to type on the other
// device - and returns at once, because bluetoothd is waiting to carry on with
// the pairing this is part of.
func (a *Agent) show(req Request) *dbus.Error {
	if _, err := a.open(req, false); err != nil {
		return rejected(err.Error())
	}
	return nil
}

// open takes the one question slot, or refuses.
//
// One at a time, deliberately. A queue of pairing questions is a stack of
// dialogs to click through, and the second one is the one nobody reads - which
// is the whole attack: ask twice, and the answer to the first is spent on the
// second.
func (a *Agent) open(req Request, answerable bool) (*question, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.q != nil {
		return nil, fmt.Errorf("another pairing question is already waiting (%s)", a.q.req.Device)
	}
	q := &question{req: req}
	if answerable {
		q.answered = make(chan bool, 1)
	}
	a.q = q
	return q, nil
}

// close drops a question, and only if it is still the one standing.
func (a *Agent) close(q *question) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.q == q {
		a.q = nil
	}
}

// Clear drops whatever is waiting. Used when the attempt it belonged to is
// over: a passkey left on the screen after the pairing ended is a question
// about nothing, and it would hold the slot against the next real one.
func (a *Agent) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.q = nil
}

func rejected(why string) *dbus.Error {
	return &dbus.Error{Name: errRejected, Body: []any{why}}
}

// agent1 is the object on the bus: exactly the methods org.bluez.Agent1
// defines and nothing else.
//
// A type of its own for the same reason internal/attn has one: what is exported
// is every method of what the connection is handed, so exporting the Agent
// itself would publish Answer - and then anything that can reach this object
// could say yes to its own pairing. The method set is pinned by a test.
type agent1 struct{ a *Agent }

// Release is bluetoothd saying the agent is no longer registered. Whatever was
// waiting is waiting on nothing.
func (g *agent1) Release() *dbus.Error {
	g.a.Clear()
	return nil
}

// RequestConfirmation is numeric comparison, and it is the method this whole
// package is built around: six digits on both screens, and a person to say
// whether they are the same six.
func (g *agent1) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	return g.a.ask(Request{
		Device:  addrOf(device),
		Kind:    KindConfirm,
		Passkey: fmt.Sprintf("%06d", passkey),
	})
}

// RequestAuthorization is a pairing with no number to compare, which BlueZ
// calls "just works". It is asked anyway: the whole difference between this
// machine and one running a NoInputNoOutput agent is that somebody is told.
func (g *agent1) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	return g.a.ask(Request{Device: addrOf(device), Kind: KindAuthorize})
}

// AuthorizeService is a paired but untrusted device asking to use a profile.
//
// Asked rather than allowed, and that is the price of not trusting on pairing:
// a headset reconnecting asks once per profile until somebody trusts it. That
// is what trust means and it is the reason the verb exists - the alternative is
// a device that was agreed to once quietly getting everything for ever.
func (g *agent1) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	return g.a.ask(Request{Device: addrOf(device), Kind: KindService, UUID: uuid})
}

// DisplayPasskey is the keyboard case: this machine shows six digits and the
// person types them on the device being paired. Nothing to answer - typing it
// is the answer - so this returns at once and the question stands until the
// attempt ends.
func (g *agent1) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return g.a.show(Request{
		Device:  addrOf(device),
		Kind:    KindDisplay,
		Passkey: fmt.Sprintf("%06d", passkey),
	})
}

// DisplayPinCode is the same thing for a device too old for passkeys. Shown
// rather than refused because a legacy keyboard is a real thing to be holding,
// and a PIN this machine chose is one the person can type.
func (g *agent1) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	return g.a.show(Request{Device: addrOf(device), Kind: KindDisplay, Passkey: pincode})
}

// RequestPinCode wants a PIN made up here and typed on the other device.
// Refused: the honest answers are a PIN a person chooses, which needs a text
// field this surface does not have, or the "0000" every example uses - and
// answering a pairing request with a constant is the auto-accept this agent
// exists to not be. Legacy pairing waits for a field to type in.
func (g *agent1) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	return "", rejected("zde cannot type a PIN into this: pair it from the device's own side")
}

// RequestPasskey is the mirror: a number shown on the other device and typed
// here. Refused for the same reason - there is nowhere to type it - and a
// DisplayYesNo agent should not be asked for it at all.
func (g *agent1) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	return 0, rejected("zde has nowhere to type a passkey into: pair it from the device's own side")
}

// Cancel is the other end giving up. The question goes with it, so that the
// slot is free and no passkey is left on screen for a pairing that ended.
func (g *agent1) Cancel() *dbus.Error {
	g.a.Clear()
	return nil
}
