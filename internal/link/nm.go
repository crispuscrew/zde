package link

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crispuscrew/zde/internal/bus"
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

	// A radio that is up and joined to nothing. Worth naming because it is what
	// an idle second radio reads as, and telling it from one that is not there
	// to be used is how the right radio gets asked.
	stateDisconnected = 30
	// Past this the password has been accepted and what is left is an address.
	stateIPConfig  = 70
	stateActivated = 100
	stateFailed    = 120

	// NMActiveConnectionState.
	activeActivated   = 2
	activeDeactivated = 4
)

// askFor bounds a whole question put to NetworkManager, and not each call
// inside it. zded answers keybinds on one socket, and a bus call with no
// deadline is a keypress that never comes back if NetworkManager wedges -
// which it can, waiting on a driver.
//
// One budget for the question, because the arithmetic of the other way is
// worth writing down. Listing a room with thirty access points and eight saved
// networks is about forty-five calls: the device list, a property read per
// device, the access points, a property read for each of them, and one
// GetSettings per saved profile. A deadline per call makes the ceiling
// forty-five times this, against a client that gives up after five seconds
// (internal/zded, Client.Call) - so on a wedged NetworkManager the caller sees
// a timeout while zded grinds on for a minute and a half behind it, once per
// keypress. With one budget the ceiling is the number written here.
//
// It buys nothing in the ordinary case: forty-five calls on a local system bus
// are milliseconds. It is the wedged case it is for, which is the only one
// where any of these numbers matter.
const askFor = 2 * time.Second

// within opens a budget for one question. Every call made under it shares the
// deadline, so a question is bounded by askFor however many calls it takes.
func (m *NM) within() (context.Context, context.CancelFunc) {
	return m.until(time.Now().Add(askFor))
}

// until is within for a question that already knows when it has to be over: a
// join, which is a sequence of calls and then a wait, and which has to answer
// before the caller stops listening. No call runs past askFor, and none runs
// past the end of the thing it belongs to.
func (m *NM) until(end time.Time) (context.Context, context.CancelFunc) {
	if soon := time.Now().Add(askFor); soon.Before(end) {
		end = soon
	}
	return context.WithDeadline(context.Background(), end)
}

// carryOn decides what an object that will not answer means.
//
// One that went away between the listing and the question is skipped, and
// carrying on is right: a USB dongle being unplugged and an access point that
// stopped broadcasting are both ordinary. A budget that has run out is not,
// because every remaining object would be skipped the same way and the answer
// would be half a room with nothing on it to say that it was half.
func carryOn(ctx context.Context, what string) error {
	if ctx.Err() == nil {
		return nil
	}
	return fmt.Errorf("NetworkManager did not finish saying %s within %s", what, askFor)
}

// NM is NetworkManager on the system bus.
type NM struct {
	conn *dbus.Conn
	// absent is set when the bus says nobody answers to NetworkManager's name
	// any more. The connection does not die with the service - it is a
	// connection to the bus, not to NetworkManager - so without this a daemon
	// that was stopped mid-session reads as a manager that has stopped
	// answering, and the bar says zded is broken about a machine where nothing
	// is.
	absent atomic.Bool
	// watchers is the joins still being watched after the call that started
	// them answered. Nothing in production waits on it; the tests do, because
	// what happens behind the answer is most of what a join does.
	watchers sync.WaitGroup
}

// Open connects, or says that there is nothing here to connect to.
//
// It returns the interface rather than the type because the absence of a
// manager is one of the answers: the caller wants "there is no NetworkManager"
// as a state, and this is the one place that can tell it apart from a bus that
// is there and unhappy.
func Open() (Manager, error) {
	// Bounded, because everything else here is and the connect was the one part
	// that was not (internal/bus). This is called from the bar's poll: a system
	// bus that accepts the socket and then says nothing would otherwise hold the
	// daemon's link lock for as long as it liked, once every five seconds.
	conn, err := bus.System()
	if err != nil {
		// No system bus at all, or one that will not finish saying hello. A
		// machine can be perfectly online like that; what it cannot be is asked
		// about it this way.
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
func (m *NM) Alive() bool { return m.conn != nil && m.conn.Connected() && !m.absent.Load() }

// Close gives the bus connection back. A manager that is dropped and not closed
// leaks the socket and the two goroutines the bus library runs on it, once per
// reopen - and reopening is what happens every time NetworkManager restarts.
func (m *NM) Close() error {
	if m.conn == nil {
		return nil
	}
	return m.conn.Close()
}

func (m *NM) call(ctx context.Context, path dbus.ObjectPath, method string, out []any, args ...any) error {
	c := m.conn.Object(nmService, path).CallWithContext(ctx, method, 0, args...)
	if c.Err != nil {
		return m.noteAbsence(c.Err)
	}
	if len(out) == 0 {
		return nil
	}
	return c.Store(out...)
}

// props reads every property of one interface in one call. One round trip per
// object rather than five: a busy room is thirty access points, and thirty
// objects at five calls each is a widget that opens slowly for no reason.
func (m *NM) props(ctx context.Context, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	var out map[string]dbus.Variant
	err := m.call(ctx, path, propsGetAll, []any{&out}, iface)
	return out, err
}

func (m *NM) prop(ctx context.Context, path dbus.ObjectPath, iface, name string, into any) error {
	var v dbus.Variant
	if err := m.call(ctx, path, propsGet, []any{&v}, iface, name); err != nil {
		return err
	}
	return v.Store(into)
}

// noteAbsence turns the bus saying "nobody answers to that name" into the
// absence of a manager, and remembers it so the daemon drops this connection
// and finds out for itself next time (internal/zded, links).
//
// It is the one case where NetworkManager going away is invisible from the
// socket: the connection is to the bus, and the bus is still there.
func (m *NM) noteAbsence(err error) error {
	var derr dbus.Error
	if !errors.As(err, &derr) {
		return err
	}
	switch derr.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner":
		m.absent.Store(true)
		return fmt.Errorf("%w: %s", ErrNoManager, derr.Name)
	}
	return err
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

func (m *NM) devices(ctx context.Context) ([]device, error) {
	var paths []dbus.ObjectPath
	if err := m.call(ctx, nmPath, nmIface+".GetDevices", []any{&paths}); err != nil {
		return nil, err
	}
	out := make([]device, 0, len(paths))
	for _, p := range paths {
		props, err := m.props(ctx, p, devIface)
		if err != nil {
			// A device that went away between the list and the question - a
			// USB dongle being unplugged is exactly this. One missing device is
			// not a reason to have no answer about the others, and a spent
			// budget is not a machine with no radio in it.
			if err := carryOn(ctx, "which devices it has"); err != nil {
				return nil, err
			}
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
	ctx, cancel := m.within()
	defer cancel()
	devs, err := m.devices(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{Kind: KindNone}
	// Whether zde (or anything else) has NetworkManager's networking switch
	// off. Asked here rather than in a verb of its own because every reader of
	// a link already asks this question - the bar, the widget, `zde net status`
	// - and a cut machine looks exactly like a broken one without it. One
	// property read on the same budget as the rest.
	//
	// A NetworkManager that will not answer it is not a reason to have no link:
	// what is missing is why there is no network, not whether there is one.
	var enabled bool
	if err := m.prop(ctx, nmPath, nmIface, "NetworkingEnabled", &enabled); err == nil {
		st.Killed = !enabled
	}
	// The same radio List and Connect will use, from the same reading, so that
	// the bar and the widget cannot end up describing different devices.
	wifi := pickWifi(devs)
	st.Wifi = wifi.path != ""
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
		st.SSID, st.Signal = m.activeAP(ctx, wifi.path)
	}
	return st, nil
}

// activeAP is the name and strength of what a wifi device is on.
func (m *NM) activeAP(ctx context.Context, dev dbus.ObjectPath) (string, int) {
	var ap dbus.ObjectPath
	if err := m.prop(ctx, dev, wifiIface, "ActiveAccessPoint", &ap); err != nil || ap == "" || ap == "/" {
		return "", 0
	}
	props, err := m.props(ctx, ap, apIface)
	if err != nil {
		return "", 0
	}
	ssid, _ := printableSSID(raw(props, "Ssid"))
	return ssid, num(props, "Strength")
}

// wifiDevice is the radio to ask, or the empty path on a machine with none.
func (m *NM) wifiDevice(ctx context.Context) (dbus.ObjectPath, error) {
	devs, err := m.devices(ctx)
	if err != nil {
		return "", err
	}
	return pickWifi(devs).path, nil
}

// pickWifi chooses between radios, because a laptop with a dongle plugged in
// has two and NetworkManager lists them in whatever order it has them.
//
// The connected one wins: it is the one carrying the traffic, so it is the one
// the bar is about and the one whose room the widget should list. Failing that,
// one that is up and joined to nothing beats one the kernel or NetworkManager
// has parked - an unmanaged or unavailable radio can be asked and answers
// nothing, which reads on the bar as a machine with no network on a machine
// that is online.
//
// Taking the first of them, which is what this did, meant an idle dongle
// enumerated before the built-in radio made the bar say "no network" over a
// working link, with nothing on screen to say which radio had been asked.
func pickWifi(devs []device) device {
	var first, idle device
	for _, d := range devs {
		if d.kind != typeWifi {
			continue
		}
		if d.state == stateActivated {
			return d
		}
		if first.path == "" {
			first = d
		}
		if idle.path == "" && d.state >= stateDisconnected {
			idle = d
		}
	}
	if idle.path != "" {
		return idle
	}
	return first
}

// List is what the radio can see.
//
// A call per access point, plus one per saved profile: about forty-five of them
// in a busy building, all under one budget (see askFor). Most of that can be
// flattened and is not, yet. NetworkManager implements ObjectManager at
// /org/freedesktop, and one GetManagedObjects returns every device and every
// access point with all their properties - which is the device list, the
// property read per device, the access point list and the property read per
// access point, in one call. What it cannot flatten is the saved half: the
// settings of a profile come from a method and not a property, so the
// GetSettings per saved network stays whatever else changes.
//
// Not done here because none of it can be tried on a machine with no
// NetworkManager on it, and it would replace the four entry points at once on
// the shape of a reply nothing in this checkout can print. It wants a laptop
// under it, and it is worth roughly forty of the forty-five calls when it gets
// one.
func (m *NM) List() ([]Network, error) {
	ctx, cancel := m.within()
	defer cancel()
	dev, err := m.wifiDevice(ctx)
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
	m.call(ctx, dev, wifiIface+".RequestScan", nil, map[string]dbus.Variant{}) //nolint:errcheck // rate limited or not permitted: the cache is still an answer

	var aps []dbus.ObjectPath
	if err := m.call(ctx, dev, wifiIface+".GetAllAccessPoints", []any{&aps}); err != nil {
		return nil, err
	}
	saved, err := m.savedProfiles(ctx)
	if err != nil {
		return nil, err
	}
	here, _ := m.activeAP(ctx, dev)
	seen := make([]Network, 0, len(aps))
	for _, p := range aps {
		props, err := m.props(ctx, p, apIface)
		if err != nil {
			// Gone since the list was taken, or the budget with it.
			if err := carryOn(ctx, "what is in range"); err != nil {
				return nil, err
			}
			continue
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
func (m *NM) savedProfiles(ctx context.Context) (map[string]dbus.ObjectPath, error) {
	out := map[string]dbus.ObjectPath{}
	var profiles []dbus.ObjectPath
	if err := m.call(ctx, setPath, setIface+".ListConnections", []any{&profiles}); err != nil {
		return nil, err
	}
	for _, p := range profiles {
		ssid, ok := m.profileSSID(ctx, p)
		if !ok {
			// A profile that would not answer is skipped like any other object
			// that went away - and a budget that ran out is not, for the reason
			// carryOn gives. This column is not cosmetic: it decides whether
			// anybody is asked for a password, and whether a join adds a second
			// profile for a network that already has one.
			if err := carryOn(ctx, "which networks it has saved"); err != nil {
				return nil, err
			}
			continue
		}
		if _, had := out[ssid]; !had {
			out[ssid] = p
		}
	}
	return out, nil
}

// profileSSID is the network a saved profile is for, and false for a profile
// that is not wifi.
//
// GetSettings never returns secrets - the password is behind GetSecrets, which
// nothing in zde calls.
func (m *NM) profileSSID(ctx context.Context, p dbus.ObjectPath) (string, bool) {
	settings, err := m.settingsOf(ctx, p)
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

func (m *NM) settingsOf(ctx context.Context, p dbus.ObjectPath) (map[string]map[string]dbus.Variant, error) {
	var settings map[string]map[string]dbus.Variant
	err := m.call(ctx, p, profIface+".GetSettings", []any{&settings})
	return settings, err
}

// accessPoint is the strongest access point broadcasting an ssid, with the
// bytes it broadcasts it as.
//
// The bytes and not the string: a new profile is made with the ssid as
// NetworkManager stores it, and re-encoding the name we printed would be a
// second guess at something we already have exactly right.
func (m *NM) accessPoint(ctx context.Context, dev dbus.ObjectPath, ssid string) (dbus.ObjectPath, []byte, error) {
	var aps []dbus.ObjectPath
	if err := m.call(ctx, dev, wifiIface+".GetAllAccessPoints", []any{&aps}); err != nil {
		return "", nil, err
	}
	best, bestBytes, bestSignal := dbus.ObjectPath(""), []byte(nil), -1
	for _, p := range aps {
		props, err := m.props(ctx, p, apIface)
		if err != nil {
			if err := carryOn(ctx, "what is in range"); err != nil {
				return "", nil, err
			}
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
//
// The whole of it - working out what to activate, asking, and waiting for the
// answer - fits inside connectWithin, because a verdict that arrives after the
// caller has gone is a verdict nobody reads.
func (m *NM) Connect(ssid, secret string) (err error) {
	// The last check on the way out, on every path at once. Everything below
	// is written not to put the password in an error, and this is the one place
	// where the password and the error are both in hand to prove it.
	defer func() { err = withoutSecret(err, secret) }()

	end := time.Now().Add(connectWithin)
	ctx, cancel := m.until(end)
	defer cancel()

	dev, err := m.wifiDevice(ctx)
	if err != nil {
		return err
	}
	if dev == "" {
		return errors.New("this machine has no wifi radio")
	}
	ap, apSSID, err := m.accessPoint(ctx, dev, ssid)
	if err != nil {
		return err
	}
	if ap == "" {
		// Out of range, or not broadcasting. Said before anything is attempted,
		// because NetworkManager's own answer to this arrives late and reads
		// like a failure to authenticate.
		return fmt.Errorf("no network in range is called %q", ssid)
	}
	saved, err := m.savedProfiles(ctx)
	if err != nil {
		return err
	}
	profile := saved[ssid]

	a := attempt{dev: dev, ssid: ssid, offered: secret != ""}
	if profile != "" {
		if secret != "" {
			// A password for a network NetworkManager already has a profile
			// for. This used to write the new one into that profile before
			// anything was tried, which is a destructive edit on a guess: one
			// typo and a working network is gone, with nothing holding the old
			// password because GetSettings does not return secrets.
			//
			// So it is refused, and the refusal names the way out. Forgetting
			// is a deliberate act, it is one key on the surface, and it is the
			// only thing here that deletes a profile a person did not create
			// with this widget.
			return fmt.Errorf("%s is already saved, and this will not overwrite "+
				"what is saved: forget it first, then join it again", ssid)
		}
		if err := m.call(ctx, nmPath, nmIface+".ActivateConnection", []any{&a.active}, profile, dev, ap); err != nil {
			return err
		}
	} else {
		if err := m.call(ctx, nmPath, nmIface+".AddAndActivateConnection", []any{&a.added, &a.active},
			wifiSettings(ssid, apSSID, secret), dev, ap); err != nil {
			return err
		}
	}

	err = m.settle(a, end)
	if err != nil {
		// The answer goes back now and the watching carries on behind it,
		// whatever the answer was. A refusal is usually not in yet - the
		// supplicant retries for longer than anybody can be kept waiting - and
		// when it lands, the profile this call created has to go with it. One
		// path rather than a delete here and a watcher for the rest, because
		// two ways to delete a profile is one more than anybody can hold in
		// their head about a thing that destroys something.
		m.watch(a)
	}
	return err
}

// attempt is one join being watched: what was asked for, and where the answer
// will come from. Together rather than four arguments, because every one of
// these is needed to read a verdict and to act on it, and the two that decide
// whether a profile is deleted are the easiest to pass in the wrong order.
type attempt struct {
	dev    dbus.ObjectPath
	active dbus.ObjectPath
	// added is the profile this join created, and the empty path when it
	// activated one that was already there. Only ever this one is deleted.
	added dbus.ObjectPath
	ssid  string
	// offered is whether a password went with the request, which is the
	// difference between "the password was refused" and "this network wants
	// one" for the same reason code.
	offered bool
}

// refused is NetworkManager's own verdict on an attempt, as opposed to a bus
// that would not answer or a budget that ran out.
//
// A type and not a string, because one thing turns on the difference: whether
// the profile this join created is deleted. A verdict means it is wrong and
// must go; anything else means nobody knows yet, and deleting it would end a
// join that was still going.
type refused struct{ why string }

func (r refused) Error() string { return r.why }

// forget deletes a profile, on a budget of its own. Errors go nowhere useful:
// this runs when something else has already failed, and the failure is the half
// worth reporting.
func (m *NM) forget(profile dbus.ObjectPath) {
	if profile == "" {
		return
	}
	ctx, cancel := m.within()
	defer cancel()
	m.call(ctx, profile, profIface+".Delete", nil) //nolint:errcheck // see above
}

// Forget drops a saved network, which is how a password that has changed gets
// typed again: the widget asks for one only when there is no profile, so
// without this a network with a wrong password saved against it can never be
// joined from zde again.
func (m *NM) Forget(ssid string) error {
	ctx, cancel := m.within()
	defer cancel()
	saved, err := m.savedProfiles(ctx)
	if err != nil {
		return err
	}
	profile, ok := saved[ssid]
	if !ok {
		return fmt.Errorf("no saved network is called %q", ssid)
	}
	return m.call(ctx, profile, profIface+".Delete", nil)
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

// The clock a join runs against.
//
// callerWaits is what zded's client gives any call (internal/zded, Client.Call).
// connectWithin is what a join is allowed of it, with the rest left for the
// socket and for zded's own work: a join that answers at five seconds and one
// that never answers are the same thing to whoever pressed the key, and the
// widget would show a timeout for a join that was about to work.
//
// Association is the fast half of joining and an address is the slow one, so
// this catches the refusals NetworkManager makes its mind up about quickly and
// hands back ErrStillTrying for the rest. A wrong password is usually in the
// second group: the supplicant retries. That is what watch is for.
const (
	callerWaits   = 5 * time.Second
	connectWithin = 4 * time.Second
	joinCheck     = 150 * time.Millisecond

	// watchFor is how long the verdict is waited for after the call has
	// answered. Longer than any of the above, because nothing is waiting on it
	// - and bounded, because a goroutine per keypress that never ends is a leak
	// with a keyboard shortcut. NetworkManager gives up on a wifi association
	// well inside a minute.
	watchFor   = 60 * time.Second
	watchEvery = time.Second
)

// watch keeps looking after the call has answered, and deletes the profile this
// join created if the verdict turns out to be no.
//
// This is the half of a wrong password that does not fit in a keypress. The
// call says "still trying" and somebody reads that; a minute later
// NetworkManager gives up, and without this the profile it left behind is a
// saved network with a wrong password in it - which is the one thing nothing
// ever asks a password for again.
//
// Nothing is told when this finishes. It writes to NetworkManager and to
// nothing in zde, so there is no state to race with, and the next question
// about the link asks NetworkManager rather than this.
func (m *NM) watch(a attempt) {
	if a.added == "" {
		// Nothing to undo: this join activated a profile that was already
		// there, and that one is not ours to delete.
		return
	}
	m.watchers.Add(1)
	go func() {
		defer m.watchers.Done()
		end := time.Now().Add(watchFor)
		for time.Now().Before(end) {
			done, err := m.decided(a, end)
			switch {
			case done:
				return // it landed, and the profile is the machine's now
			case errors.As(err, &refused{}):
				m.forget(a.added)
				return
			case err != nil:
				// A bus that will not answer is not a verdict. Try again for as
				// long as there is time; if the whole window goes that way, the
				// profile stays, because deleting one on a guess is the failure
				// this is here to avoid rather than one to swap it for.
			}
			time.Sleep(watchEvery)
		}
	}()
}

// settle waits for NetworkManager to make up its mind about one attempt.
//
// The active connection is watched rather than the device, because the device
// is still reading ACTIVATED from the network you are leaving for the first
// moments of joining another. The device is asked as well, but only once it is
// on this attempt - see decided.
func (m *NM) settle(a attempt, end time.Time) error {
	if a.active == "" || a.active == "/" {
		// Nothing to watch. NetworkManager always hands back a handle for an
		// activation it accepted, so this is a shape nobody has seen - and
		// asking the bus about an empty path would answer about the path
		// rather than about the network.
		return ErrStillTrying
	}
	for {
		if !time.Now().Before(end) {
			return ErrStillTrying
		}
		done, err := m.decided(a, end)
		switch {
		case done:
			return nil
		case err == nil:
		case errors.Is(err, context.DeadlineExceeded) && !time.Now().Before(end):
			// Our own clock, not NetworkManager's answer: the wait ran out
			// while a read was in flight. That is still-trying by another name,
			// and reporting it as a bus error would put "context deadline
			// exceeded" on a widget about a join that is still going.
			return ErrStillTrying
		default:
			return err
		}
		time.Sleep(joinCheck)
	}
}

// decided is one look at whether the attempt is over, on a budget of its own -
// its own because the wait it belongs to is meant to span seconds and askFor is
// the bound on a call.
//
// Two objects, and the order matters. The activation says whether this attempt
// is done; the device says whether the password was accepted, which is the
// earlier moment and the one worth answering on, since what is left after it is
// an address and DHCP cannot fail for a reason anybody can act on.
//
// But the device is only asked about once it is on this attempt. Until then it
// is still describing the network being left - ACTIVATED, on the old link - and
// reading that as a verdict is how a join answered "joined" in a millisecond
// while the machine had not moved.
func (m *NM) decided(a attempt, end time.Time) (bool, error) {
	ctx, cancel := m.until(end)
	defer cancel()
	var state uint32
	err := m.prop(ctx, a.active, actIface, "State", &state)
	switch {
	case gone(err):
		// The active connection is removed when an attempt fails, so its
		// absence is the failure. The device kept the reason.
		return false, refused{m.whyItFailed(ctx, a)}
	case err != nil:
		return false, err
	case state == activeActivated:
		return true, nil
	case state == activeDeactivated:
		return false, refused{m.whyItFailed(ctx, a)}
	}
	var on dbus.ObjectPath
	if err := m.prop(ctx, a.dev, devIface, "ActiveConnection", &on); err != nil || on != a.active {
		return false, nil
	}
	if s, _ := m.deviceState(ctx, a.dev); s >= stateIPConfig && s <= stateActivated {
		return true, nil
	}
	if s, _ := m.deviceState(ctx, a.dev); s == stateFailed {
		return false, refused{m.whyItFailed(ctx, a)}
	}
	return false, nil
}

// whyItFailed is the device's own account of what went wrong, in words.
func (m *NM) whyItFailed(ctx context.Context, a attempt) string {
	state, reason := m.deviceState(ctx, a.dev)
	if state == stateFailed || reason != 0 {
		return refusal(reason, a.offered)
	}
	return "NetworkManager did not join it, and gave no reason"
}

// deviceState is the device's state and the reason it is in it. StateReason
// carries both from one read; a NetworkManager that will not answer it still
// answers State, and a state with no reason is better than neither.
func (m *NM) deviceState(ctx context.Context, dev dbus.ObjectPath) (state, reason int) {
	var sr struct {
		State  uint32
		Reason uint32
	}
	if err := m.prop(ctx, dev, devIface, "StateReason", &sr); err == nil {
		return int(sr.State), int(sr.Reason)
	}
	var s uint32
	if err := m.prop(ctx, dev, devIface, "State", &s); err == nil {
		return int(s), 0
	}
	return 0, 0
}

// Disconnect drops the wifi link and leaves the profile where it is, so the
// network can be rejoined without typing anything again.
func (m *NM) Disconnect() error {
	ctx, cancel := m.within()
	defer cancel()
	dev, err := m.wifiDevice(ctx)
	if err != nil {
		return err
	}
	if dev == "" {
		return errors.New("this machine has no wifi radio")
	}
	var active dbus.ObjectPath
	if err := m.prop(ctx, dev, devIface, "ActiveConnection", &active); err != nil {
		return err
	}
	if active == "" || active == "/" {
		return errors.New("no wifi connection to drop")
	}
	return m.call(ctx, nmPath, nmIface+".DeactivateConnection", nil, active)
}

// Kill cuts the network at NetworkManager's own switch, and puts it back.
//
// What it is: Enable(false), which is `nmcli networking off`. Every managed
// device is deactivated and brought down - wired and wifi together, and a
// bluetooth PAN with them - and every saved profile stays exactly where it was,
// so the way back is Enable(true) and nothing has to be typed again.
//
// What it deliberately is not is the radios. Blocking those means bluetooth
// goes with wifi, and on a laptop whose keyboard is bluetooth that is a machine
// nobody can undo this from - which is the one thing a reversible security
// action must not be. The reach is not there either: bluetooth's block is
// /dev/rfkill or bluez, and zde's user is in networkmanager and video and no
// other group (nix/live.nix).
//
// One switch and not two, for the same reason. NetworkManager's WirelessEnabled
// is a second bit that would have to be put back, and putting it back would
// silently un-block a radio somebody had blocked themselves before ever pressing
// this.
//
// The honest limit: this cuts what NetworkManager manages. A tunnel somebody
// built by hand, a container bridge, a tether nothing here knows about, carry on
// - and netview's per-app cut (0.3) is what reaches those.
//
// The switch is NetworkManager's own state and is read back rather than
// remembered (Status), which is what lets a bar say "zde cut this" after a
// restart of either side.
func (m *NM) Kill(cut bool) error {
	ctx, cancel := m.within()
	defer cancel()
	// polkit decides this, not us: enable-disable-network is a permission an
	// account has or does not, and NetworkManager's refusal is the sentence
	// worth passing on rather than one written here about a machine this code
	// cannot see.
	return m.call(ctx, nmPath, nmIface+".Enable", nil, !cut)
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
