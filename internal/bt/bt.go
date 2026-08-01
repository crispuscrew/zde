// Package bt is bluetooth: what is around, what this machine is paired with,
// and the agent that stands between a stranger's device and it.
//
// It talks to BlueZ on the system bus. Not to bluetoothctl: that is an
// interactive REPL, and driving it through a pipe would make zde a parser of
// somebody's prompt text - text that carries no version, changes between
// releases, and is written for a person. The bus interface is typed and
// documented, and it is the same one bluetoothctl uses.
//
// The D-Bus shape here is the one internal/attn already established for the
// notification server: an object exported with exactly the methods a spec
// defines and nothing else, and calls made on a connection this package owns.
package bt

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

// The names BlueZ fixes. Written once, because every one of them is somebody
// else's spelling and a typo in any of them is a call that silently reaches
// nothing.
const (
	busName           = "org.bluez"
	rootPath          = dbus.ObjectPath("/")
	managerPath       = dbus.ObjectPath("/org/bluez")
	adapterIface      = "org.bluez.Adapter1"
	deviceIface       = "org.bluez.Device1"
	agentIface        = "org.bluez.Agent1"
	agentManagerIface = "org.bluez.AgentManager1"
	objectManager     = "org.freedesktop.DBus.ObjectManager"
	properties        = "org.freedesktop.DBus.Properties"
)

// Adapter is the radio, as much of it as anybody has to know.
type Adapter struct {
	Present     bool   `json:"present"`
	Powered     bool   `json:"powered"`
	Discovering bool   `json:"discovering"`
	Address     string `json:"address,omitempty"`
	Name        string `json:"name,omitempty"`
	// Why says what is missing when Present is false: no bluetoothd on the bus,
	// or a bluetoothd with no radio under it. Those are different machines with
	// different fixes - one is a desktop that never asked for bluetooth, the
	// other is a laptop whose adapter has gone - and a bare "no adapter" sends
	// half the people who read it looking in the wrong place.
	Why string `json:"why,omitempty"`
}

// Device is one thing the adapter can see or remembers.
//
// Trusted is separate from Paired on purpose, and it is the difference this
// whole package is careful about: paired means the two have a key, trusted
// means this one reconnects and uses services without asking anybody again.
type Device struct {
	Address   string `json:"address"`
	Name      string `json:"name,omitempty"`
	Paired    bool   `json:"paired"`
	Trusted   bool   `json:"trusted"`
	Connected bool   `json:"connected"`
	// RSSI is the signal in dBm, and 0 means the device is remembered rather
	// than in front of us right now. A real reading is always negative.
	RSSI int16 `json:"rssi,omitempty"`
}

// State is the whole answer to "what is going on with bluetooth".
type State struct {
	Adapter Adapter  `json:"adapter"`
	Devices []Device `json:"devices"`
	// Pending is the pairing question waiting for a person, when there is one.
	Pending *Request `json:"pending,omitempty"`
	// Doing is the long call still running, as "pairing AA:BB:.." or
	// "connecting AA:BB:..", and Failed is what the last finished one said when
	// it did not work.
	//
	// Both are here because those two calls outlast the socket round trip that
	// asked for them: pairing waits on a person reading a number off a phone,
	// and connecting waits on a radio. So the answer to "how did it go" is the
	// next question you ask, and this is where it is.
	Doing  string `json:"doing,omitempty"`
	Failed string `json:"failed,omitempty"`
}

// Absent is the answer for a machine with no bluetooth to speak of. It is an
// answer and not an error: a desktop that never asked for a radio is the
// ordinary case, and a verb that errors there is one a person reads as broken.
func Absent(why string) State { return State{Adapter: Adapter{Why: why}} }

// snapshot is one read of the bus: the state a caller sees, and the object
// paths behind it. The paths stay in this package - nothing outside it has any
// business addressing a bluez object, and a path that crossed the zded socket
// would be a path something could ask us to call.
type snapshot struct {
	state   State
	adapter dbus.ObjectPath
	devices map[string]dbus.ObjectPath // address -> path
	byAddr  map[string]Device
}

// read asks ObjectManager for everything org.bluez has, in one call, and turns
// it into the answer. One call rather than a walk: the tree is adapters and
// their devices, and asking per object would mean a list assembled from a
// dozen different moments.
func read(b bus) (snapshot, error) {
	objs, err := b.Managed(askFor)
	if err != nil {
		if noService(err) {
			// The bus is there and bluetoothd is not: the ordinary state of a
			// machine with hardware.bluetooth off, which is every desktop until
			// somebody says otherwise.
			return snapshot{state: Absent("bluetoothd is not on the system bus")}, nil
		}
		return snapshot{}, err
	}

	// Sorted, so that a machine with two radios picks the same one every time.
	// Which one is arbitrary; that it does not change between two presses of a
	// key is not.
	paths := make([]dbus.ObjectPath, 0, len(objs))
	for p := range objs {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })

	snap := snapshot{devices: map[string]dbus.ObjectPath{}, byAddr: map[string]Device{}}
	for _, p := range paths {
		props, ok := objs[p][adapterIface]
		if !ok {
			continue
		}
		snap.adapter = p
		snap.state.Adapter = Adapter{
			Present:     true,
			Powered:     boolOf(props, "Powered"),
			Discovering: boolOf(props, "Discovering"),
			Address:     stringOf(props, "Address"),
			Name:        stringOf(props, "Alias"),
		}
		break
	}
	if !snap.state.Adapter.Present {
		snap.state = Absent("bluetoothd is running and has no adapter: no radio, or rfkill has it")
		return snap, nil
	}

	for _, p := range paths {
		props, ok := objs[p][deviceIface]
		if !ok {
			continue
		}
		// Only this adapter's devices. With two radios the tree carries both,
		// and a list mixing them would offer a row that this adapter cannot act
		// on at all.
		if !strings.HasPrefix(string(p), string(snap.adapter)+"/") {
			continue
		}
		addr, err := normalAddr(stringOf(props, "Address"))
		if err != nil {
			continue // bluez does not do this; a device without one is not addressable
		}
		d := Device{
			Address:   addr,
			Name:      stringOf(props, "Alias"),
			Paired:    boolOf(props, "Paired"),
			Trusted:   boolOf(props, "Trusted"),
			Connected: boolOf(props, "Connected"),
			RSSI:      int16Of(props, "RSSI"),
		}
		snap.state.Devices = append(snap.state.Devices, d)
		snap.devices[addr] = p
		snap.byAddr[addr] = d
	}
	sortDevices(snap.state.Devices)
	return snap, nil
}

// sortDevices puts the list in an order that does not move under the hand.
//
// What you own first - connected, then paired - because those are the rows a
// person came here to act on, and the rest of the list is whatever happens to
// be in the room. Then named before nameless, since a row that says nothing but
// an address is not one anybody recognises.
//
// The signal is deliberately not in it. It changes every second while a scan is
// running, and a list that reorders itself while you are reaching for row three
// is one where the number under your finger is not the thing you looked at.
func sortDevices(devices []Device) {
	sort.SliceStable(devices, func(i, j int) bool {
		a, b := devices[i], devices[j]
		if a.Connected != b.Connected {
			return a.Connected
		}
		if a.Paired != b.Paired {
			return a.Paired
		}
		if (a.Name != "") != (b.Name != "") {
			return a.Name != ""
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Address < b.Address
	})
}

// normalAddr is a device address as BlueZ writes them: six hex pairs, upper
// case, colon separated.
//
// Checked rather than trusted, because the address is what every verb takes
// from a person and it is matched against object paths under the adapter. An
// unchecked string reaching a bus call is a way to address objects that are not
// devices at all - and the check is six pairs of hex, which is the whole of
// what an address is.
func normalAddr(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return "", fmt.Errorf("%q is not a bluetooth address: six hex pairs, like 44:5C:E9:1A:2B:3C", s)
	}
	for _, p := range parts {
		if len(p) != 2 || !isHex(p[0]) || !isHex(p[1]) {
			return "", fmt.Errorf("%q is not a bluetooth address: six hex pairs, like 44:5C:E9:1A:2B:3C", s)
		}
	}
	return s, nil
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
}

// addrOf is the address inside a bluez device path, which is where the agent
// gets it: bluetoothd hands its callbacks an object path and nothing else.
//
// /org/bluez/hci0/dev_44_5C_E9_1A_2B_3C is bluez's own spelling of an address
// and has been for the life of the interface. A path that is not one answers
// empty rather than guessing, and a question about a device nobody can name is
// still a question worth refusing.
func addrOf(path dbus.ObjectPath) string {
	last := string(path)
	if i := strings.LastIndex(last, "/"); i >= 0 {
		last = last[i+1:]
	}
	if !strings.HasPrefix(last, "dev_") {
		return ""
	}
	addr, err := normalAddr(strings.ReplaceAll(strings.TrimPrefix(last, "dev_"), "_", ":"))
	if err != nil {
		return ""
	}
	return addr
}

// noService is the bus saying nobody owns org.bluez. It is an error on the
// wire and an answer to us, the same distinction internal/attn makes about the
// notification name.
func noService(err error) bool {
	var derr dbus.Error
	if !errors.As(err, &derr) {
		return false
	}
	switch derr.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner":
		return true
	}
	return false
}

// The property readers. Typed rather than converted: a property that is not
// the type the interface says it is means we are talking to something that is
// not BlueZ, and reading it as a zero value beats inventing a number.
func boolOf(props map[string]dbus.Variant, name string) bool {
	var b bool
	if v, ok := props[name]; ok {
		v.Store(&b)
	}
	return b
}

func stringOf(props map[string]dbus.Variant, name string) string {
	var s string
	if v, ok := props[name]; ok {
		v.Store(&s)
	}
	return s
}

func int16Of(props map[string]dbus.Variant, name string) int16 {
	var n int16
	if v, ok := props[name]; ok {
		v.Store(&n)
	}
	return n
}
