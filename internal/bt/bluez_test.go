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
	calls  []string
	bounds []time.Duration // the deadline each call carried, in the same order
	fail   map[string]error
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
	f.record(within, "call "+string(path)+" "+method+argText(args))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fail[method]
}

func (f *fakeBus) Set(within time.Duration, path dbus.ObjectPath, iface, prop string, value any) error {
	f.record(within, "set "+string(path)+" "+iface+"."+prop+argText([]any{value}))
	return nil
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
	if err := client(b).Forget("44:5C:E9:1A:2B:3C"); err != nil {
		t.Fatal(err)
	}
	if !b.made("call " + string(hci0) + " org.bluez.Adapter1.RemoveDevice " + string(phonePth)) {
		t.Errorf("forget did not remove the device: %v", b.calls)
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
		func() error { return c.Trust("44:5C:E9:1A:2B:3C", true) },
		func() error { return c.Disconnect("44:5C:E9:1A:2B:3C") },
		func() error { return c.Forget("44:5C:E9:1A:2B:3C") },
		func() error { return c.Pair("44:5C:E9:1A:2B:3C") },
	} {
		if err := do(); err != nil {
			t.Fatal(err)
		}
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
	if pair <= answerWait {
		t.Errorf("pairing is bounded at %s and the question it waits for stands for %s", pair, answerWait)
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
	c.agent.Answer(false)
}
