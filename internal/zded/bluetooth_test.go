package zded

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/bt"
)

// fakeRadio is bluetooth without a radio: it writes down what it was asked to
// do, which is what the dispatcher's job comes down to.
type fakeRadio struct {
	st bt.State

	mu    sync.Mutex
	calls []string
	opens int
}

func (f *fakeRadio) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeRadio) did(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}

func (f *fakeRadio) State() (bt.State, error)  { return f.st, nil }
func (f *fakeRadio) Power(on bool) error       { f.note(boolCall("power", on)); return nil }
func (f *fakeRadio) Discover(on bool) error    { f.note(boolCall("discover", on)); return nil }
func (f *fakeRadio) Pair(a string) error       { f.note("pair " + a); return nil }
func (f *fakeRadio) Connect(a string) error    { f.note("connect " + a); return nil }
func (f *fakeRadio) Disconnect(a string) error { f.note("disconnect " + a); return nil }
func (f *fakeRadio) Forget(a string) error     { f.note("forget " + a); return nil }
func (f *fakeRadio) Trust(a string, yes bool) error {
	f.note(boolCall("trust "+a, yes))
	return nil
}
func (f *fakeRadio) Answer(yes bool) error { f.note(boolCall("answer", yes)); return nil }
func (f *fakeRadio) Close() error          { f.note("close"); return nil }

func boolCall(what string, on bool) string {
	if on {
		return what + " on"
	}
	return what + " off"
}

// withRadio is a daemon whose bluetooth is the fake, counting how often it was
// opened.
func withRadio(r *fakeRadio) *Server {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	s.openBluetooth = func() (Bluetooth, error) {
		r.mu.Lock()
		r.opens++
		r.mu.Unlock()
		return r, nil
	}
	return s
}

// A machine with no bluetooth answers the question rather than failing it.
// Break this and every desktop with the radio off gets an error out of the one
// verb that exists to say there is no radio - and the surface draws nothing at
// all instead of "no adapter".
func TestBluetoothStateWithNoRadioIsStillAnAnswer(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	s.openBluetooth = func() (Bluetooth, error) {
		return nil, errors.New("system bus: no such file")
	}
	resp := s.Dispatch(Request{Method: "bluetooth.state"})
	if resp.Error != "" {
		t.Fatalf("bluetooth.state on a machine with no bus: %s", resp.Error)
	}
	var st bt.State
	if err := json.Unmarshal(resp.Ok, &st); err != nil {
		t.Fatal(err)
	}
	if st.Adapter.Present {
		t.Error("no bus and the adapter reads present")
	}
	if !strings.Contains(st.Adapter.Why, "system bus") {
		t.Errorf("why = %q, want what actually went wrong", st.Adapter.Why)
	}
}

// Asking to pair on a machine with no radio is a refusal. Break this and the
// key reports success for something that cannot have happened, which is worse
// than an error.
func TestBluetoothActionsRefuseWithNoRadio(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	s.openBluetooth = func() (Bluetooth, error) { return nil, errors.New("no bluetoothd") }
	for _, m := range []Request{
		{Method: "bluetooth.pair", Args: []string{"44:5C:E9:1A:2B:3C"}},
		{Method: "bluetooth.scan", Args: []string{"on"}},
		{Method: "bluetooth.confirm", Args: []string{"yes"}},
	} {
		resp := s.Dispatch(m)
		if resp.Error == "" {
			t.Errorf("%s worked on a machine with no radio", m.Method)
		}
	}
}

// The radio is opened once and kept. Break this and every call registers a
// pairing agent and drops it again, so a device pairing to this machine
// between two keypresses reaches nobody.
func TestTheRadioIsOpenedOnceAndKept(t *testing.T) {
	r := &fakeRadio{}
	s := withRadio(r)
	for i := 0; i < 3; i++ {
		if resp := s.Dispatch(Request{Method: "bluetooth.state"}); resp.Error != "" {
			t.Fatal(resp.Error)
		}
	}
	s.Dispatch(Request{Method: "bluetooth.scan", Args: []string{"on"}})
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opens != 1 {
		t.Errorf("the radio was opened %d times, want once", r.opens)
	}
}

// Two questions at once dial once between them, and the second waits for the
// first rather than opening a connection of its own.
//
// Break this - release the lock before the dial, which is what it used to do -
// and both callers dial, both export a pairing agent and both register one.
// BlueZ keys agents by the sender's unique name with the path, so two
// connections are two agents and neither registration is refused; whichever one
// is closed afterwards may be the one that won RequestDefaultAgent, leaving an
// agent that is registered and is not the default. Nothing here fails visibly
// after that: pairing from this machine still works, and a phone pairing to it
// is refused by BlueZ with no question reaching anybody.
func TestTwoQuestionsAtOnceDialOnce(t *testing.T) {
	r := &fakeRadio{}
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	// A dial that announces itself and then waits, which is what a real one is:
	// a bus connection, an Export and two calls to bluetoothd.
	dialling := make(chan struct{}, 4)
	release := make(chan struct{})
	s.openBluetooth = func() (Bluetooth, error) {
		r.mu.Lock()
		r.opens++
		r.mu.Unlock()
		dialling <- struct{}{}
		<-release
		return r, nil
	}

	done := make(chan Response, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- s.Dispatch(Request{Method: "bluetooth.state"}) }()
	}
	<-dialling // one of them is inside the dial
	// And the other one is not, and will not be: it is waiting on the lock.
	// Generous, because what is being watched for is a goroutine that has to be
	// scheduled to fail the test.
	select {
	case <-dialling:
		t.Fatal("both callers dialled, so both registered a pairing agent")
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		if resp := <-done; resp.Error != "" {
			t.Fatal(resp.Error)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opens != 1 {
		t.Errorf("the radio was dialled %d times, want once", r.opens)
	}
}

// The session ending gives the radio up. Break this and a bus connection with
// an exported pairing agent outlives the daemon: bluetoothd goes on calling an
// object nothing can answer through, and every question times out into a
// refusal nobody was asked for.
func TestClosingTheDaemonGivesUpTheRadio(t *testing.T) {
	r := &fakeRadio{}
	s := withRadio(r)
	if resp := s.Dispatch(Request{Method: "bluetooth.state"}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !r.did("close") {
		t.Errorf("the daemon closed and the radio was left open: %v", r.calls)
	}
	// And it is forgotten, not merely closed: a later question has to open a
	// new one rather than talking down a connection that is gone.
	if resp := s.Dispatch(Request{Method: "bluetooth.state"}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opens != 2 {
		t.Errorf("opens = %d, want a fresh one after the close", r.opens)
	}
}

// Each verb reaches the thing it names, and untrust is not trust. Break the
// trust pair and the one verb that revokes a standing permission does the
// opposite of what it says.
func TestBluetoothVerbsReachWhatTheyName(t *testing.T) {
	r := &fakeRadio{}
	s := withRadio(r)
	addr := "44:5C:E9:1A:2B:3C"
	for _, tc := range []struct {
		req  Request
		want string
	}{
		{Request{Method: "bluetooth.power", Args: []string{"on"}}, "power on"},
		{Request{Method: "bluetooth.scan", Args: []string{"off"}}, "discover off"},
		{Request{Method: "bluetooth.pair", Args: []string{addr}}, "pair " + addr},
		{Request{Method: "bluetooth.connect", Args: []string{addr}}, "connect " + addr},
		{Request{Method: "bluetooth.disconnect", Args: []string{addr}}, "disconnect " + addr},
		{Request{Method: "bluetooth.forget", Args: []string{addr}}, "forget " + addr},
		{Request{Method: "bluetooth.trust", Args: []string{addr}}, "trust " + addr + " on"},
		{Request{Method: "bluetooth.untrust", Args: []string{addr}}, "trust " + addr + " off"},
		{Request{Method: "bluetooth.confirm", Args: []string{"yes"}}, "answer on"},
		{Request{Method: "bluetooth.confirm", Args: []string{"no"}}, "answer off"},
	} {
		if resp := s.Dispatch(tc.req); resp.Error != "" {
			t.Fatalf("%s: %s", tc.req.Method, resp.Error)
		}
		if !r.did(tc.want) {
			t.Errorf("%s %v did not reach %q: %v", tc.req.Method, tc.req.Args, tc.want, r.calls)
		}
	}
}

// A verb given nonsense refuses rather than guessing. Break this and
// `bluetooth.confirm` with no word at all is read as one of the two answers -
// and the one it would be read as is yes.
func TestBluetoothVerbsRefuseWhatTheyCannotRead(t *testing.T) {
	r := &fakeRadio{}
	s := withRadio(r)
	for _, req := range []Request{
		{Method: "bluetooth.state", Args: []string{"extra"}},
		{Method: "bluetooth.confirm"},
		{Method: "bluetooth.confirm", Args: []string{"maybe"}},
		{Method: "bluetooth.scan", Args: []string{"sometimes"}},
		{Method: "bluetooth.pair"},
		{Method: "bluetooth.pair", Args: []string{"one", "two"}},
	} {
		if resp := s.Dispatch(req); resp.Error == "" {
			t.Errorf("%s %v was accepted", req.Method, req.Args)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) != 0 {
		t.Errorf("a refused request reached the radio anyway: %v", r.calls)
	}
}

// The question waiting crosses the socket, or no surface can draw it and no
// terminal can print it - which would leave the passkey somewhere only the
// daemon can see.
func TestTheWaitingQuestionCrossesTheSocket(t *testing.T) {
	r := &fakeRadio{st: bt.State{
		Adapter: bt.Adapter{Present: true, Powered: true},
		Pending: &bt.Request{Device: "44:5C:E9:1A:2B:3C", Kind: bt.KindConfirm, Passkey: "004291"},
	}}
	s := withRadio(r)
	resp := s.Dispatch(Request{Method: "bluetooth.state"})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var st bt.State
	if err := json.Unmarshal(resp.Ok, &st); err != nil {
		t.Fatal(err)
	}
	if st.Pending == nil || st.Pending.Passkey != "004291" {
		t.Errorf("state = %+v, want the passkey that is waiting", st)
	}
}
