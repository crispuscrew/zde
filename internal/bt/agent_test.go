package bt

import (
	"reflect"
	"strings"
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

func outcome(t *testing.T, out chan *dbus.Error) *dbus.Error {
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

	if err := a.Answer(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if derr := outcome(t, out); derr != nil {
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

	derr := outcome(t, out)
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
	req := waitPending(t, a)

	if err := a.Answer(req.ID, false); err != nil {
		t.Fatal(err)
	}
	derr := outcome(t, out)
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
	if derr := outcome(t, out); derr == nil {
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
	// In words, not only as a number: 0000110b is headphones, and nobody can
	// answer a question about 0000110b.
	if req.Kind != KindService || req.UUID == "" || req.Service == "" {
		t.Errorf("question = %+v, want the service named", req)
	}
	if derr := outcome(t, out); derr == nil {
		t.Error("nobody answered and the service was allowed")
	}
}

// One question at a time. Break this and two requests stack up, which is a
// dialog to click through - and the second one is the one nobody reads.
func TestASecondQuestionIsRefusedWhileOneWaits(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	first := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 1) })
	standing := waitPending(t, a)

	other := dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF")
	if derr := g.RequestConfirmation(other, 2); derr == nil {
		t.Error("a second pairing question was accepted while one was waiting")
	}
	// And the first one is untouched: it is still the question, and still the
	// one an answer belongs to.
	if req, _ := a.Pending(); req.Device != "44:5C:E9:1A:2B:3C" {
		t.Errorf("the waiting question became %+v", req)
	}
	a.Answer(standing.ID, true)
	if derr := outcome(t, first); derr != nil {
		t.Errorf("the first question was answered yes and got %v", derr)
	}
}

// A yes with nothing to say it to is refused rather than kept. Break this and
// a stray confirmation is spent on whatever asks next, which is precisely the
// pairing nobody looked at.
func TestAnswerBeforeTheQuestionIsNotStored(t *testing.T) {
	a := quick()
	if err := a.Answer("1", true); err == nil {
		t.Error("a yes was accepted with nothing waiting for one")
	}
	g := &agent1{a: a}
	out := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 7) })
	waitPending(t, a)
	if derr := outcome(t, out); derr == nil {
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
	if err := a.Answer(req.ID, true); err == nil {
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
	if derr := outcome(t, out); derr == nil {
		t.Error("a cancelled pairing was accepted")
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

// An answer is spent on the question it names, and on nothing else.
//
// This is the attack the id exists for, and it needs nobody to do anything
// wrong. Your phone's question is on the screen; bluetoothd cancels it, or it
// simply reaches its 45 seconds; a stranger's device asks in the same second;
// the person - still reading the first passkey - presses y. With one slot and a
// bare yes, that y pairs the stranger. Break this and that is the behaviour
// again, and it looks like nothing at all from the outside.
func TestAnAnswerIsSpentOnTheQuestionItNames(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	mine := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 4291) })
	first := waitPending(t, a)

	// The first question goes away on its own, the way bluetoothd's Cancel or
	// the wait running out would take it.
	g.Cancel()
	if derr := outcome(t, mine); derr == nil {
		t.Fatal("a cancelled pairing was accepted")
	}

	stranger := dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF")
	theirs := asked(func() *dbus.Error { return g.RequestConfirmation(stranger, 999999) })
	second := waitPending(t, a)
	if second.ID == first.ID {
		t.Fatal("two questions in a row got the same id, so naming one names both")
	}

	// The keystroke that was meant for the first question.
	if err := a.Answer(first.ID, true); err == nil {
		t.Error("an answer for a question that is gone was accepted")
	}
	if derr := outcome(t, theirs); derr == nil {
		t.Error("the stranger's pairing was let through by an answer meant for another question")
	}
}

// The question the person is looking at can still be answered by name.
func TestTheWaitingQuestionIsAnsweredByName(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	out := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 1) })
	req := waitPending(t, a)
	if err := a.Answer(req.ID, true); err != nil {
		t.Fatal(err)
	}
	if derr := outcome(t, out); derr != nil {
		t.Errorf("the question that was answered yes got %v", derr)
	}
}

// A passkey being typed expires like everything else. Break this and a keyboard
// carried out of range mid-pairing leaves the one slot taken for the rest of
// the session: every later question, including a device pairing to this
// machine, is refused for being second, and nobody is ever asked.
func TestAPasskeyOnTheScreenExpires(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	if derr := g.DisplayPasskey(phone, 42, 0); derr != nil {
		t.Fatal(derr)
	}
	waitPending(t, a)

	gone := false
	for i := 0; i < 200; i++ {
		if _, waiting := a.Pending(); !waiting {
			gone = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !gone {
		t.Fatal("the passkey is still on the screen long after the question died")
	}
	// And the slot is free for a real one.
	out := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 7) })
	req := waitPending(t, a)
	if err := a.Answer(req.ID, false); err != nil {
		t.Fatal(err)
	}
	if derr := outcome(t, out); derr == nil {
		t.Error("the question after the expired passkey was accepted")
	}
}

// The same passkey arriving again is the same question, with more of it.
//
// bluetoothd sends DisplayPasskey once per digit the other device takes. Break
// this and every digit after the first is refused as a second question, which
// is the pairing that is going well being told no - and the count, which is the
// only sign that anybody is typing at all, is thrown away.
func TestTheSamePasskeyAgainIsTheSameQuestion(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	if derr := g.DisplayPasskey(phone, 42, 0); derr != nil {
		t.Fatal(derr)
	}
	first := waitPending(t, a)
	for _, typed := range []uint16{1, 2, 3} {
		if derr := g.DisplayPasskey(phone, 42, typed); derr != nil {
			t.Fatalf("digit %d of the passkey was refused: %v", typed, derr)
		}
	}
	req, waiting := a.Pending()
	if !waiting || req.ID != first.ID {
		t.Errorf("question = %+v, want the one already on the screen (%s)", req, first.ID)
	}
	if req.Entered != 3 {
		t.Errorf("entered = %d, want the count bluetoothd sent", req.Entered)
	}
	// A different device is still a second question, and still refused.
	other := dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF")
	if derr := g.DisplayPasskey(other, 99, 0); derr == nil {
		t.Error("another device's passkey took the slot from the one being typed")
	}
}

// A locally started pairing takes down its own passkey and nothing else.
//
// Break this and a `zde system bluetooth pair` that fails in a second removes
// an incoming question mid-read - a device pairing to this machine, which
// nothing here started - while bluetoothd stays blocked on it for its own 45
// seconds with nobody able to answer it any more.
func TestClearingAPairingLeavesSomebodyElsesQuestion(t *testing.T) {
	a := quick()
	g := &agent1{a: a}
	out := asked(func() *dbus.Error { return g.RequestConfirmation(phone, 4291) })
	req := waitPending(t, a)

	// An attempt for another device ending is not this question ending.
	a.ClearShown("AA:BB:CC:DD:EE:FF")
	if _, waiting := a.Pending(); !waiting {
		t.Fatal("somebody else's pairing attempt took the question off the screen")
	}
	// Nor is an attempt for this device: a question with a yes and a no on it
	// ends when it is answered, and this one has not been.
	a.ClearShown(req.Device)
	if _, waiting := a.Pending(); !waiting {
		t.Error("a confirmation the person was reading was cleared by a pairing attempt")
	}
	a.Answer(req.ID, false)
	outcome(t, out)

	// The passkey this machine put on the screen is the one it takes down.
	if derr := g.DisplayPasskey(phone, 42, 0); derr != nil {
		t.Fatal(derr)
	}
	a.ClearShown("44:5C:E9:1A:2B:3C")
	if _, waiting := a.Pending(); waiting {
		t.Error("the passkey outlived the attempt that put it there")
	}
}

// What a device is asking for, in words. Break this and the question reads
// "0000110b-0000-1000-8000-00805f9b34fb wants authorising", which nobody can
// answer - so they say yes, which is the habit this surface exists to not build.
func TestAServiceQuestionSaysWhatTheServiceIs(t *testing.T) {
	if got := serviceName("0000110B-0000-1000-8000-00805f9b34fb"); got == "" {
		t.Error("headphones are not named")
	}
	// The one worth reading twice: a device asking to be a keyboard is asking to
	// type into anything the session has open.
	if got := serviceName("00001124-0000-1000-8000-00805f9b34fb"); !strings.Contains(got, "keyboard") {
		t.Errorf("the HID profile reads %q", got)
	}
	// Anything else falls back to the raw UUID rather than inventing a name.
	for _, uuid := range []string{
		"", "not-a-uuid", "0000ffff-0000-1000-8000-00805f9b34fb",
		"12345678-1234-1234-1234-123456789abc",
	} {
		if got := serviceName(uuid); got != "" {
			t.Errorf("serviceName(%q) = %q, want nothing invented", uuid, got)
		}
	}
}

// What bluetoothd sees on the bus: the wrapper, at the path the agent manager
// is told about.
//
// Reflection over the Go type cannot show this. Handing Export the Agent itself
// would compile and register and look right, and godbus would find no method
// whose last return is *dbus.Error - so it would export nothing at all, every
// call from bluetoothd would fail, and no pairing would ever ask anybody.
func TestTheAgentGoesOnTheBusAsTheWrapper(t *testing.T) {
	e := &fakeExporter{}
	a := NewAgent()
	if err := exportAgent(e, a); err != nil {
		t.Fatal(err)
	}
	wrapper, ok := e.v.(*agent1)
	if !ok {
		t.Fatalf("exported %T, want the Agent1 wrapper", e.v)
	}
	if wrapper.a != a {
		t.Error("the object on the bus is not backed by the agent that was handed over")
	}
	if e.path != AgentPath || e.iface != agentIface {
		t.Errorf("exported at %q as %q", e.path, e.iface)
	}
}

// fakeExporter is a bus connection's Export and nothing else.
type fakeExporter struct {
	v     any
	path  dbus.ObjectPath
	iface string
}

func (f *fakeExporter) Export(v any, path dbus.ObjectPath, iface string) error {
	f.v, f.path, f.iface = v, path, iface
	return nil
}

// The interface as bluetoothd sees it: the method names, their argument types,
// and the *dbus.Error that makes godbus export them at all.
//
// The names alone are not the surface. A method whose argument becomes a uint64
// no longer matches Agent1's "ou" and every confirmation dies with a signature
// error; one whose last return is not *dbus.Error is silently not exported;
// one renamed is a call bluetoothd makes into nothing. All three compile, and
// all three are a pairing that never asks anybody.
func TestTheAgentMatchesBluezsInterface(t *testing.T) {
	// method -> the D-Bus signature of its arguments, then of its results.
	want := map[string][2]string{
		"Release":              {"", ""},
		"RequestPinCode":       {"o", "s"},
		"DisplayPinCode":       {"os", ""},
		"RequestPasskey":       {"o", "u"},
		"DisplayPasskey":       {"ouq", ""},
		"RequestConfirmation":  {"ou", ""},
		"RequestAuthorization": {"o", ""},
		"AuthorizeService":     {"os", ""},
		"Cancel":               {"", ""},
	}
	typ := reflect.TypeOf(&agent1{})
	errType := reflect.TypeOf(&dbus.Error{})
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		spec, known := want[m.Name]
		if !known {
			t.Errorf("%s is on the bus for bluetoothd to call and is not part of Agent1", m.Name)
			continue
		}
		delete(want, m.Name)
		// The last return is what godbus binds on: without it the method is not
		// exported and bluetoothd's call fails with UnknownMethod.
		if n := m.Type.NumOut(); n == 0 || m.Type.Out(n-1) != errType {
			t.Errorf("%s does not end in *dbus.Error, so godbus would not export it", m.Name)
			continue
		}
		if got := signatureOf(argsOf(m.Type, true)); got != spec[0] {
			t.Errorf("%s takes %q, and Agent1 says %q", m.Name, got, spec[0])
		}
		if got := signatureOf(argsOf(m.Type, false)); got != spec[1] {
			t.Errorf("%s answers %q, and Agent1 says %q", m.Name, got, spec[1])
		}
	}
	for name := range want {
		t.Errorf("%s is missing, so bluetoothd would call into nothing", name)
	}
}

// argsOf is a method's arguments without the receiver, or its results without
// the trailing *dbus.Error: the parts that become a D-Bus signature.
func argsOf(t reflect.Type, in bool) []reflect.Type {
	var out []reflect.Type
	if in {
		for i := 1; i < t.NumIn(); i++ {
			out = append(out, t.In(i))
		}
		return out
	}
	for i := 0; i < t.NumOut()-1; i++ {
		out = append(out, t.Out(i))
	}
	return out
}

func signatureOf(types []reflect.Type) string {
	var b strings.Builder
	for _, t := range types {
		b.WriteString(dbus.SignatureOfType(t).String())
	}
	return b.String()
}
