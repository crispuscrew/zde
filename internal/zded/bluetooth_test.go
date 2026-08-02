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
	dead  bool
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
func (f *fakeRadio) Answer(id string, yes bool) error {
	f.note(boolCall("answer "+id, yes))
	return nil
}
func (f *fakeRadio) Close() error { f.note("close"); return nil }

// Alive is the connection behind the radio still being there. A fake has none
// to lose, so it says so until a test says otherwise.
func (f *fakeRadio) Alive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead
}

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
		{Method: "bluetooth.confirm", Args: []string{"7", "yes"}},
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

// A daemon that has been asked one bluetooth question stops when it is told to,
// even while the bus it is connecting to has said nothing at all.
//
// This is the whole of the SIGTERM defect, on a scratch socket. Break it - one
// mutex over both the field and the dial, which is what this was - and Close
// sits in Mutex.Lock behind somebody else's round trip. Measured against a
// socket that accepts and never speaks: the signal handler never got past the
// lock, zded went on answering queue.list after SIGTERM, left its socket file
// behind, and only SIGKILL ended it. On a real machine that is a ninety second
// logout while systemd waits.
func TestClosingDoesNotWaitForABusThatWillNotAnswer(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	dialling := make(chan struct{})
	hang := make(chan struct{})
	defer close(hang)
	s.openBluetooth = func() (Bluetooth, error) {
		close(dialling)
		<-hang
		return nil, errors.New("the bus never spoke")
	}
	path := socketPath(t)
	if err := s.Listen(path); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve() }()

	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// This one never comes back: it is inside the dial for as long as the test
	// holds it there, which is what a bus that accepts and says nothing does.
	go c.Call("bluetooth.state", nil) //nolint:errcheck // it is not meant to answer
	<-dialling

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited for the bus, so a session ending waits for it too")
	}
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("the listener outlived Close")
	}
	// And nothing is answering there any more, which is the part somebody
	// watching a logout would actually notice.
	if late, err := DialPath(path); err == nil {
		late.Close()
		t.Error("zded went on answering its socket after Close")
	}
}

// A radio that arrives after the session has ended belongs to nobody, so it is
// closed rather than kept. Break this and the connection a slow dial finally
// opens is stored into a daemon that has stopped, with a pairing agent exported
// on it - which is the leak closeRadio exists to prevent, reached by the one
// path that goes around it.
func TestARadioThatArrivesAfterTheCloseIsGivenUp(t *testing.T) {
	r := &fakeRadio{}
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	dialling := make(chan struct{})
	release := make(chan struct{})
	s.openBluetooth = func() (Bluetooth, error) {
		r.mu.Lock()
		r.opens++
		r.mu.Unlock()
		close(dialling)
		<-release
		return r, nil
	}

	asked := make(chan Response, 1)
	go func() { asked <- s.Dispatch(Request{Method: "bluetooth.scan", Args: []string{"on"}}) }()
	<-dialling
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)

	if resp := <-asked; resp.Error == "" {
		t.Error("a verb answered through a radio the daemon had already given up")
	}
	if !r.did("close") {
		t.Errorf("the late radio was kept: %v", r.calls)
	}
	if r.did("discover on") {
		t.Errorf("the verb ran on a connection that belonged to a session that had ended: %v", r.calls)
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
		{Request{Method: "bluetooth.confirm", Args: []string{"7", "yes"}}, "answer 7 on"},
		{Request{Method: "bluetooth.confirm", Args: []string{"7", "no"}}, "answer 7 off"},
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
		{Method: "bluetooth.confirm", Args: []string{"yes"}},
		{Method: "bluetooth.confirm", Args: []string{"7"}},
		{Method: "bluetooth.confirm", Args: []string{"7", "maybe"}},
		{Method: "bluetooth.confirm", Args: []string{"7", "on"}},
		{Method: "bluetooth.scan", Args: []string{"sometimes"}},
		{Method: "bluetooth.pair"},
		{Method: "bluetooth.pair", Args: []string{"one", "two"}},
		{Method: "bluetooth.scan", Args: []string{"yes"}},
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

// A radio whose connection has died is dropped and dialled again. Break this
// and a system bus restart costs the session every bluetooth verb until the
// next login - with the pairing agent gone from the bus and nothing to put it
// back, so a device pairing to this machine reaches nobody at all.
func TestADeadRadioIsDroppedAndDialledAgain(t *testing.T) {
	r := &fakeRadio{}
	s := withRadio(r)
	if resp := s.Dispatch(Request{Method: "bluetooth.state"}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	r.mu.Lock()
	r.dead = true
	r.mu.Unlock()

	if resp := s.Dispatch(Request{Method: "bluetooth.state"}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opens != 2 {
		t.Errorf("opens = %d, want a second dial after the connection died", r.opens)
	}
	// And the dead one was let go of rather than leaked.
	closed := 0
	for _, c := range r.calls {
		if c == "close" {
			closed++
		}
	}
	if closed != 1 {
		t.Errorf("the dead connection was closed %d times, want once", closed)
	}
}
