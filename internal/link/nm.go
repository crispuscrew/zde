package link

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// NetworkManager over D-Bus, and not over nmcli, and the reason is the
// password.
//
// `nmcli device wifi connect SSID password SECRET` puts a wifi password in a
// process's argv, and argv is world readable on a stock Linux: anybody with an
// account on the machine can read /proc/<pid>/cmdline for as long as that
// process lives, and it takes seconds. nmcli can be fed a secret other ways -
// --ask on a tty, a keyfile - but every one of them is a second path to get
// right, and the tty one does not exist for a daemon answering a socket.
//
// Over D-Bus the secret is an argument in a message on NetworkManager's own
// system-bus socket: no argv, no file zde wrote, no shell history. Where it
// ends up is NetworkManager's own store under /etc/NetworkManager, root-only
// and 0600, which is exactly what "NetworkManager has a saved profile for this
// network" means. zde never reads it back - nothing here calls GetSecrets - and
// code that cannot read a password cannot leak one.
//
// The other half of the argument is that the bus library is already vendored
// and already in the daemon (internal/attn speaks the notification spec on it),
// so this costs no new dependency; what it costs is the property reads below,
// which nmcli would have done for us.
//
// The names NetworkManager fixes. Every one of these is API: they are what
// nmcli itself calls.
const (
	nmService = "org.freedesktop.NetworkManager"
	nmIface   = "org.freedesktop.NetworkManager"
	devIface  = nmIface + ".Device"
	wifiIface = devIface + ".Wireless"
	apIface   = nmIface + ".AccessPoint"
	setIface  = nmIface + ".Settings"
	profIface = setIface + ".Connection"
	actIface  = nmIface + ".Connection.Active"

	propsGet    = "org.freedesktop.DBus.Properties.Get"
	propsGetAll = "org.freedesktop.DBus.Properties.GetAll"

	nmPath  = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	setPath = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")

	// The settings groups a wifi profile is made of, in NetworkManager's
	// spelling.
	groupConn = "connection"
	groupWifi = "802-11-wireless"
	groupSec  = "802-11-wireless-security"
)

// NMDeviceType and NMDeviceState, the few values this needs.
const (
	typeEthernet = 1
	typeWifi     = 2

	// Past this the password has been accepted and what is left is an address.
	stateIPConfig  = 70
	stateActivated = 100
	stateFailed    = 120

	// NMActiveConnectionState.
	activeActivated   = 2
	activeDeactivated = 4
)

// askFor bounds every call to NetworkManager. zded answers keybinds on one
// socket, and a bus call with no deadline is a keypress that never comes back
// if NetworkManager wedges - which it can, waiting on a driver.
const askFor = 2 * time.Second

// NM is NetworkManager on the system bus.
type NM struct {
	conn *dbus.Conn
}

// Open connects, or says that there is nothing here to connect to.
//
// It returns the interface rather than the type because the absence of a
// manager is one of the answers: the caller wants "there is no NetworkManager"
// as a state, and this is the one place that can tell it apart from a bus that
// is there and unhappy.
func Open() (Manager, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		// No system bus at all. A machine can be perfectly online like that;
		// what it cannot be is asked about it this way.
		return nil, fmt.Errorf("%w: %v", ErrNoManager, err)
	}
	// Bounded like every other call here: a bus daemon that accepts a message
	// and never answers would otherwise hang the first keypress that asks.
	ctx, cancel := context.WithTimeout(context.Background(), askFor)
	defer cancel()
	var owned bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, nmService).Store(&owned); err != nil {
		conn.Close()
		return nil, err
	}
	if !owned {
		// The bus is there and nobody is NetworkManager: layer 0 installs it
		// only for laptops (nix/system.nix, zde.laptop.enable), so this is the
		// ordinary state of a desktop, not a fault.
		conn.Close()
		return nil, ErrNoManager
	}
	return &NM{conn: conn}, nil
}

// Alive is whether the connection is still worth keeping. NetworkManager
// restarts - an update, a `systemctl restart` - and the socket dies with it;
// without this the daemon would hold a dead connection and report an unknown
// link for the rest of the session.
func (m *NM) Alive() bool { return m.conn != nil && m.conn.Connected() }

func (m *NM) call(path dbus.ObjectPath, method string, out []any, args ...any) error {
	ctx, cancel := context.WithTimeout(context.Background(), askFor)
	defer cancel()
	c := m.conn.Object(nmService, path).CallWithContext(ctx, method, 0, args...)
	if c.Err != nil {
		return c.Err
	}
	if len(out) == 0 {
		return nil
	}
	return c.Store(out...)
}

// props reads every property of one interface in one call. One round trip per
// object rather than five: a busy room is thirty access points, and thirty
// objects at five calls each is a widget that opens slowly for no reason.
func (m *NM) props(path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	var out map[string]dbus.Variant
	err := m.call(path, propsGetAll, []any{&out}, iface)
	return out, err
}

func (m *NM) prop(path dbus.ObjectPath, iface, name string, into any) error {
	var v dbus.Variant
	if err := m.call(path, propsGet, []any{&v}, iface, name); err != nil {
		return err
	}
	return v.Store(into)
}

// num reads a NetworkManager number whatever width it chose for it: Strength
// is a byte, State is a uint32, and the bus library converts between neither.
// A property that is not there reads as zero, which for every one of these is
// the honest answer - no signal, state unknown, no flags.
func num(p map[string]dbus.Variant, name string) int {
	v, ok := p[name]
	if !ok {
		return 0
	}
	switch x := v.Value().(type) {
	case byte:
		return int(x)
	case uint32:
		return int(x)
	case int32:
		return int(x)
	}
	return 0
}

func raw(p map[string]dbus.Variant, name string) []byte {
	var b []byte
	if v, ok := p[name]; ok {
		v.Store(&b) //nolint:errcheck // a property of another type reads as no bytes, which is what it is
	}
	return b
}

// device is one of NetworkManager's devices, narrowed to what a link is made
// of.
type device struct {
	path  dbus.ObjectPath
	kind  int
	state int
}

func (m *NM) devices() ([]device, error) {
	var paths []dbus.ObjectPath
	if err := m.call(nmPath, nmIface+".GetDevices", []any{&paths}); err != nil {
		return nil, err
	}
	out := make([]device, 0, len(paths))
	for _, p := range paths {
		props, err := m.props(p, devIface)
		if err != nil {
			// A device that went away between the list and the question - a
			// USB dongle being unplugged is exactly this. One missing device is
			// not a reason to have no answer about the others.
			continue
		}
		out = append(out, device{path: p, kind: num(props, "DeviceType"), state: num(props, "State")})
	}
	return out, nil
}

// Status is the link, read off the devices rather than off the default route.
//
// Wired wins when both are up, because NetworkManager's own metrics send the
// traffic down the cable, and the bar should say what is carrying it.
//
// A link that is neither - a modem, a bridge, a tunnel carrying everything on
// its own - reads as nothing connected. That is what a widget which knows about
// two kinds of link can honestly say, and it is why the word for it on the bar
// is "no network" and not "offline".
func (m *NM) Status() (Status, error) {
	devs, err := m.devices()
	if err != nil {
		return Status{}, err
	}
	st := Status{Kind: KindNone}
	wifi := device{}
	for _, d := range devs {
		if d.kind == typeWifi && wifi.path == "" {
			wifi = d
			st.Wifi = true
		}
	}
	for _, d := range devs {
		if d.kind == typeEthernet && d.state == stateActivated {
			st.Kind = KindWired
			return st, nil
		}
	}
	if wifi.path != "" && wifi.state == stateActivated {
		st.Kind = KindWifi
		// A wifi link whose access point cannot be read is still a wifi link:
		// the name and the bars are what is missing, not the connection.
		st.SSID, st.Signal = m.activeAP(wifi.path)
	}
	return st, nil
}

// activeAP is the name and strength of what a wifi device is on.
func (m *NM) activeAP(dev dbus.ObjectPath) (string, int) {
	var ap dbus.ObjectPath
	if err := m.prop(dev, wifiIface, "ActiveAccessPoint", &ap); err != nil || ap == "" || ap == "/" {
		return "", 0
	}
	props, err := m.props(ap, apIface)
	if err != nil {
		return "", 0
	}
	ssid, _ := printableSSID(raw(props, "Ssid"))
	return ssid, num(props, "Strength")
}

// wifiDevice is the radio, or the empty path on a machine with none.
func (m *NM) wifiDevice() (dbus.ObjectPath, error) {
	devs, err := m.devices()
	if err != nil {
		return "", err
	}
	for _, d := range devs {
		if d.kind == typeWifi {
			return d.path, nil
		}
	}
	return "", nil
}

// List is what the radio can see.
func (m *NM) List() ([]Network, error) {
	dev, err := m.wifiDevice()
	if err != nil {
		return nil, err
	}
	if dev == "" {
		// No radio. An empty list and no error: Status.Wifi is what says why,
		// and a machine with no wifi is not a machine with a problem.
		return nil, nil
	}
	// Ask for a scan and do not wait for one. A scan takes seconds, and
	// NetworkManager refuses one it has just done, so waiting would make the
	// widget slow exactly when it already has an answer. What this buys is the
	// second look: open the widget again and the list is fresher.
	m.call(dev, wifiIface+".RequestScan", nil, map[string]dbus.Variant{}) //nolint:errcheck // rate limited or not permitted: the cache is still an answer

	var aps []dbus.ObjectPath
	if err := m.call(dev, wifiIface+".GetAllAccessPoints", []any{&aps}); err != nil {
		return nil, err
	}
	saved := m.savedProfiles()
	here, _ := m.activeAP(dev)
	seen := make([]Network, 0, len(aps))
	for _, p := range aps {
		props, err := m.props(p, apIface)
		if err != nil {
			continue // gone since the list was taken
		}
		ssid, ok := printableSSID(raw(props, "Ssid"))
		if !ok {
			continue
		}
		seen = append(seen, Network{
			SSID:   ssid,
			Signal: num(props, "Strength"),
			Secure: secured(num(props, "Flags"), num(props, "WpaFlags"), num(props, "RsnFlags")),
			Saved:  saved[ssid] != "",
			Active: here != "" && ssid == here,
		})
	}
	return strongestFirst(seen), nil
}

// savedProfiles is the networks NetworkManager already has a profile for, and
// the profile for each. One question answered twice: whether joining has to ask
// anybody for a password, and which profile to activate when it does not.
//
// Two profiles for one network is allowed, and the first is kept - which is the
// one NetworkManager would pick itself.
//
// Errors are dropped on purpose: a profile that cannot be read is a network
// offered as unsaved, which costs a password prompt somebody can cancel.
// Refusing the whole list over it would cost the list.
func (m *NM) savedProfiles() map[string]dbus.ObjectPath {
	out := map[string]dbus.ObjectPath{}
	var profiles []dbus.ObjectPath
	if err := m.call(setPath, setIface+".ListConnections", []any{&profiles}); err != nil {
		return out
	}
	for _, p := range profiles {
		ssid, ok := m.profileSSID(p)
		if !ok {
			continue
		}
		if _, had := out[ssid]; !had {
			out[ssid] = p
		}
	}
	return out
}

// profileSSID is the network a saved profile is for, and false for a profile
// that is not wifi.
//
// GetSettings never returns secrets - the password is behind GetSecrets, which
// nothing in zde calls.
func (m *NM) profileSSID(p dbus.ObjectPath) (string, bool) {
	settings, err := m.settingsOf(p)
	if err != nil {
		return "", false
	}
	wifi, ok := settings[groupWifi]
	if !ok {
		return "", false
	}
	var ssid []byte
	if v, ok := wifi["ssid"]; ok {
		v.Store(&ssid) //nolint:errcheck // a profile with an unreadable ssid is one this cannot offer
	}
	return printableSSID(ssid)
}

func (m *NM) settingsOf(p dbus.ObjectPath) (map[string]map[string]dbus.Variant, error) {
	var settings map[string]map[string]dbus.Variant
	err := m.call(p, profIface+".GetSettings", []any{&settings})
	return settings, err
}

// accessPoint is the strongest access point broadcasting an ssid, with the
// bytes it broadcasts it as.
//
// The bytes and not the string: a new profile is made with the ssid as
// NetworkManager stores it, and re-encoding the name we printed would be a
// second guess at something we already have exactly right.
func (m *NM) accessPoint(dev dbus.ObjectPath, ssid string) (dbus.ObjectPath, []byte, error) {
	var aps []dbus.ObjectPath
	if err := m.call(dev, wifiIface+".GetAllAccessPoints", []any{&aps}); err != nil {
		return "", nil, err
	}
	best, bestBytes, bestSignal := dbus.ObjectPath(""), []byte(nil), -1
	for _, p := range aps {
		props, err := m.props(p, apIface)
		if err != nil {
			continue
		}
		broadcast := raw(props, "Ssid")
		name, ok := printableSSID(broadcast)
		if !ok || name != ssid {
			continue
		}
		if s := num(props, "Strength"); s > bestSignal {
			best, bestBytes, bestSignal = p, broadcast, s
		}
	}
	return best, bestBytes, nil
}

// Connect joins a network, and says what became of it.
//
// The secret is used and dropped: it goes into one D-Bus message and is not
// kept, logged, or put in an error. Every error out of here names the network
// and never the password (see the tests in internal/zded, which read what the
// daemon wrote down).
func (m *NM) Connect(ssid, secret string) error {
	dev, err := m.wifiDevice()
	if err != nil {
		return err
	}
	if dev == "" {
		return errors.New("this machine has no wifi radio")
	}
	ap, apSSID, err := m.accessPoint(dev, ssid)
	if err != nil {
		return err
	}
	if ap == "" {
		// Out of range, or not broadcasting. Said before anything is attempted,
		// because NetworkManager's own answer to this arrives late and reads
		// like a failure to authenticate.
		return fmt.Errorf("no network in range is called %q", ssid)
	}
	profile := m.savedProfiles()[ssid]
	var added, active dbus.ObjectPath
	if profile != "" {
		if secret != "" {
			// A password for a network that already has a profile: the password
			// changed, or the saved one was wrong. Updating beats adding a
			// second profile for the same network, which is what would
			// otherwise pile up, and it keeps whatever else that profile says -
			// a static address, a metric - which deleting it would throw away.
			if err := m.setSecret(profile, secret); err != nil {
				return err
			}
		}
		if err := m.call(nmPath, nmIface+".ActivateConnection", []any{&active}, profile, dev, ap); err != nil {
			return err
		}
	} else if err := m.call(nmPath, nmIface+".AddAndActivateConnection", []any{&added, &active},
		wifiSettings(ssid, apSSID, secret), dev, ap); err != nil {
		return err
	}
	err = m.settle(dev, active)
	if err != nil && !errors.Is(err, ErrStillTrying) && added != "" {
		// The profile this call created did not work. Left on disk it would be
		// a saved network with a wrong password, and a saved network is exactly
		// the one nothing asks a password for - so the next attempt would use
		// the bad one for ever. Only ever the profile added here: one that was
		// already on the machine is somebody's, and may be carrying more than a
		// password.
		m.call(added, profIface+".Delete", nil) //nolint:errcheck // already reporting the join's failure, which is the useful half
	}
	return err
}

// wifiSettings is a new profile, in the shape NetworkManager's Settings takes.
func wifiSettings(ssid string, apSSID []byte, secret string) map[string]map[string]dbus.Variant {
	if len(apSSID) == 0 {
		apSSID = []byte(ssid)
	}
	settings := map[string]map[string]dbus.Variant{
		groupConn: {
			"id":   dbus.MakeVariant(ssid),
			"type": dbus.MakeVariant(groupWifi),
		},
		groupWifi: {
			"ssid": dbus.MakeVariant(apSSID),
		},
	}
	if secret != "" {
		// wpa-psk covers WPA2 and WPA3-personal transition mode, which is what
		// a home or a cafe has. Enterprise (802.1x) is a different conversation
		// and is not attempted here: it wants a certificate, an identity and an
		// inner method, and guessing at those produces a profile that fails in
		// a way nobody can read.
		settings[groupSec] = map[string]dbus.Variant{
			"key-mgmt": dbus.MakeVariant("wpa-psk"),
			"psk":      dbus.MakeVariant(secret),
		}
	}
	return settings
}

// setSecret puts a new password on a profile that already exists.
func (m *NM) setSecret(profile dbus.ObjectPath, secret string) error {
	settings, err := m.settingsOf(profile)
	if err != nil {
		return err
	}
	sec, ok := settings[groupSec]
	if !ok {
		sec = map[string]dbus.Variant{}
	}
	if _, ok := sec["key-mgmt"]; !ok {
		sec["key-mgmt"] = dbus.MakeVariant("wpa-psk")
	}
	sec["psk"] = dbus.MakeVariant(secret)
	settings[groupSec] = sec
	return m.call(profile, profIface+".Update", nil, settings)
}

// How long a join is waited on, and how often it is asked about.
//
// Short, because this is answered on the socket a keybind is waiting on: the
// client gives a call five seconds (internal/zded, Client.Call) and a person
// gives it fewer. Association is the fast half and an address is the slow one,
// so a wait this long catches the refusals NetworkManager makes its mind up
// about quickly - a wrong password usually, a network that vanished - and
// hands back ErrStillTrying for the rest.
//
// What it does not catch is a refusal NetworkManager takes twenty seconds to
// decide, which happens when the supplicant retries. That comes back as "still
// trying", and the surface then watches the link rather than the call. Pushing
// the verdict as an event when it finally lands is the fix, and it is not built.
const (
	joinWait  = 3500 * time.Millisecond
	joinCheck = 150 * time.Millisecond
)

// settle waits for NetworkManager to make up its mind about one attempt.
//
// The active connection is watched rather than the device, because the device
// is still reading ACTIVATED from the network you are leaving for the first
// moments of joining another - a race that reports a join as done before it
// has begun. The device is asked only for the reason, which is the one thing
// the active connection does not carry.
func (m *NM) settle(dev, active dbus.ObjectPath) error {
	if active == "" || active == "/" {
		// Nothing to watch. NetworkManager always hands back a handle for an
		// activation it accepted, so this is a shape nobody has seen - and
		// asking the bus about an empty path would answer about the path
		// rather than about the network.
		return ErrStillTrying
	}
	deadline := time.Now().Add(joinWait)
	for {
		var state uint32
		err := m.prop(active, actIface, "State", &state)
		switch {
		case gone(err):
			// The active connection is removed when an attempt fails, so its
			// absence is the failure. The device kept the reason.
			return errors.New(m.whyItFailed(dev))
		case err != nil:
			return err
		case state == activeActivated:
			return nil
		case state == activeDeactivated:
			return errors.New(m.whyItFailed(dev))
		}
		// Past IP config the password has been accepted, and what is left is an
		// address. That is a joined network by every measure a person has, and
		// waiting for DHCP here would spend the whole budget on the half that
		// cannot fail for a reason anybody can act on.
		if s, _ := m.deviceState(dev); s >= stateIPConfig && s <= stateActivated {
			return nil
		}
		if !time.Now().Before(deadline) {
			return ErrStillTrying
		}
		time.Sleep(joinCheck)
	}
}

// whyItFailed is the device's own account of what went wrong, in words.
func (m *NM) whyItFailed(dev dbus.ObjectPath) string {
	state, reason := m.deviceState(dev)
	if state == stateFailed || reason != 0 {
		return refusal(reason)
	}
	return "NetworkManager did not join it, and gave no reason"
}

// deviceState is the device's state and the reason it is in it. StateReason
// carries both from one read; a NetworkManager that will not answer it still
// answers State, and a state with no reason is better than neither.
func (m *NM) deviceState(dev dbus.ObjectPath) (state, reason int) {
	var sr struct {
		State  uint32
		Reason uint32
	}
	if err := m.prop(dev, devIface, "StateReason", &sr); err == nil {
		return int(sr.State), int(sr.Reason)
	}
	var s uint32
	if err := m.prop(dev, devIface, "State", &s); err == nil {
		return int(s), 0
	}
	return 0, 0
}

// Disconnect drops the wifi link and leaves the profile where it is, so the
// network can be rejoined without typing anything again.
func (m *NM) Disconnect() error {
	dev, err := m.wifiDevice()
	if err != nil {
		return err
	}
	if dev == "" {
		return errors.New("this machine has no wifi radio")
	}
	var active dbus.ObjectPath
	if err := m.prop(dev, devIface, "ActiveConnection", &active); err != nil {
		return err
	}
	if active == "" || active == "/" {
		return errors.New("no wifi connection to drop")
	}
	return m.call(nmPath, nmIface+".DeactivateConnection", nil, active)
}

// gone reports whether an error is D-Bus for "that object is not there any
// more", which is how a failed activation ends: NetworkManager removes the
// active connection with it. Told apart from every other error because the two
// mean opposite things - one is an answer, the other is a bus that is unwell.
func gone(err error) bool {
	if err == nil {
		return false
	}
	var derr dbus.Error
	if !errors.As(err, &derr) {
		return false
	}
	return strings.HasPrefix(derr.Name, "org.freedesktop.DBus.Error.Unknown")
}
