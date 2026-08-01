package bt

import (
	"reflect"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// A device path the way bluetoothd hands them to an agent.
const phone = dbus.ObjectPath("/org/bluez/hci0/dev_44_5C_E9_1A_2B_3C")

// quick is an agent whose questions do not stand for three quarters of a
// minute, so the refusal path can be held without the test sleeping through it.
func quick() *Agent {
	a := NewAgent()
	a.wait = 50 * time.Millisecond
	return a
}

// asked runs an agent method in the background and hands back the answer when
// it comes. Every question blocks the caller - that is what a question is - so
// a test that called one directly would deadlock on its own assertion.
func asked(call func() *dbus.Error) chan *dbus.Error {
	out := make(chan *dbus.Error, 1)
	go func() { out <- call() }()
	return out
}

func waitFor(t *testing.T, out chan *dbus.Error) *dbus.Error {
	t.Helper()
	select {
	case derr := <-out:
		return derr
	case <-time.After(2 * time.Second):
		t.Fatal("the agent never answered bluetoothd")
		return nil
	}
}

// waitPending waits for the question to reach the slot. The agent method runs
// in another goroutine, so asking straight away asks before it got there.
func waitPending(t *testing.T, a *Agent) Request {
	t.Helper()
	for i := 0; i < 200; i++ {
		if req, ok := a.Pending(); ok {
			return req
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no question ever reached the person")
	return Request{}
}

// Nothing pairs without somebody saying yes. Break this and a device in range
// pairs itself to the machine while the person is looking at something else,
// which is the whole reason this package has an agent at all.
func TestPairingNeedsAnExplicitYes(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	out := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 4291) })

	req := waitPending(t, a)
	if req.Device != "44:5C:E9:1A:2B:3C" || req.Kind != KindConfirm {
		t.Errorf("question = %+v, want a confirmation for the phone", req)
	}
	// Zero padded, because that is how the other end shows it: a person told
	// 4291 while their phone says 004291 is being asked a different question.
	if req.Passkey != "004291" {
		t.Errorf("passkey = %q, want six digits", req.Passkey)
	}
	select {
	case derr := <-out:
		t.Fatalf("pairing finished before anybody answered: %v", derr)
	case <-time.After(10 * time.Millisecond):
	}

	if err := a.Answer(true); err != nil {
		t.Fatal(err)
	}
	if derr := waitFor(t, out); derr != nil {
		t.Errorf("a pairing that was agreed to was refused: %v", derr)
	}
	if _, waiting := a.Pending(); waiting {
		t.Error("the question is still waiting after it was answered")
	}
}

// A question nobody answers ends in a refusal. Break this and an unanswered
// pairing request stands for ever - which is a yes with a long fuse, waiting
// for whoever next leans on the keyboard.
func TestUnansweredPairingIsRefused(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	out := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 123456) })
	waitPending(t, a)

	derr := waitFor(t, out)
	if derr == nil {
		t.Fatal("nobody answered and the pairing was accepted")
	}
	if derr.Name != errRejected {
		t.Errorf("refused with %q, want %q", derr.Name, errRejected)
	}
	// And the slot is free, or the next real question would be refused for
	// being second.
	if _, waiting := a.Pending(); waiting {
		t.Error("the question that timed out is still holding the slot")
	}
}

// No means no, and it reaches BlueZ as a refusal rather than as a failure to
// answer. Break this and the button that says no does nothing.
func TestPairingSaidNoIsRefused(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	out := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 1) })
	waitPending(t, a)

	if err := a.Answer(false); err != nil {
		t.Fatal(err)
	}
	derr := waitFor(t, out)
	if derr == nil || derr.Name != errRejected {
		t.Errorf("a refused pairing answered %v", derr)
	}
}

// Just works pairing has no number to compare, and is asked about anyway.
// Break this and the association model with nothing to check becomes the one
// that needs no person - which is the cafe attack exactly.
func TestJustWorksPairingIsStillAsked(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	out := asked(func() *dbus.Error { return g.RequestAuthorization(phone) })

	req := waitPending(t, a)
	if req.Kind != KindAuthorize || req.Passkey != "" {
		t.Errorf("question = %+v, want an authorisation with nothing to compare", req)
	}
	if derr := waitFor(t, out); derr == nil {
		t.Error("nobody answered and it was allowed")
	}
}

// A paired but untrusted device asking for a service is asked about too. Break
// this and pairing once quietly buys everything for ever, which is what trust
// is supposed to mean and pairing is not.
func TestAnUntrustedDevicesServiceIsAsked(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	out := asked(func() *dbus.Error {
		return g.AuthorizeService(phone, "0000110b-0000-1000-8000-00805f9b34fb")
	})

	req := waitPending(t, a)
	if req.Kind != KindService || req.UUID == "" {
		t.Errorf("question = %+v, want the service named", req)
	}
	if derr := waitFor(t, out); derr == nil {
		t.Error("nobody answered and the service was allowed")
	}
}

// One question at a time. Break this and two requests stack up, which is a
// dialog to click through - and the second one is the one nobody reads.
func TestASecondQuestionIsRefusedWhileOneWaits(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	first := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 1) })
	waitPending(t, a)

	other := dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF")
	if derr := g.RequestConfirmation(other, 2); derr == nil {
		t.Error("a second pairing question was accepted while one was waiting")
	}
	// And the first one is untouched: it is still the question, and still the
	// one an answer belongs to.
	if req, _ := a.Pending(); req.Device != "44:5C:E9:1A:2B:3C" {
		t.Errorf("the waiting question became %+v", req)
	}
	a.Answer(true)
	if derr := waitFor(t, first); derr != nil {
		t.Errorf("the first question was answered yes and got %v", derr)
	}
}

// A yes with nothing to say it to is refused rather than kept. Break this and
// a stray confirmation is spent on whatever asks next, which is precisely the
// pairing nobody looked at.
func TestAnswerBeforeTheQuestionIsNotStored(t *testing.T) {
	a := quick()
	if err := a.Answer(true); err == nil {
		t.Error("a yes was accepted with nothing waiting for one")
	}
	g := &agent1{a: a}
	out := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 7) })
	waitPending(t, a)
	if derr := waitFor(t, out); derr == nil {
		t.Error("the pairing that followed was let through by an earlier yes")
	}
}

// The legacy paths are refused rather than answered with a constant. Break
// this and the agent invents a PIN - "0000" is what every example uses - which
// is auto-accept wearing a number.
func TestLegacyPinAndPasskeyAreRefused(t *testing.T) {
	g := &agent1{a: quick()}
	if pin, derr := g.RequestPinCode(phone); derr == nil || pin != "" {
		t.Errorf("RequestPinCode answered %q, %v", pin, derr)
	}
	if key, derr := g.RequestPasskey(phone); derr == nil || key != 0 {
		t.Errorf("RequestPasskey answered %d, %v", key, derr)
	}
}

// A passkey to type on the other device is shown and returns at once: that is
// how a keyboard pairs. Break this by treating it as a question and bluetoothd
// waits for an answer nobody can give, so no keyboard ever pairs.
func TestAPasskeyToTypeIsShownAndNeedsNoAnswer(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	if derr := g.DisplayPasskey(phone, 42, 0); derr != nil {
		t.Fatalf("showing a passkey answered %v", derr)
	}
	req, waiting := a.Pending()
	if !waiting || req.Kind != KindDisplay || req.Passkey != "000042" {
		t.Errorf("question = %+v, waiting = %v", req, waiting)
	}
	// There is no yes or no to give it: the answer is typing it on the device.
	if err := a.Answer(true); err == nil {
		t.Error("a passkey to type accepted a yes")
	}
	// And it ends when the attempt does, rather than holding the slot.
	a.Clear()
	if _, waiting := a.Pending(); waiting {
		t.Error("the passkey stayed on screen after the attempt ended")
	}
}

// bluetoothd giving up takes the question with it. Break this and a passkey
// stays on the screen for a pairing that is over, and holds the slot against
// the next real one.
func TestCancelDropsTheQuestion(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	out := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 9) })
	waitPending(t, a)

	if derr := g.Cancel(); derr != nil {
		t.Fatal(derr)
	}
	if _, waiting := a.Pending(); waiting {
		t.Error("the question outlived the pairing it belonged to")
	}
	// The call it belonged to still ends in a refusal, because nothing ever
	// said yes to it.
	if derr := waitFor(t, out); derr == nil {
		t.Error("a cancelled pairing was accepted")
	}
}

// What goes on the bus is the Agent1 interface and nothing else.
//
// The reasoning is internal/attn's, and so is the failure it prevents:
// whatever the connection is handed has every one of its exported methods
// published, so exporting the Agent itself would put Answer on the bus - and
// then anything that can reach this object could say yes to its own pairing.
func TestOnlyTheAgentSpecIsOnTheBus(t *testing.T) {
	want := map[string]bool{
		"Release":              true,
		"RequestPinCode":       true,
		"DisplayPinCode":       true,
		"RequestPasskey":       true,
		"DisplayPasskey":       true,
		"RequestConfirmation":  true,
		"RequestAuthorization": true,
		"AuthorizeService":     true,
		"Cancel":               true,
	}
	typ := reflect.TypeOf(&agent1{})
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if !want[name] {
			t.Errorf("%s is exported to bluetoothd and is not part of Agent1", name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("%s is missing from the agent on the bus", name)
	}
}

// The capability zde registers with. DisplayYesNo means "there is a person
// here and they will be asked"; NoInputNoOutput, which is what every short
// example uses, means "accept whatever asks".
func TestTheCapabilityIsDisplayYesNo(t *testing.T) {
	if Capability != "DisplayYesNo" {
		t.Errorf("capability = %q, which is not a person being asked", Capability)
	}
}
