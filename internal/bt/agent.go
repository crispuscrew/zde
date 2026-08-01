package bt

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
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
//     is a yes waiting for somebody to lean on the keyboard. Every question,
//     including the ones with nothing to answer: a passkey left on the screen
//     holds the one slot, and a held slot is a refusal of everything after it.
//  4. Pairing does not trust. Trusted is a separate property and a separate
//     verb, because trusted means "reconnect and use services without asking
//     again" - which is a decision a person makes about a device they own, not
//     a side effect of having once agreed to pair it.
//  5. An answer names the question it answers. A yes is spent on the question
//     the person read, or on nothing at all - see Answer.
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

// AnswerWait is how long a question stands before it is refused.
//
// Long enough to pick up a phone, read six digits and press a key; short enough
// that a request nobody is there for dies rather than waiting for whoever walks
// past next. BlueZ's own pairing procedure gives up around a minute, so this
// sits inside it: the refusal that reaches the other device should be ours and
// say so, rather than the connection timing out underneath us.
//
// Exported because it is the outer edge of every wait around it: the bus call
// that waits on it, and the client that watches for its answer, are both longer
// on purpose, and a client that named its own number would be one edit away
// from cutting a question short (see waitFor, WatchFor).
const AnswerWait = 45 * time.Second

// Request is a pairing question waiting for a person.
//
// The device is an address and not a name, because the agent is handed an
// object path by bluetoothd and nothing else. Whatever the device calls itself
// is in the device list, which every surface asking this question already has,
// and looking it up here would mean another bus call inside a callback
// bluetoothd is blocked on.
type Request struct {
	// ID names this question, and every answer has to carry it. It is a counter
	// and never reused inside a session, which is the whole of what it needs to
	// be: it is not a secret, it is a name for "the question that was on the
	// screen when you decided". See Answer for what it is defending against.
	ID     string `json:"id"`
	Device string `json:"device"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind"`
	// Passkey is six digits, zero padded, or empty where there is nothing to
	// compare. Padded because that is how the other device shows it: a person
	// comparing 12345 with 012345 is a person being asked a different question
	// than the one that matters.
	Passkey string `json:"passkey,omitempty"`
	// Entered is how many digits of that passkey the other device has taken so
	// far, which is the useful half of the repeats bluetoothd sends while
	// somebody types on a keyboard: it is the difference between a passkey being
	// typed and a passkey nobody is looking at.
	Entered int `json:"entered,omitempty"`
	// UUID is the service being asked for on a KindService question, and Service
	// is what that UUID means in words. A 128-bit number is not something a
	// person can answer: "wants to be a keyboard" is.
	UUID    string `json:"uuid,omitempty"`
	Service string `json:"service,omitempty"`
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
	// seq names questions. A counter rather than a random string: it never
	// leaves this machine, and one that reads 7 is one a person can type back.
	seq uint64
}

type question struct {
	req Request
	// answered carries the person's yes or no, and is nil for a question that
	// has no answer (KindDisplay). Buffered by one, so an answer arriving as
	// the wait expires does not block whoever gave it.
	answered chan bool
	// expiry ends a question that nothing is waiting on. The answerable ones end
	// themselves - ask is sitting on a select with a timeout - but a passkey
	// being typed has nobody in a select, and without this it would hold the one
	// slot until bluetoothd said otherwise, which it may never do.
	expiry *time.Timer
}

func NewAgent() *Agent { return &Agent{wait: AnswerWait} }

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

// Answer is the person saying yes or no to one named question.
//
// The name is the point, and it is what stands between this and the attack the
// whole file exists for. One slot and a bare yes means the answer lands on
// whatever is in the slot when it arrives, which is not necessarily what the
// person read: your phone's question is on the screen, bluetoothd cancels it,
// a stranger's device asks, and the y that was meant for the first pairs the
// second. Nothing has to go wrong for that - the first question expiring after
// its 45 seconds opens the same window - and the person sees nothing, because
// both questions look like "a device wants to pair".
//
// So an answer names the question, and an answer for a question that is not the
// one waiting is refused and said out loud. With nothing waiting it is an error
// rather than a note kept for later, for the same reason: a yes stored in
// advance would be spent on whatever asks next.
func (a *Agent) Answer(id string, yes bool) error {
	// Held across the send, so that an answer cannot be dropped into a question
	// that is being taken off the slot at that moment. The channel is buffered
	// and the send never blocks, so the lock is held for no longer than a copy.
	a.mu.Lock()
	defer a.mu.Unlock()
	q := a.q
	if q == nil {
		return errors.New("no pairing question is waiting for an answer")
	}
	if id != q.req.ID {
		return fmt.Errorf("that answer is for question %s, and the question waiting is %s "+
			"(%s wants to pair): it has not been answered", id, q.req.ID, q.req.Device)
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
//
// It expires like everything else here. Nothing is sitting in a select waiting
// on this one, so without a timer it would stand until bluetoothd said
// otherwise, and bluetoothd does not always say: a keyboard carried out of
// range mid-pairing leaves a passkey on the screen and the one slot taken, so
// the next real question - including an incoming pairing - is refused for being
// second. Principle 3 is not only about the questions with a button on them.
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
//
// The one thing that is not a second question is the same one again. bluetoothd
// re-sends DisplayPasskey for every digit the other device takes, so a keyboard
// being typed on produces six of them; treating those as second questions
// refuses the pairing that is going well and throws away the count, which is
// the only sign that anybody is typing at all.
func (a *Agent) open(req Request, answerable bool) (*question, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.q != nil {
		if !a.sameShown(req) {
			return nil, fmt.Errorf("another pairing question is already waiting (%s)", a.q.req.Device)
		}
		// More of the question already on the screen: the id stays, because it
		// is still the thing the person is looking at.
		req.ID = a.q.req.ID
		a.q.req = req
		a.q.expiry.Reset(a.wait)
		return a.q, nil
	}
	a.seq++
	req.ID = strconv.FormatUint(a.seq, 10)
	q := &question{req: req}
	if answerable {
		q.answered = make(chan bool, 1)
	}
	a.q = q
	// Identity, not a bare clear: by the time this fires the slot may hold a
	// different question, and ending somebody else's is how a passkey timer
	// becomes a way to cancel a pairing.
	q.expiry = time.AfterFunc(a.wait, func() { a.close(q) })
	return q, nil
}

// sameShown reports whether this is the passkey already on the screen, arriving
// again. Called with the lock held.
func (a *Agent) sameShown(req Request) bool {
	return req.Kind == KindDisplay &&
		a.q.req.Kind == KindDisplay &&
		a.q.req.Device == req.Device &&
		a.q.req.Passkey == req.Passkey
}

// close drops a question, and only if it is still the one standing.
func (a *Agent) close(q *question) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.q != q {
		return
	}
	a.q.expiry.Stop()
	a.q = nil
}

// Clear drops whatever is waiting, whichever question it is. Only for
// bluetoothd saying the pairing is over - Cancel and Release - because that is
// the one caller entitled to end a question it did not ask.
func (a *Agent) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.q == nil {
		return
	}
	a.q.expiry.Stop()
	a.q = nil
}

// ClearShown takes down the passkey being shown for one device, and only that.
//
// It is what a locally started pairing calls when its attempt ends. Anything
// broader is a bug with a person behind it: a pairing that this machine started
// and that failed in a second would otherwise take an incoming question off the
// screen mid-read, and bluetoothd would stay blocked on that question for its
// own 45 seconds with nobody able to answer it any more.
func (a *Agent) ClearShown(device string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.q == nil || a.q.req.Kind != KindDisplay || a.q.req.Device != device {
		return
	}
	a.q.expiry.Stop()
	a.q = nil
}

func rejected(why string) *dbus.Error {
	return &dbus.Error{Name: errRejected, Body: []any{why}}
}

// services is what the profiles a device asks for are called, by the 16 bits
// that vary in a Bluetooth base UUID.
//
// Not a lookup table for its own sake: a question a person cannot answer is a
// question they say yes to, and "0000110b-0000-1000-8000-00805f9b34fb wants
// authorising" is not answerable by anybody. Short, because these are the ones
// a person actually meets; anything else falls back to the raw UUID, which at
// least goes in a bug report.
//
// The one worth reading twice is 1124. A device asking for that is asking to be
// a keyboard on this machine, which is the profile that can type into anything
// the session has open.
var services = map[string]string{
	"1108": "a headset",
	"110a": "an audio source",
	"110b": "an audio sink (headphones)",
	"110c": "a remote control target",
	"110e": "a remote control",
	"111e": "hands free calling",
	"1124": "a keyboard or mouse (it could type into anything you have open)",
	"1105": "sending files to this machine",
	"112f": "your phone book",
	"1132": "your messages",
	"112d": "your SIM",
	"1116": "a network connection through this machine",
}

// serviceName is what a service UUID means, in words, or empty when this does
// not know. The Bluetooth base UUID is 0000xxxx-0000-1000-8000-00805f9b34fb and
// everything in the assigned list is one of those.
func serviceName(uuid string) string {
	u := strings.ToLower(strings.TrimSpace(uuid))
	if len(u) != 36 || !strings.HasSuffix(u, "-0000-1000-8000-00805f9b34fb") {
		return ""
	}
	return services[u[4:8]]
}

// exporter is the little of a bus connection that putting the agent on it
// needs.
//
// An interface so that what goes on the bus can be looked at in a test. It is
// the thing that matters and the one thing reflection over a Go type cannot
// show: bluetoothd calls whatever object was handed to Export, and handing it
// the Agent rather than the wrapper would export a type whose methods do not
// end in *dbus.Error - which godbus takes as no methods at all, silently, so
// every call from bluetoothd would fail and no pairing would ever ask anybody.
type exporter interface {
	Export(v any, path dbus.ObjectPath, iface string) error
}

// exportAgent puts the agent object on the connection, at the path the agent
// manager is told about.
func exportAgent(e exporter, a *Agent) error {
	return e.Export(&agent1{a: a}, AgentPath, agentIface)
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
	// The UUID and what it means, because the UUID alone is unanswerable: a
	// person cannot be asked whether 0000110b-0000-1000-8000-00805f9b34fb is
	// reasonable, and the one that matters most - a device asking to be a
	// keyboard - is indistinguishable from the rest as a number.
	return g.a.ask(Request{
		Device:  addrOf(device),
		Kind:    KindService,
		UUID:    printable(uuid),
		Service: serviceName(uuid),
	})
}

// DisplayPasskey is the keyboard case: this machine shows six digits and the
// person types them on the device being paired. Nothing to answer - typing it
// is the answer - so this returns at once, and bluetoothd sends it again for
// every digit taken, which is where the progress comes from.
func (g *agent1) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return g.a.show(Request{
		Device:  addrOf(device),
		Kind:    KindDisplay,
		Passkey: fmt.Sprintf("%06d", passkey),
		Entered: int(entered),
	})
}

// DisplayPinCode is the same thing for a device too old for passkeys. Shown
// rather than refused because a legacy keyboard is a real thing to be holding,
// and a PIN this machine chose is one the person can type.
//
// The PIN comes from bluetoothd rather than from the device, and it is still
// put through the same filter as everything else that is going to be drawn: one
// unchecked string on this path is one too many to have to think about.
func (g *agent1) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	return g.a.show(Request{Device: addrOf(device), Kind: KindDisplay, Passkey: printable(pincode)})
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
