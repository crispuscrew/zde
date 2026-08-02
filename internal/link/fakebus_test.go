package link

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// A NetworkManager that is not NetworkManager, on a bus that is a real bus.
//
// Everything behind the Manager interface used to be untested, and it was the
// only part of this package a person's password goes through. Not for want of
// trying: the code is a conversation with another process, and a fake at the
// Go boundary would have been a fake of the conversation zde believes it is
// having - which is exactly the belief that was wrong. A reviewer built this
// with a real dbus-daemon in an afternoon and found nine defects with it, six
// of which no in-process fake could have seen, because they live in the order
// of the calls and in what the other end says between them.
//
// So: a private dbus-daemon, a fake NetworkManager exporting the objects and
// the methods the real one does, and the real NM type driven at it through the
// real Open(). The only thing the test changes about the code under test is
// DBUS_SYSTEM_BUS_ADDRESS, which is what the bus library reads to find the
// system bus - so even the name check in Open() is exercised.
//
// The world is a map of objects to interfaces to properties, and tests reach
// into it while a call is in flight. That is the point: a link changes under
// the question, and the defects were all about what zde concluded when it did.

// dbusDaemon is the binary this needs. Without it these tests skip, loudly:
// there is no way to fake a bus honestly without one, and a skipped test that
// says nothing is a test nobody notices has stopped running.
const dbusDaemon = "dbus-daemon"

// fakeBus is a private message bus with nobody on it but this test. Not `bus`,
// which is the package that bounds a dial (internal/bus) and is imported by the
// code under test.
type fakeBus struct {
	addr string
	conn *dbus.Conn // the fake's own connection, which owns the NetworkManager name
}

// startBus brings up a dbus-daemon of this test's own and points the code under
// test at it. It dies with the test.
func startBus(t *testing.T) *fakeBus {
	t.Helper()
	if _, err := exec.LookPath(dbusDaemon); err != nil {
		t.Skipf("no %s on PATH, so there is no bus to fake NetworkManager on: "+
			"add pkgs.dbus to the devshell (flake.nix) and this runs", dbusDaemon)
	}

	// The socket goes wherever the daemon puts it rather than in the test's
	// own directory: a unix socket path is capped at about 108 bytes, and a Go
	// temp directory under a long TMPDIR has spent most of that before the
	// file name.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(cfg, []byte(`<!DOCTYPE busconfig PUBLIC
 "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:tmpdir=/tmp</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow own="*"/>
    <allow send_destination="*"/>
    <allow receive_sender="*"/>
  </policy>
</busconfig>
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(dbusDaemon, "--config-file="+cfg, "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		cmd.Process.Kill() //nolint:errcheck // already failing
		t.Fatalf("%s never printed an address: %v", dbusDaemon, err)
	}
	addr := strings.TrimSpace(line)

	b := &fakeBus{addr: addr}
	t.Cleanup(func() {
		if b.conn != nil {
			b.conn.Close()
		}
		// Interrupt rather than kill, so the daemon unlinks its socket on the
		// way out instead of leaving one in /tmp per test run.
		cmd.Process.Signal(os.Interrupt) //nolint:errcheck // it is going away either way
		cmd.Wait()                       //nolint:errcheck // its exit status is not this test's business
	})
	// What ConnectSystemBus reads. This is the whole seam: no production code
	// knows it is being tested.
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)
	return b
}

// stateReason is what NetworkManager's Device.StateReason property is: a D-Bus
// struct of the state and the reason it is in it, from one read.
//
// A Go struct and not a slice, because those are different types on the wire -
// (uu) against av - and the production code stores this into a struct of its
// own. A fake that sent the wrong one would prove that decode works while the
// real one failed, which is the whole class of mistake this file is for.
type stateReason struct {
	State  uint32
	Reason uint32
}

// world is what the fake NetworkManager knows: objects, their interfaces, and
// the properties on them, exactly as they arrive over the bus.
type world map[dbus.ObjectPath]map[string]map[string]dbus.Variant

// nmFake answers as NetworkManager. Its state is public to the test on purpose:
// a link changes while it is being asked about, and every defect this file was
// written for is about what zde concluded when it did.
type nmFake struct {
	// conn is the fake's own connection to the bus, set by serve, and t is the
	// test it is serving - so a world put together wrongly is reported against
	// the test that did it.
	conn *dbus.Conn
	t    *testing.T

	mu sync.Mutex
	// objs is the world. Read and written by the test while calls are in
	// flight, so it is behind the lock.
	objs world
	// settings is what a saved profile holds, which is a method on the bus and
	// not a property (GetSettings), and is why the saved half of a listing
	// cannot be flattened into one call.
	settings map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	// calls is every method that arrived, in order, as "path member".
	calls []string
	// slow is how long a call takes, for the tests about budgets, and slowOn
	// is which one - empty for all of them. Naming the member matters: a bus
	// that is slow from the first call never reaches the enumeration a test is
	// about, and the test then passes by never having got there.
	slow   time.Duration
	slowOn string
	// fail is the D-Bus error a member answers with, by name. A bus that will
	// not answer is not the same fact as one that answers "no", and telling
	// those apart is what decides whether a profile is deleted.
	fail map[string]string
	// nextActive is what the state of the next activation's object will be.
	// ACTIVATING by default, which is what NetworkManager answers for the first
	// moments of every join.
	nextActive uint32
	// added counts the profiles this fake was asked to create, so a test can
	// name the one that a join left behind.
	added int
	// newest is the activation object the fake handed back most recently.
	newest dbus.ObjectPath
}

const (
	rootPath   = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	setsPath   = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")
	wifiPath   = dbus.ObjectPath("/org/freedesktop/NetworkManager/Devices/1")
	donglePath = dbus.ObjectPath("/org/freedesktop/NetworkManager/Devices/0")
	wiredPath  = dbus.ObjectPath("/org/freedesktop/NetworkManager/Devices/2")
	homeAP     = dbus.ObjectPath("/org/freedesktop/NetworkManager/AccessPoint/1")
	cafeAP     = dbus.ObjectPath("/org/freedesktop/NetworkManager/AccessPoint/2")
	homeConn   = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings/1")
	onHome     = dbus.ObjectPath("/org/freedesktop/NetworkManager/ActiveConnection/1")
)

// The interfaces every object answers on. One list for all of them, because
// D-Bus dispatches on path and interface and member together: an object asked
// about an interface it has nothing for answers about the member, which is what
// the real one does too.
var faces = []string{
	"org.freedesktop.DBus.Properties",
	nmIface, devIface, wifiIface, apIface, setIface, profIface, actIface,
}

// serve puts a fake NetworkManager on the bus and hands back the real NM,
// opened the way zded opens it.
func serve(t *testing.T, f *nmFake) *NM {
	t.Helper()
	b := startBus(t)
	conn, err := dbus.Connect(b.addr)
	if err != nil {
		t.Fatal(err)
	}
	b.conn = conn
	if f.settings == nil {
		f.settings = map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}
	}
	if f.nextActive == 0 {
		f.nextActive = 1 // ACTIVATING
	}
	f.conn = conn
	f.t = t
	for path := range f.objs {
		f.export(t, path)
	}
	// The profiles too. A saved network is an object with no properties on it
	// worth reading and a method that answers what it is for, which is why the
	// saved half of a listing costs a call each and cannot be flattened.
	for path := range f.settings {
		f.export(t, path)
	}
	reply, err := conn.RequestName(nmService, dbus.NameFlagDoNotQueue)
	if err != nil {
		t.Fatal(err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("the fake could not take %s on its own bus", nmService)
	}

	m, err := Open()
	if err != nil {
		t.Fatalf("Open against the fake bus: %v", err)
	}
	nm, ok := m.(*NM)
	if !ok {
		t.Fatalf("Open answered %T, and these tests drive the real one", m)
	}
	t.Cleanup(func() { nm.Close() }) //nolint:errcheck // the test is over
	return nm
}

func (f *nmFake) export(t *testing.T, path dbus.ObjectPath) {
	t.Helper()
	o := &nmObject{f: f, path: path}
	for _, iface := range faces {
		if err := f.conn.Export(o, path, iface); err != nil {
			t.Fatal(err)
		}
	}
}

// The bits of the fake a test drives.

func (f *nmFake) get(path dbus.ObjectPath, iface, name string) dbus.Variant {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objs[path][iface][name]
}

// answerable is whether the fake can hold a value as a property and still
// answer with it. Only object paths can fail: the bus library will not marshal
// one that is empty or does not start with a slash, and everything else these
// tests put in a property is a number, a string or a list.
func answerable(v any) bool {
	p, ok := v.(dbus.ObjectPath)
	return !ok || p.IsValid()
}

// setSlow changes how long a read takes while a call is in flight, which is how
// a test says "the bus went slow at exactly this moment".
// setFail makes a member answer with a D-Bus error from now on.
func (f *nmFake) setFail(member, errName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]string{}
	}
	f.fail[member] = errName
}

func (f *nmFake) failing(member string) *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name, ok := f.fail[member]
	if !ok {
		return nil
	}
	return dbus.NewError(name, []any{"the fake was told to fail " + member})
}

func (f *nmFake) setSlow(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slow = d
}

// set changes the world under a call in flight, which is what most of these
// tests are about.
//
// It refuses to hold an object path that is not one. A property answered with
// an empty path is a reply the bus library cannot marshal, so the fake sends an
// error instead of a value - and the code under test then spends its whole
// budget on a device it thinks will not answer, four seconds later and three
// screens away from the line that did it. Worse than the wasted time: a fake
// that answers invalidly teaches the code to tolerate something NetworkManager
// never sends.
//
// Errorf and not Fatalf, because most of these writes happen on a goroutine
// staging a change mid-call, and FailNow on a goroutine that is not the test's
// stops that goroutine rather than the test.
func (f *nmFake) set(path dbus.ObjectPath, iface, name string, v any) {
	if !answerable(v) {
		f.t.Errorf("%s.%s was set to %q, which is not an object path: "+
			"NetworkManager answers with a path or with nothing", iface, name, v)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objs[path] == nil {
		f.objs[path] = map[string]map[string]dbus.Variant{}
	}
	if f.objs[path][iface] == nil {
		f.objs[path][iface] = map[string]dbus.Variant{}
	}
	f.objs[path][iface][name] = dbus.MakeVariant(v)
}

// asked is whether a member was called on a path, which is how a test says
// "the profile was deleted" or "nothing was written to it".
func (f *nmFake) asked(path dbus.ObjectPath, member string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == string(path)+" "+member {
			return true
		}
	}
	return false
}

func (f *nmFake) askedAny(member string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasSuffix(c, " "+member) {
			return true
		}
	}
	return false
}

func (f *nmFake) record(path dbus.ObjectPath, member string) {
	f.mu.Lock()
	f.calls = append(f.calls, string(path)+" "+member)
	slow, on := f.slow, f.slowOn
	f.mu.Unlock()
	if slow > 0 && (on == "" || strings.HasPrefix(member, on)) {
		time.Sleep(slow)
	}
}

// nmObject is one object on the fake's bus. One type for every path: D-Bus
// dispatches on path, interface and member, so an object asked for a member it
// does not have answers about the member, which is what the real one does.
type nmObject struct {
	f    *nmFake
	path dbus.ObjectPath
}

func (o *nmObject) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	o.f.record(o.path, "Get."+name)
	if err := o.f.failing("Get." + name); err != nil {
		return dbus.Variant{}, err
	}
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	props, ok := o.f.objs[o.path][iface]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", []any{"no " + iface})
	}
	v, ok := props[name]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"no property " + name})
	}
	return v, nil
}

func (o *nmObject) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	o.f.record(o.path, "GetAll."+iface)
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	props := o.f.objs[o.path][iface]
	out := make(map[string]dbus.Variant, len(props))
	for k, v := range props {
		out[k] = v
	}
	return out, nil
}

func (o *nmObject) GetDevices() ([]dbus.ObjectPath, *dbus.Error) {
	o.f.record(o.path, "GetDevices")
	return o.paths(nmIface, "Devices"), nil
}

func (o *nmObject) GetAllAccessPoints() ([]dbus.ObjectPath, *dbus.Error) {
	o.f.record(o.path, "GetAllAccessPoints")
	return o.paths(wifiIface, "AccessPoints"), nil
}

func (o *nmObject) ListConnections() ([]dbus.ObjectPath, *dbus.Error) {
	o.f.record(o.path, "ListConnections")
	return o.paths(setIface, "Connections"), nil
}

func (o *nmObject) paths(iface, name string) []dbus.ObjectPath {
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	var out []dbus.ObjectPath
	if v, ok := o.f.objs[o.path][iface][name]; ok {
		v.Store(&out) //nolint:errcheck // a world that put the wrong type here is a test bug
	}
	return out
}

func (o *nmObject) RequestScan(options map[string]dbus.Variant) *dbus.Error {
	o.f.record(o.path, "RequestScan")
	return nil
}

func (o *nmObject) GetSettings() (map[string]map[string]dbus.Variant, *dbus.Error) {
	o.f.record(o.path, "GetSettings")
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	s, ok := o.f.settings[o.path]
	if !ok {
		return nil, dbus.NewError("org.freedesktop.DBus.Error.UnknownMethod", []any{"no such profile"})
	}
	// Secrets are never in what GetSettings answers, which is the fact the
	// production code leans on when it says it cannot read a password back.
	out := map[string]map[string]dbus.Variant{}
	for group, fields := range s {
		out[group] = map[string]dbus.Variant{}
		for k, v := range fields {
			if k == "psk" {
				continue
			}
			out[group][k] = v
		}
	}
	return out, nil
}

func (o *nmObject) Update(settings map[string]map[string]dbus.Variant) *dbus.Error {
	o.f.record(o.path, "Update")
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	o.f.settings[o.path] = settings
	return nil
}

func (o *nmObject) Delete() *dbus.Error {
	o.f.record(o.path, "Delete")
	o.f.mu.Lock()
	defer o.f.mu.Unlock()
	delete(o.f.settings, o.path)
	// And out of the list, the way NetworkManager drops a deleted profile.
	var keep []dbus.ObjectPath
	if v, ok := o.f.objs[setsPath][setIface]["Connections"]; ok {
		var all []dbus.ObjectPath
		v.Store(&all) //nolint:errcheck // the world's own type
		for _, p := range all {
			if p != o.path {
				keep = append(keep, p)
			}
		}
	}
	o.f.objs[setsPath][setIface]["Connections"] = dbus.MakeVariant(keep)
	return nil
}

func (o *nmObject) ActivateConnection(profile, dev, ap dbus.ObjectPath) (dbus.ObjectPath, *dbus.Error) {
	o.f.record(o.path, "ActivateConnection")
	return o.f.activate(dev), nil
}

func (o *nmObject) AddAndActivateConnection(settings map[string]map[string]dbus.Variant, dev, ap dbus.ObjectPath) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	o.f.record(o.path, "AddAndActivateConnection")
	o.f.mu.Lock()
	o.f.added++
	profile := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/NetworkManager/Settings/added%d", o.f.added))
	o.f.settings[profile] = settings
	var all []dbus.ObjectPath
	if v, ok := o.f.objs[setsPath][setIface]["Connections"]; ok {
		v.Store(&all) //nolint:errcheck // the world's own type
	}
	o.f.objs[setsPath][setIface]["Connections"] = dbus.MakeVariant(append(all, profile))
	o.f.mu.Unlock()
	o.f.exportLater(profile)
	return profile, o.f.activate(dev), nil
}

func (o *nmObject) DeactivateConnection(active dbus.ObjectPath) *dbus.Error {
	o.f.record(o.path, "DeactivateConnection")
	return nil
}

// activate makes the activation object NetworkManager hands back, in the state
// the test asked for. The device is deliberately left alone: the first moments
// of a join are exactly the moments when the device still reads as being on the
// network you are leaving, and pretending otherwise would hide the defect this
// file exists to catch.
func (f *nmFake) activate(dev dbus.ObjectPath) dbus.ObjectPath {
	f.mu.Lock()
	path := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/NetworkManager/ActiveConnection/new%d", len(f.calls)))
	state := f.nextActive
	f.mu.Unlock()

	// On the bus first. Everything that learns about an activation learns
	// through the fake, so an object that is visible before it can answer is a
	// window where a read of it comes back "no such object" - which the code
	// under test reads, correctly, as an activation that has been torn down.
	f.exportLater(path)

	f.mu.Lock()
	if f.objs[path] == nil {
		f.objs[path] = map[string]map[string]dbus.Variant{}
	}
	f.objs[path][actIface] = map[string]dbus.Variant{"State": dbus.MakeVariant(state)}
	// What the fake handed back last, remembered rather than worked out again
	// afterwards: these are named new1, new2, new10, and the newest of those by
	// string order is new2.
	f.newest = path
	f.mu.Unlock()
	return path
}

// exportLater puts an object made mid-call onto the bus. Its own method because
// exporting has to happen outside the fake's lock: the bus library takes its own
// while it registers.
func (f *nmFake) exportLater(path dbus.ObjectPath) {
	o := &nmObject{f: f, path: path}
	for _, iface := range faces {
		f.conn.Export(o, path, iface) //nolint:errcheck // a fake that cannot export is a test that will fail on the next call
	}
}

// home is the ordinary machine these tests start from: one wifi radio,
// activated on "home", one saved profile for it, and a second network in range
// that nothing has ever joined.
func home() *nmFake {
	return &nmFake{
		objs: world{
			rootPath: {nmIface: {"Devices": dbus.MakeVariant([]dbus.ObjectPath{wifiPath})}},
			setsPath: {setIface: {"Connections": dbus.MakeVariant([]dbus.ObjectPath{homeConn})}},
			wifiPath: {
				devIface: {
					"DeviceType":       dbus.MakeVariant(uint32(typeWifi)),
					"State":            dbus.MakeVariant(uint32(stateActivated)),
					"ActiveConnection": dbus.MakeVariant(onHome),
					"StateReason":      dbus.MakeVariant(stateReason{uint32(stateActivated), 0}),
				},
				wifiIface: {
					"AccessPoints":      dbus.MakeVariant([]dbus.ObjectPath{homeAP, cafeAP}),
					"ActiveAccessPoint": dbus.MakeVariant(homeAP),
				},
			},
			homeAP: {apIface: {
				"Ssid":     dbus.MakeVariant([]byte("home")),
				"Strength": dbus.MakeVariant(byte(84)),
				"WpaFlags": dbus.MakeVariant(uint32(0x100)),
			}},
			cafeAP: {apIface: {
				"Ssid":     dbus.MakeVariant([]byte("cafe")),
				"Strength": dbus.MakeVariant(byte(61)),
				"RsnFlags": dbus.MakeVariant(uint32(0x100)),
			}},
			onHome: {actIface: {"State": dbus.MakeVariant(uint32(activeActivated))}},
		},
		settings: map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
			homeConn: {
				groupConn: {"id": dbus.MakeVariant("home"), "type": dbus.MakeVariant(groupWifi)},
				groupWifi: {"ssid": dbus.MakeVariant([]byte("home"))},
				groupSec:  {"key-mgmt": dbus.MakeVariant("wpa-psk"), "psk": dbus.MakeVariant("the-real-passphrase")},
			},
		},
	}
}

// neverWithin is the other half of waitFor: something that must not happen,
// watched for long enough that it would have. Used where the wrong behaviour is
// fast, so the window is short and the test is not a sleep.
func neverWithin(t *testing.T, d time.Duration, what string, happened func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if happened() {
			t.Fatalf("%s, and it should not have", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitFor is how these tests watch for something the code does behind the
// answer it already gave - a profile being deleted by the watcher, say. A bare
// sleep would either be flaky or slow, and every one of these lands in
// milliseconds on a local bus.
func waitFor(t *testing.T, what string, until func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if until() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waited five seconds and %s never happened", what)
}
