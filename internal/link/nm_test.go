package link

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// The harness itself, asserted before anything is asserted with it: a fake that
// answers nothing would make every test below pass by describing an empty
// machine. This is the ordinary case end to end - Open finds the name, Status
// reads the link off the devices, and List reads the room - through the real
// bus, the real bus library and the real NM type.
//
// If this regresses, every other test in this file is checking a bus that is
// not there.
func TestTheFakeBusAnswersAsNetworkManagerDoes(t *testing.T) {
	f := home()
	m := serve(t, f)

	st, err := m.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Kind != KindWifi || st.SSID != "home" || st.Signal != 84 || !st.Wifi {
		t.Errorf("status = %+v, and the machine is on home at 84%%", st)
	}

	networks, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(networks) != 2 {
		t.Fatalf("list = %+v, and there are two networks in range", networks)
	}
	// Strongest first, the one you are on marked, and the saved column read off
	// the profiles rather than off the access points.
	if networks[0].SSID != "home" || !networks[0].Active || !networks[0].Saved || !networks[0].Secure {
		t.Errorf("home reads as %+v", networks[0])
	}
	if networks[1].SSID != "cafe" || networks[1].Saved || networks[1].Active {
		t.Errorf("cafe reads as %+v", networks[1])
	}
	// And it really did ask: the scan, the access points, and the settings of
	// every saved profile.
	for _, member := range []string{"RequestScan", "GetAllAccessPoints", "GetSettings"} {
		if !f.askedAny(member) {
			t.Errorf("nothing called %s, so this fake is not being driven", member)
		}
	}
}

// A join is not joined while the device is still on the network you are
// leaving. NetworkManager hands back an activation object in ACTIVATING and
// leaves the device reading ACTIVATED for the old link for the first moments
// after that, which is exactly where the first look lands.
//
// If this regresses, `zde net connect cafe` prints "joined cafe" in a
// millisecond, the widget closes saying the same, and the bar goes on saying
// home - which is the one lie the bar's whole known/unknown pattern exists to
// prevent, arriving through the front door.
func TestAJoinIsNotJoinedWhileTheDeviceIsStillOnTheOldNetwork(t *testing.T) {
	f := home()
	m := serve(t, f)

	start := time.Now()
	err := m.Connect("cafe", "hunter2")
	if err == nil {
		t.Fatalf("joining cafe answered success in %s, with the device still on home", time.Since(start))
	}
	if !errors.Is(err, ErrStillTrying) {
		t.Errorf("joining cafe = %v, and NetworkManager had not decided", err)
	}
	// And the link is still what it was, which is what the caller would have
	// been contradicting.
	st, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.SSID != "home" {
		t.Errorf("the link reads %q after a join that never landed", st.SSID)
	}
}

// The device state is worth reading once the device is on this attempt: that is
// what makes a join answer "joined" without waiting for DHCP. What it must not
// be is read while it is still describing the network being left.
//
// If this regresses, either a join reports too early (the test above) or every
// successful join waits out the whole three and a half seconds and reports
// "still trying", which is a widget that never says a good word about anything.
func TestAJoinIsJoinedOnceTheDeviceIsOnThisAttempt(t *testing.T) {
	f := home()
	m := serve(t, f)

	// The device moves onto the new activation, past authentication, the way
	// NetworkManager reports it: IP config, address pending.
	go func() {
		waitForCall(f, "AddAndActivateConnection")
		active := lastActive(f)
		f.set(wifiPath, devIface, "ActiveConnection", active)
		f.set(wifiPath, devIface, "State", uint32(stateIPConfig))
		f.set(wifiPath, devIface, "StateReason", stateReason{uint32(stateIPConfig), 0})
	}()

	if err := m.Connect("cafe", "hunter2"); err != nil {
		t.Fatalf("joining cafe = %v, and the device was on it past authentication", err)
	}
}

// A profile this created for a join that never landed is not left behind.
//
// A wrong password is the case: NetworkManager takes longer to give up than a
// keypress can wait, so the call answers "still trying" and the verdict lands
// afterwards. Left alone, the profile stays saved - and a saved network is the
// one nothing ever asks a password for again, so the widget would offer the
// wrong password for ever with no way to type another.
//
// If this regresses, one typo makes a network permanently unjoinable from zde.
func TestAProfileIsNotLeftBehindByAJoinTheAccessPointRefused(t *testing.T) {
	f := home()
	m := serve(t, f)

	// The verdict arrives after the call has given up waiting: the supplicant
	// tried, and NetworkManager put the device in FAILED with NO_SECRETS.
	go func() {
		waitForCall(f, "AddAndActivateConnection")
		time.Sleep(50 * time.Millisecond)
		active := lastActive(f)
		f.set(active, actIface, "State", uint32(activeDeactivated))
		f.set(wifiPath, devIface, "StateReason", stateReason{uint32(stateFailed), 7})
		f.set(wifiPath, devIface, "State", uint32(stateFailed))
	}()

	err := m.Connect("cafe", "wrong-password")
	if err == nil {
		t.Fatal("a refused join answered as success")
	}
	// The verdict was in before the call answered, so the caller is told what
	// it was rather than that nobody knows yet. This is the difference between
	// a widget that says "the password was refused" and one that shrugs.
	if errors.Is(err, ErrStillTrying) {
		t.Errorf("NetworkManager had refused it and the answer was %v", err)
	}
	if !strings.Contains(err.Error(), "password was refused") {
		t.Errorf("the answer does not say what NetworkManager said: %v", err)
	}
	waitFor(t, "the profile this join added being deleted", func() bool {
		return f.askedAny("Delete")
	})
	// And it is gone from the list, so nothing reads it as saved afterwards.
	networks, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range networks {
		if n.SSID == "cafe" && n.Saved {
			t.Error("cafe still reads as saved after the password was refused")
		}
	}
}

// The verdict on a wrong password usually arrives after the call has answered:
// the supplicant retries for longer than a keypress can be kept waiting, so the
// join comes back "still trying" and NetworkManager gives up a minute later.
// The profile it created has to go then too.
//
// If this regresses, the ordinary wrong password - the one this whole widget
// exists for - leaves a saved network with the wrong password in it, and a
// saved network is the one nothing ever asks a password for again. Which is
// exactly the failure the commit that added this claimed it had designed out.
func TestAProfileIsNotLeftBehindByAVerdictThatArrivesLate(t *testing.T) {
	f := home()
	m := serve(t, f)

	err := m.Connect("cafe", "wrong-password")
	if !errors.Is(err, ErrStillTrying) {
		t.Fatalf("joining cafe = %v, and NetworkManager had not answered", err)
	}
	if !f.askedAny("AddAndActivateConnection") {
		t.Fatal("no profile was added, so there is nothing for this to be about")
	}
	if f.askedAny("Delete") {
		t.Fatal("the profile was deleted before anybody had decided anything")
	}

	// NetworkManager gives up, after the call has gone.
	active := lastActive(f)
	f.set(active, actIface, "State", uint32(activeDeactivated))
	f.set(wifiPath, devIface, "StateReason", stateReason{uint32(stateFailed), 7})
	f.set(wifiPath, devIface, "State", uint32(stateFailed))

	waitFor(t, "the profile being deleted once the verdict landed", func() bool {
		return f.askedAny("Delete")
	})
}

// A profile somebody already had is never written to. It may carry a static
// address, a metric, a password that is right - and none of that is this
// widget's to overwrite on a guess.
//
// If this regresses, one typo at the prompt destroys a working saved network
// with nothing holding the old password, because GetSettings never returns it.
func TestASavedProfileIsNeverOverwritten(t *testing.T) {
	f := home()
	m := serve(t, f)

	err := m.Connect("home", "a-typo")
	if err == nil {
		t.Fatal("a password for a saved network was taken as if it could be tried")
	}
	if !strings.Contains(err.Error(), "forget") {
		t.Errorf("the refusal does not say how to get out of it: %v", err)
	}
	if f.asked(homeConn, "Update") {
		t.Error("the saved profile was written to")
	}
	// The passphrase that was there is still there.
	f.mu.Lock()
	psk := f.settings[homeConn][groupSec]["psk"].Value()
	f.mu.Unlock()
	if psk != "the-real-passphrase" {
		t.Errorf("the stored passphrase is now %v", psk)
	}
}

// Forgetting is the way out, and the only thing that deletes a profile a person
// did not ask to have deleted.
//
// If this regresses, there is no way to change a saved password from inside
// zde: the widget will not prompt for a network it thinks is saved.
func TestForgettingIsHowASavedPasswordIsChanged(t *testing.T) {
	f := home()
	m := serve(t, f)

	if err := m.Forget("home"); err != nil {
		t.Fatalf("forgetting home: %v", err)
	}
	if !f.asked(homeConn, "Delete") {
		t.Error("forgetting home deleted nothing")
	}
	networks, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range networks {
		if n.SSID == "home" && n.Saved {
			t.Error("home still reads as saved after being forgotten")
		}
	}
	// And a network nobody has heard of is a refusal rather than a silence.
	if err := m.Forget("nowhere"); err == nil {
		t.Error("forgetting a network with no profile answered as if it did something")
	}
}

// A bus that went slow is not a join that failed. The budget running out says
// nothing about what NetworkManager will do with the request it already has -
// so deleting the profile there would be zde killing a join that was going
// fine, and deleting a connection is how NetworkManager is told to drop it.
//
// If this regresses, a stalled system bus turns every join into
// "context deadline exceeded" plus a network that has to be typed in again.
func TestABusThatWillNotAnswerDoesNotDeleteTheProfile(t *testing.T) {
	f := home()
	m := serve(t, f)

	// The bus stops answering once the profile exists, which is the only
	// arrangement that tests anything: one that was broken from the first call
	// never gets as far as adding a profile, and the test would pass by never
	// having been where the mistake is.
	go func() {
		waitForCall(f, "AddAndActivateConnection")
		f.setFail("Get.State", "org.freedesktop.DBus.Error.NoReply")
	}()

	err := m.Connect("cafe", "hunter2")
	if err == nil {
		t.Fatal("a join nobody decided answered as success")
	}
	if !f.askedAny("AddAndActivateConnection") {
		t.Fatal("the bus broke before the profile was added, so this proves nothing")
	}
	// Whatever it says, the profile stays. Nothing about a bus that will not
	// answer says what NetworkManager is doing with the request it already
	// has - and deleting a connection is how NetworkManager is told to drop
	// one, so guessing here would be zde ending a join that was going fine.
	//
	// Watched for longer than the watcher's own interval, so that a watcher
	// which deletes on any error has had two chances to.
	neverWithin(t, 3*watchEvery, "a bus that would not answer deleted the profile", func() bool {
		return f.askedAny("Delete")
	})
}

// The whole of a join fits inside the deadline the caller gives it. zded's
// client gives a call five seconds (internal/zded, Client.Call), and a verdict
// that arrives after the caller has gone is a verdict nobody reads - the widget
// shows a timeout for a join that may well have worked.
//
// If this regresses, the CLI and the surface both fail on exactly the joins
// that take longest, which are the ones worth reporting.
func TestAJoinAnswersInsideTheCallersDeadline(t *testing.T) {
	f := home()
	// Every property read costs a fifth of a second, which is a bus under load
	// rather than a bus that is broken.
	f.slow = 200 * time.Millisecond
	m := serve(t, f)

	start := time.Now()
	m.Connect("cafe", "hunter2") //nolint:errcheck // the answer is not what this is about; the clock is
	if took := time.Since(start); took >= callerWaits {
		t.Errorf("joining took %s, and the caller gives up at %s", took, callerWaits)
	}
}

// The radio that is connected is the one to read from. A laptop with a dongle
// plugged in has two, and NetworkManager lists them in its own order.
//
// If this regresses, the bar says "no network" on a working link and the widget
// lists an empty room, because zde asked the wrong radio - and there is nothing
// on screen to suggest which radio it asked.
func TestTheRadioThatIsConnectedIsTheOneReadFrom(t *testing.T) {
	f := home()
	// A second radio, listed first, connected to nothing: an unplugged dongle
	// that NetworkManager still has an object for.
	f.set(donglePath, devIface, "DeviceType", uint32(typeWifi))
	f.set(donglePath, devIface, "State", uint32(30)) // DISCONNECTED
	f.set(donglePath, wifiIface, "AccessPoints", []dbus.ObjectPath{})
	f.set(rootPath, nmIface, "Devices", []dbus.ObjectPath{donglePath, wifiPath})
	m := serve(t, f)

	st, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Kind != KindWifi || st.SSID != "home" {
		t.Errorf("status = %+v, with the machine connected on its other radio", st)
	}
	networks, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(networks) != 2 {
		t.Errorf("the list has %d networks, and the connected radio can see two", len(networks))
	}
}

// The saved column comes from an enumeration like every other, and it has to
// tell a profile that went away from a budget that ran out for the same reason
// they do: a wrong saved column is not a cosmetic error. It decides whether
// anybody is asked for a password, and it decides whether a join adds a second
// profile for a network that already has one.
//
// If this regresses, a slow bus produces a list where nothing is saved,
// silently.
func TestASpentBudgetIsNotAnUnsavedNetwork(t *testing.T) {
	f := home()
	// The stall is on the saved half specifically. A bus that is slow from the
	// first call runs out of budget in the device list, which is a different
	// enumeration with its own answer - and the test would pass without ever
	// reaching the one it is about.
	f.slow, f.slowOn = askFor, "GetSettings"
	m := serve(t, f)

	_, err := m.List()
	if err == nil {
		t.Fatal("a listing that ran out of time came back looking complete")
	}
	if !strings.Contains(err.Error(), "saved") {
		t.Errorf("the refusal does not say which half went missing: %v", err)
	}
}

// NetworkManager leaving mid-session is the absence of a manager, not a daemon
// that stopped answering. The bus connection outlives it, so nothing about the
// socket says the service is gone - only what its name answers does.
//
// If this regresses, stopping NetworkManager makes the bar read "zded is not
// answering", which sends somebody to the wrong daemon entirely - and the
// absent-versus-unknown distinction this widget is argued around collapses in
// the one case that is not a boot-time fact.
func TestNetworkManagerLeavingIsAbsentAndNotUnknown(t *testing.T) {
	f := home()
	m := serve(t, f)
	if _, err := m.Status(); err != nil {
		t.Fatalf("Status before NetworkManager left: %v", err)
	}

	// It exits: the name goes, the bus stays.
	f.conn.Close()

	_, err := m.Status()
	if !errors.Is(err, ErrNoManager) {
		t.Errorf("with NetworkManager gone, Status = %v", err)
	}
	// And the manager says it is not worth keeping, so the daemon opens a new
	// one and finds out for itself that there is nobody there.
	if m.Alive() {
		t.Error("a manager whose NetworkManager has gone still says it is alive")
	}
}

// waitForCall blocks until the fake has been asked for a member. The tests that
// change the world mid-join need to change it after the request has landed, and
// a sleep long enough to be sure would be a sleep long enough to be slow.
func waitForCall(f *nmFake, member string) {
	for {
		if f.askedAny(member) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// lastActive is the activation object the fake handed back most recently.
func lastActive(f *nmFake) dbus.ObjectPath {
	f.mu.Lock()
	defer f.mu.Unlock()
	newest := dbus.ObjectPath("")
	for path := range f.objs {
		if strings.Contains(string(path), "ActiveConnection/new") && path > newest {
			newest = path
		}
	}
	return newest
}
