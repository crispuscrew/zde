package bt

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeBus is the bus without a radio behind it: it answers with the objects it
// was given and writes down every call that went out, which is what the tests
// below are actually about - which object a verb reaches, and which calls a
// verb does not make.
type fakeBus struct {
	objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err  error

	mu     sync.Mutex
	dead   bool
	calls  []string
	bounds []time.Duration // the deadline each call carried, in the same order
	fail   map[string]error
	// held is a call that has been taken and not answered, which is what a
	// bluetoothd waiting on a device's radio looks like from here.
	held map[string]chan struct{}
}

// holdOn makes any call whose text contains this wait for the channel.
func (f *fakeBus) holdOn(substr string, until chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held == nil {
		f.held = map[string]chan struct{}{}
	}
	f.held[substr] = until
}

// waitIfHeld blocks a recorded call for as long as the test wants it blocked.
func (f *fakeBus) waitIfHeld(line string) {
	f.mu.Lock()
	var on chan struct{}
	for substr, ch := range f.held {
		if strings.Contains(line, substr) {
			on = ch
			break
		}
	}
	f.mu.Unlock()
	if on != nil {
		<-on
	}
}

// Managed is recorded like the rest, because "it never reached the bus" is a
// claim about reading it too: an address that is not one should be refused
// before anything is asked of anybody.
func (f *fakeBus) Managed(within time.Duration) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	f.record(within, "managed")
	if f.err != nil {
		return nil, f.err
	}
	return f.objs, nil
}

func (f *fakeBus) Call(within time.Duration, path dbus.ObjectPath, method string, args ...any) error {
	line := "call " + string(path) + " " + method + argText(args)
	f.record(within, line)
	f.waitIfHeld(line)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail[method]
}

func (f *fakeBus) Set(within time.Duration, path dbus.ObjectPath, iface, prop string, value any) error {
	f.record(within, "set "+string(path)+" "+iface+"."+prop+argText([]any{value}))
	return nil
}

// dead is a bus whose connection has gone, the way it does when the system bus
// is restarted under a running daemon.
func (f *fakeBus) Alive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead
}

func (f *fakeBus) Close() error { return nil }

func (f *fakeBus) record(within time.Duration, line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, line)
	f.bounds = append(f.bounds, within)
}

// bound is the deadline the first call matching this text was given.
func (f *fakeBus) bound(substr string) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if strings.Contains(c, substr) {
			return f.bounds[i], true
		}
	}
	return 0, false
}

func argText(args []any) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString(" ")
		switch v := a.(type) {
		case string:
			b.WriteString(v)
		case dbus.ObjectPath:
			b.WriteString(string(v))
		case bool:
			if v {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		}
	}
	return b.String()
}

// made says whether a call like this one went out.
func (f *fakeBus) made(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// settled waits for the background call a verb started, so that what it did (or
// did not do) can be looked at.
func (f *fakeBus) settled(t *testing.T, c *Client, substr string) {
	t.Helper()
	for i := 0; i < 400; i++ {
		st, err := c.State()
		if err == nil && st.Doing == "" && f.made(substr) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s was ever made: %v", substr, f.calls)
}

// idle waits for whatever a verb started in the background to be over, so that
// the next one is not refused for arriving while the radio is busy.
func idle(t *testing.T, c *Client) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if st, err := c.State(); err == nil && st.Doing == "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the radio never stopped being busy")
}

const (
	hci0     = dbus.ObjectPath("/org/bluez/hci0")
	phonePth = dbus.ObjectPath("/org/bluez/hci0/dev_44_5C_E9_1A_2B_3C")
	budsPth  = dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_01")
	noisePth = dbus.ObjectPath("/org/bluez/hci0/dev_CC_CC_CC_CC_CC_CC")
)

func v(x any) dbus.Variant { return dbus.MakeVariant(x) }

// oneAdapter is a machine with a radio, a phone that is paired but not
// trusted, connected earbuds, and something nameless in the room.
func oneAdapter() *fakeBus {
	return &fakeBus{objs: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		hci0: {adapterIface: {
			"Powered": v(true), "Discovering": v(false),
			"Address": v("00:11:22:33:44:55"), "Alias": v("zdebox"),
		}},
		phonePth: {deviceIface: {
			"Address": v("44:5C:E9:1A:2B:3C"), "Alias": v("Ilya's phone"),
			"Paired": v(true), "Trusted": v(false), "Connected": v(false),
			"RSSI": v(int16(-67)),
		}},
		budsPth: {deviceIface: {
			"Address": v("AA:BB:CC:DD:EE:01"), "Alias": v("buds"),
			"Paired": v(true), "Trusted": v(true), "Connected": v(true),
		}},
		noisePth: {deviceIface: {
			"Address": v("CC:CC:CC:CC:CC:CC"),
			"Paired":  v(false), "RSSI": v(int16(-91)),
		}},
	}}
}

func client(b bus) *Client { return &Client{bus: b, agent: NewAgent()} }

// No bluetoothd is an answer, not an error. Break this and every desktop with
// the radio off gets a failure out of a verb that should say "no adapter" and
// exit - which reads as zde being broken rather than as bluetooth being off.
func TestNoBluetoothdIsAnAnswerNotAnError(t *testing.T) {
	b := &fakeBus{err: dbus.Error{
		Name: "org.freedesktop.DBus.Error.ServiceUnknown",
		Body: []any{"no such service"},
	}}
	st, err := client(b).State()
	if err != nil {
		t.Fatalf("a machine with no bluetoothd answered with an error: %v", err)
	}
	if st.Adapter.Present {
		t.Error("no bluetoothd and the adapter reads present")
	}
	if st.Adapter.Why == "" {
		t.Error("nothing says why there is no adapter")
	}
}

// A bus that is broken in some other way is still an error. Break this and a
// real fault reads as "you have no bluetooth", which is the wrong thing to go
// looking at.
func TestABrokenBusIsStillAnError(t *testing.T) {
	b := &fakeBus{err: errors.New("connection reset")}
	if _, err := client(b).State(); err == nil {
		t.Error("a broken bus was reported as a machine without bluetooth")
	}
}

// bluetoothd with no radio under it says so in its own words. Break this and a
// laptop whose adapter has gone looks exactly like a desktop that never had
// one.
func TestAnAdapterlessBluetoothdSaysWhich(t *testing.T) {
	b := &fakeBus{objs: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}}
	st, err := client(b).State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Adapter.Present || !strings.Contains(st.Adapter.Why, "no adapter") {
		t.Errorf("adapter = %+v", st.Adapter)
	}
}

// What one read of the bus turns into. Break this and the list says a device
// is paired when it is not, or loses the signal, which is the whole content of
// the surface.
func TestTheListIsWhatTheBusSaid(t *testing.T) {
	st, err := client(oneAdapter()).State()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Adapter.Present || !st.Adapter.Powered || st.Adapter.Name != "zdebox" {
		t.Errorf("adapter = %+v", st.Adapter)
	}
	if len(st.Devices) != 3 {
		t.Fatalf("devices = %+v", st.Devices)
	}
	var phone Device
	for _, d := range st.Devices {
		if d.Address == "44:5C:E9:1A:2B:3C" {
			phone = d
		}
	}
	if phone.Name != "Ilya's phone" || !phone.Paired || phone.Trusted || phone.Connected {
		t.Errorf("the phone reads %+v", phone)
	}
	if phone.RSSI != -67 {
		t.Errorf("signal = %d, want what the bus said", phone.RSSI)
	}
}

// The order is what you own first, and it does not move while a scan is
// running. Break this by sorting on the signal and row three is a different
// device every second, so the number under somebody's finger is not the thing
// they looked at.
func TestTheOrderDoesNotMoveWhileScanning(t *testing.T) {
	b := oneAdapter()
	st, err := client(b).State()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"AA:BB:CC:DD:EE:01", "44:5C:E9:1A:2B:3C", "CC:CC:CC:CC:CC:CC"}
	for i, addr := range want {
		if st.Devices[i].Address != addr {
			t.Fatalf("order = %+v, want connected, then paired, then the rest", st.Devices)
		}
	}
	// The signal changes constantly while discovery runs; the list must not.
	b.objs[noisePth][deviceIface]["RSSI"] = v(int16(-10))
	b.objs[phonePth][deviceIface]["RSSI"] = v(int16(-99))
	again, err := client(b).State()
	if err != nil {
		t.Fatal(err)
	}
	for i, addr := range want {
		if again.Devices[i].Address != addr {
			t.Fatalf("the list reordered when the signal changed: %+v", again.Devices)
		}
	}
}

// An address is checked before anything is done with it. Break this and a
// string somebody typed reaches a bus call, which is a way to address objects
// that are not devices at all.
func TestAnAddressThatIsNotOneNeverReachesTheBus(t *testing.T) {
	for _, bad := range []string{
		"", "hello", "44:5C:E9:1A:2B", "44:5C:E9:1A:2B:3C:4D",
		"../../org/bluez", "44_5C_E9_1A_2B_3C", "ZZ:5C:E9:1A:2B:3C",
	} {
		b := oneAdapter()
		if err := client(b).Connect(bad); err == nil {
			t.Errorf("%q was accepted as an address", bad)
		}
		// Nothing at all, not even the read that would look it up: an address
		// that cannot be one is refused where it is typed.
		b.mu.Lock()
		calls := len(b.calls)
		b.mu.Unlock()
		if calls != 0 {
			t.Errorf("%q reached the bus: %v", bad, b.calls)
		}
	}
	// And a real one in the wrong case is the same address, not a refusal.
	b := oneAdapter()
	if err := client(b).Connect("44:5c:e9:1a:2b:3c"); err != nil {
		t.Errorf("a lower case address was refused: %v", err)
	}
}

// A device the adapter has never heard of is a refusal that says what to do.
// Break this and pairing a typo hangs on an object path nobody owns.
func TestADeviceThatIsNotThereIsARefusal(t *testing.T) {
	err := client(oneAdapter()).Pair("11:22:33:44:55:66")
	if err == nil {
		t.Fatal("paired with a device that is not there")
	}
	if !strings.Contains(err.Error(), "scan") {
		t.Errorf("the refusal does not say how to find it: %v", err)
	}
}

// Pairing pairs and does nothing else. This is the one that has to hold: break
// it by setting Trusted after a successful pair - which is what every desktop
// bluetooth applet does - and a device agreed to once reconnects and uses
// services for ever without asking anybody again.
func TestPairingDoesNotTrust(t *testing.T) {
	b := oneAdapter()
	// Not paired yet, so this is the first time round for it.
	b.objs[phonePth][deviceIface]["Paired"] = v(false)
	c := client(b)
	if err := c.Pair("44:5C:E9:1A:2B:3C"); err != nil {
		t.Fatal(err)
	}
	b.settled(t, c, "Device1.Pair")

	b.mu.Lock()
	defer b.mu.Unlock()
	for _, call := range b.calls {
		if strings.Contains(call, "Trusted") {
			t.Errorf("pairing wrote trust: %q", call)
		}
	}
}

// Trust is its own decision, both ways. Break this and there is no way to stop
// being asked about a device you own - or no way to stop trusting one without
// forgetting it.
func TestTrustIsItsOwnDecision(t *testing.T) {
	b := oneAdapter()
	c := client(b)
	if err := c.Trust("44:5C:E9:1A:2B:3C", true); err != nil {
		t.Fatal(err)
	}
	if !b.made("set " + string(phonePth) + " org.bluez.Device1.Trusted true") {
		t.Errorf("trusting did not write the property: %v", b.calls)
	}
	if err := c.Trust("44:5C:E9:1A:2B:3C", false); err != nil {
		t.Fatal(err)
	}
	if !b.made("set " + string(phonePth) + " org.bluez.Device1.Trusted false") {
		t.Errorf("untrusting did not write the property: %v", b.calls)
	}
}

// Forgetting goes through the adapter, because afterwards there is no device
// object left to call. Break this and forget fails on every device, leaving
// the only way to revoke a pairing broken.
func TestForgetRemovesTheDeviceFromTheAdapter(t *testing.T) {
	b := oneAdapter()
	c := client(b)
	if err := c.Forget("44:5C:E9:1A:2B:3C"); err != nil {
		t.Fatal(err)
	}
	// Started rather than waited for, so the call is looked at once it has been
	// made and not the moment it was asked for.
	b.settled(t, c, "Adapter1.RemoveDevice")
	if !b.made("call " + string(hci0) + " org.bluez.Adapter1.RemoveDevice " + string(phonePth)) {
		t.Errorf("forget did not remove the device: %v", b.calls)
	}
}

// The two verbs that tear a link down answer at once and do the waiting behind
// it, the way pairing and connecting already did.
//
// Break this - wait for the call on the request goroutine, which is what these
// two used to do - and a device that has stopped answering holds that goroutine
// for the 75 seconds a long call is given, while the client on the other end of
// the socket gives up after 5. The person is told it failed by a daemon that is
// still doing it, and every later bluetooth question queues behind an answer
// nobody is left to read.
func TestDroppingALinkAnswersBeforeBlueZHasFinished(t *testing.T) {
	for _, tc := range []struct {
		what  string
		call  func(*Client) error
		made  string
		doing string
	}{
		{"disconnect", func(c *Client) error { return c.Disconnect("AA:BB:CC:DD:EE:01") },
			"Device1.Disconnect", "disconnecting AA:BB:CC:DD:EE:01"},
		{"forget", func(c *Client) error { return c.Forget("AA:BB:CC:DD:EE:01") },
			"Adapter1.RemoveDevice", "forgetting AA:BB:CC:DD:EE:01"},
	} {
		b := oneAdapter()
		// A BlueZ that has taken the call and is still thinking about it, which
		// is what a device whose radio has gone looks like from here.
		hold := make(chan struct{})
		b.holdOn(tc.made, hold)
		c := client(b)

		answered := make(chan error, 1)
		go func() { answered <- tc.call(c) }()
		select {
		case err := <-answered:
			if err != nil {
				t.Fatalf("%s: %v", tc.what, err)
			}
		case <-time.After(2 * time.Second):
			close(hold)
			t.Fatalf("%s waited for BlueZ, and the client gives up after 5 seconds", tc.what)
		}
		// And it is not silence: what the radio is doing is in State, so a
		// surface can say so while it happens.
		st, err := c.State()
		if err != nil {
			t.Fatal(err)
		}
		if st.Doing != tc.doing {
			t.Errorf("%s: doing = %q, want %q", tc.what, st.Doing, tc.doing)
		}
		close(hold)
		b.settled(t, c, tc.made)
	}
}

// Scanning needs the radio on, and says so rather than failing in BlueZ's
// words. Break this and "org.bluez.Error.NotReady" is what a person gets for
// pressing the key on a machine whose radio is off.
func TestScanningSaysWhenTheRadioIsOff(t *testing.T) {
	b := oneAdapter()
	b.objs[hci0][adapterIface]["Powered"] = v(false)
	err := client(b).Discover(true)
	if err == nil {
		t.Fatal("started a scan on a radio that is off")
	}
	if !strings.Contains(err.Error(), "power on") {
		t.Errorf("the refusal does not say how to turn it on: %v", err)
	}
	if b.made("StartDiscovery") {
		t.Error("the scan was started anyway")
	}
}

// Scanning starts and stops on the adapter. Break the stop half and the radio
// is left announcing for the rest of the session.
func TestScanningStartsAndStops(t *testing.T) {
	b := oneAdapter()
	c := client(b)
	if err := c.Discover(true); err != nil {
		t.Fatal(err)
	}
	if !b.made("call " + string(hci0) + " org.bluez.Adapter1.StartDiscovery") {
		t.Errorf("nothing started the scan: %v", b.calls)
	}
	if err := c.Discover(false); err != nil {
		t.Fatal(err)
	}
	if !b.made("call " + string(hci0) + " org.bluez.Adapter1.StopDiscovery") {
		t.Errorf("nothing stopped the scan: %v", b.calls)
	}
}

// The radio is turned on because somebody said so, not as a side effect.
// Break this and the property write goes to the wrong object, so the one
// switch that decides whether this machine is talking to the room does nothing.
func TestPowerWritesTheAdaptersOwnProperty(t *testing.T) {
	b := oneAdapter()
	if err := client(b).Power(true); err != nil {
		t.Fatal(err)
	}
	if !b.made("set " + string(hci0) + " org.bluez.Adapter1.Powered true") {
		t.Errorf("power did not reach the adapter: %v", b.calls)
	}
}

// The agent is offered to bluetoothd as a machine with a person at it. Break
// this - register with NoInputNoOutput, which is what the short examples use -
// and BlueZ stops asking anybody anything and pairs whatever is in range.
func TestTheAgentRegistersAsAPersonBeingAsked(t *testing.T) {
	b := oneAdapter()
	c := client(b)
	if err := c.register(); err != nil {
		t.Fatal(err)
	}
	if !b.made("org.bluez.AgentManager1.RegisterAgent " + string(AgentPath) + " DisplayYesNo") {
		t.Errorf("the agent registered as something else: %v", b.calls)
	}
	// And as the default, so that a device pairing to this machine reaches a
	// person too rather than whatever else is on the bus.
	if !b.made("org.bluez.AgentManager1.RequestDefaultAgent") {
		t.Errorf("the agent is registered and is not the default: %v", b.calls)
	}
}

// A pairing already under way is not restarted. Break this and two attempts
// run at once, the second question is refused for being second, and the person
// sees a refusal for the pairing they just asked for.
func TestOnePairingAtATime(t *testing.T) {
	b := oneAdapter()
	b.objs[phonePth][deviceIface]["Paired"] = v(false)
	block := make(chan struct{})
	b.mu.Lock()
	b.fail = map[string]error{}
	b.mu.Unlock()
	c := client(b)
	// A pair call that does not come back until the test lets it.
	slow := &blockingBus{fakeBus: b, block: block}
	c.bus = slow

	if err := c.Pair("44:5C:E9:1A:2B:3C"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200 && !b.made("Device1.Pair"); i++ {
		time.Sleep(time.Millisecond)
	}
	if err := c.Pair("44:5C:E9:1A:2B:3C"); err == nil {
		t.Error("a second pairing started while one was running")
	}
	close(block)
}

// blockingBus holds Pair open, the way a real one does while somebody is
// reading six digits off a phone.
type blockingBus struct {
	*fakeBus
	block chan struct{}
}

func (s *blockingBus) Call(within time.Duration, path dbus.ObjectPath, method string, args ...any) error {
	err := s.fakeBus.Call(within, path, method, args...)
	if strings.HasSuffix(method, ".Pair") {
		<-s.block
	}
	return err
}

// What a pairing said when it failed survives long enough to be read, since
// nothing was waiting on the call. Break this and a pairing that was refused by
// the other end looks exactly like one that was never asked for.
func TestAFailedPairingSaysWhy(t *testing.T) {
	b := oneAdapter()
	b.objs[phonePth][deviceIface]["Paired"] = v(false)
	b.fail = map[string]error{deviceIface + ".Pair": errors.New("AuthenticationFailed")}
	c := client(b)
	if err := c.Pair("44:5C:E9:1A:2B:3C"); err != nil {
		t.Fatal(err)
	}
	b.settled(t, c, "Device1.Pair")
	st, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Failed, "AuthenticationFailed") {
		t.Errorf("failed = %q, want what the other end said", st.Failed)
	}
}

// Every call carries a deadline, and the one that waits on a person carries a
// longer one than the question does.
//
// Two failures in one test because they are two halves of the same decision.
// Break the first - a call made with no bound, which is what
// Object.Call(method, 0, ...) is - and a bluetoothd wedged on a driver leaves a
// goroutine that never comes back, with every later bluetooth verb queued
// behind it while the CLI has already given up. Break the second by bounding
// Pair like a question - and zde cancels the pairing out from under somebody
// who is still reading the number off their phone, which is zde saying no on
// their behalf.
func TestEveryCallIsBoundedAndPairingOutlastsTheQuestion(t *testing.T) {
	b := oneAdapter()
	b.objs[phonePth][deviceIface]["Paired"] = v(false)
	c := client(b)
	if err := c.register(); err != nil {
		t.Fatal(err)
	}
	for _, do := range []func() error{
		func() error { _, err := c.State(); return err },
		func() error { return c.Power(true) },
		func() error { return c.Discover(true) },
		// The earbuds, because they are the paired one here: trust is refused
		// on a device that has not been agreed to once.
		func() error { return c.Trust("AA:BB:CC:DD:EE:01", true) },
		func() error { return c.Disconnect("44:5C:E9:1A:2B:3C") },
		func() error { return c.Forget("44:5C:E9:1A:2B:3C") },
		func() error { return c.Pair("44:5C:E9:1A:2B:3C") },
	} {
		if err := do(); err != nil {
			t.Fatal(err)
		}
		// Three of these are started rather than waited for, and the radio does
		// one thing at a time: without this the next one is refused for arriving
		// while the last is still going.
		idle(t, c)
	}
	b.settled(t, c, "Device1.Pair")

	b.mu.Lock()
	defer b.mu.Unlock()
	for i, call := range b.calls {
		if b.bounds[i] <= 0 {
			t.Errorf("%q was made with no deadline", call)
		}
		if b.bounds[i] > waitFor {
			t.Errorf("%q was given %s, which is longer than anything here waits", call, b.bounds[i])
		}
	}
	// The one that waits on a person outlasts the question it is waiting for.
	pair, made := 0*time.Second, false
	for i, call := range b.calls {
		if strings.Contains(call, "Device1.Pair") {
			pair, made = b.bounds[i], true
		}
	}
	if !made {
		t.Fatalf("no pairing call was made: %v", b.calls)
	}
	if pair <= AnswerWait {
		t.Errorf("pairing is bounded at %s and the question it waits for stands for %s", pair, AnswerWait)
	}
	// And the rest are the short bound, or a wedged bluetoothd holds a keypress.
	for i, call := range b.calls {
		if strings.Contains(call, "Device1.Pair") ||
			strings.Contains(call, "Disconnect") || strings.Contains(call, "RemoveDevice") {
			continue
		}
		if b.bounds[i] != askFor {
			t.Errorf("%q was given %s, want the short bound %s", call, b.bounds[i], askFor)
		}
	}
}

// The question a person is being asked names the device rather than only its
// address. Break this and the surface shows six hex pairs, which nobody
// recognises as their own phone.
func TestTheQuestionCarriesTheDevicesName(t *testing.T) {
	b := oneAdapter()
	c := client(b)
	g := &agent1{a: c.agent}
	go g.RequestConfirmation(phonePth, 4291)
	for i := 0; i < 200; i++ {
		if _, ok := c.agent.Pending(); ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	st, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Pending == nil {
		t.Fatal("the state does not carry the question that is waiting")
	}
	if st.Pending.Name != "Ilya's phone" || st.Pending.Passkey != "004291" {
		t.Errorf("pending = %+v", st.Pending)
	}
	// And it carries the name the answer has to give back.
	if st.Pending.ID == "" {
		t.Error("the question that crossed the socket has no id to answer")
	}
	c.agent.Answer(st.Pending.ID, false)
}

// A device names itself, and the name is drawn on a surface with a question on
// it. Break this and a device called "\n  0000 matches, allow" writes a line of
// its own above the real one, or moves the cursor with an escape sequence and
// paints over it - and the same name injects rows into the tab separated list
// the CLI prints. The device is still perfectly usable by its address, which is
// why refusing the name costs nothing.
func TestARemoteNameCannotForgeALine(t *testing.T) {
	for _, forged := range []string{
		"line\nbreak",
		"tab\tseparated",
		"\x1b[1A\x1b[2Kpasskey 004291 matches",
		"bell\a",
		"zero​width",
	} {
		b := oneAdapter()
		b.objs[phonePth][deviceIface]["Alias"] = v(forged)
		st, err := client(b).State()
		if err != nil {
			t.Fatal(err)
		}
		var phone Device
		for _, d := range st.Devices {
			if d.Address == "44:5C:E9:1A:2B:3C" {
				phone = d
			}
		}
		if phone.Address == "" {
			t.Fatalf("%q took the device out of the list entirely", forged)
		}
		if phone.Name != "" {
			t.Errorf("name %q reached the surface as %q", forged, phone.Name)
		}
	}
	// An ordinary name, including one that is not English, is left alone.
	for _, ordinary := range []string{"Ilya's phone", "Наушники", "WH-CH720N"} {
		b := oneAdapter()
		b.objs[phonePth][deviceIface]["Alias"] = v(ordinary)
		st, err := client(b).State()
		if err != nil {
			t.Fatal(err)
		}
		kept := false
		for _, d := range st.Devices {
			if d.Name == ordinary {
				kept = true
			}
		}
		if !kept {
			t.Errorf("an ordinary name %q was thrown away", ordinary)
		}
	}
	// And a name too long to be a name is cut rather than allowed to push the
	// question off the top of a terminal.
	b := oneAdapter()
	b.objs[phonePth][deviceIface]["Alias"] = v(strings.Repeat("n", 400))
	st, err := client(b).State()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range st.Devices {
		if len([]rune(d.Name)) > nameMax {
			t.Errorf("a %d character name reached the surface", len([]rune(d.Name)))
		}
	}
}

// A property the bus did not send reads false. Break this and a device object
// without Paired or Trusted in it - which is what an object being built looks
// like, and what a bus that is not bluez would give - reads as paired and
// trusted, which is the security-shaped direction to get it wrong in.
func TestAPropertyTheBusDidNotSendReadsFalse(t *testing.T) {
	b := &fakeBus{objs: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		hci0: {adapterIface: {"Address": v("00:11:22:33:44:55")}},
		phonePth: {deviceIface: {
			"Address": v("44:5C:E9:1A:2B:3C"),
		}},
	}}
	st, err := client(b).State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Adapter.Powered || st.Adapter.Discovering {
		t.Errorf("an adapter that said nothing reads %+v", st.Adapter)
	}
	if len(st.Devices) != 1 {
		t.Fatalf("devices = %+v", st.Devices)
	}
	if d := st.Devices[0]; d.Paired || d.Trusted || d.Connected {
		t.Errorf("a device that said nothing reads %+v", d)
	}
}

// Trust is about a device you have already agreed to once. Break this and a
// standing yes can be left on an address that has never paired: it survives in
// bluez's own store, and the first time that address does pair, it pairs into a
// device that is already trusted.
func TestTrustNeedsAPairingToBeAbout(t *testing.T) {
	b := oneAdapter()
	b.objs[phonePth][deviceIface]["Paired"] = v(false)
	c := client(b)
	if err := c.Trust("44:5C:E9:1A:2B:3C", true); err == nil {
		t.Error("trusted a device that has never paired")
	}
	if b.made("Trusted") {
		t.Errorf("the property was written anyway: %v", b.calls)
	}
	// Taking a permission back never needs a reason, so untrusting is allowed.
	if err := c.Trust("44:5C:E9:1A:2B:3C", false); err != nil {
		t.Errorf("untrusting an unpaired device: %v", err)
	}
}

// Whether zde is the agent BlueZ actually calls, kept rather than discarded.
//
// BlueZ has one default agent and gives the role to whoever asked last, so any
// process on this machine can take over the answering of pairing questions and
// nothing tells the agent it displaced. Break this and the surface goes on
// looking calm while something else says yes on this machine's behalf.
func TestWhetherTheDefaultAgentRoleWasGivenIsKept(t *testing.T) {
	b := oneAdapter()
	c := client(b)
	if err := c.register(); err != nil {
		t.Fatal(err)
	}
	st, err := c.State()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Agent.Registered || !st.Agent.Default {
		t.Errorf("agent = %+v, want registered and default", st.Agent)
	}

	// And when something else holds the role, that is what it says.
	taken := oneAdapter()
	taken.fail = map[string]error{
		agentManagerIface + ".RequestDefaultAgent": errors.New("something else is the default agent"),
	}
	other := client(taken)
	if err := other.register(); err != nil {
		t.Fatal(err)
	}
	st, err = other.State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Agent.Default {
		t.Error("the role was refused and the state says zde holds it")
	}
	if !st.Agent.Registered || st.Agent.Why == "" {
		t.Errorf("agent = %+v, want registered, not default, and why", st.Agent)
	}
}

// A connection that has died says so, so that the daemon holding it can drop
// it. Break this and the system bus restarting costs the session every
// bluetooth verb and the agent with it, until the next login.
func TestADeadConnectionSaysSo(t *testing.T) {
	b := oneAdapter()
	c := client(b)
	if !c.Alive() {
		t.Fatal("a working connection says it is dead")
	}
	b.mu.Lock()
	b.dead = true
	b.mu.Unlock()
	if c.Alive() {
		t.Error("a connection that has gone says it is fine")
	}
	// And what it answers in the meantime is an absence with a reason rather
	// than a raw error, because that is what a person can read.
	b.err = errors.New("dbus: connection closed by user")
	st, err := c.State()
	if err != nil {
		t.Fatalf("a dead connection answered with an error: %v", err)
	}
	if st.Adapter.Present || st.Adapter.Why == "" {
		t.Errorf("adapter = %+v, want an absence with a reason", st.Adapter)
	}
}

// The waits nest: the question is the shortest, the call that carries it is
// longer, and the client watching is longest. Break the order and something
// gives up on a pairing that is still going - the call cancelling a question
// somebody is reading, or the client reporting a failure that has not happened.
func TestTheWaitsNest(t *testing.T) {
	if !(AnswerWait < waitFor && waitFor < WatchFor) {
		t.Errorf("question %s, call %s, watch %s: they have to nest outwards",
			AnswerWait, waitFor, WatchFor)
	}
}
