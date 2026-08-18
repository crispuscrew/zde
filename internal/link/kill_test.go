package link

import (
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// A kill has to be the switch a person can find again. It flips
// NetworkManager's own networking state, which is what `nmcli networking` reads
// and what the bar reads back - so a machine somebody cut and a machine whose
// wifi died are two different readings rather than one.
//
// Without this, Kill would be free to drop the link instead, which looks
// identical on the bar and comes back on its own the next time NetworkManager
// feels like reassociating.
func TestCuttingTheNetworkFlipsTheSwitchTheBarReadsBack(t *testing.T) {
	f := home()
	m := serve(t, f)

	if st, err := m.Status(); err != nil || st.Killed {
		t.Fatalf("status = %+v, %v, and nothing has been cut yet", st, err)
	}
	if err := m.Kill(true); err != nil {
		t.Fatalf("cutting: %v", err)
	}
	if !f.askedAny("Enable") {
		t.Fatal("nothing called Enable, so the networking switch was never touched")
	}
	st, err := m.Status()
	if err != nil {
		t.Fatalf("Status after a cut: %v", err)
	}
	if !st.Killed {
		t.Errorf("status = %+v after a cut, and the bar has no way to say zde did it", st)
	}

	// And the way back is the same switch, so one action undoes the other.
	if err := m.Kill(false); err != nil {
		t.Fatalf("putting it back: %v", err)
	}
	if st, err := m.Status(); err != nil || st.Killed {
		t.Errorf("status = %+v, %v after putting it back", st, err)
	}
}

// A cut leaves every saved profile where it was, which is what makes it
// undoable from the same keyboard: nothing has to be typed again. So the three
// members that destroy or drop something are the ones worth proving were never
// called.
func TestCuttingTheNetworkDeletesNothing(t *testing.T) {
	f := home()
	m := serve(t, f)

	if err := m.Kill(true); err != nil {
		t.Fatalf("cutting: %v", err)
	}
	for _, member := range []string{"Delete", "Update", "DeactivateConnection"} {
		if f.askedAny(member) {
			t.Errorf("cutting the network called %s, and a kill you cannot undo is a bricked session", member)
		}
	}
	f.mu.Lock()
	_, saved := f.settings[homeConn]
	f.mu.Unlock()
	if !saved {
		t.Error("the saved profile for home is gone, so coming back means typing the password again")
	}
}

// NetworkManager decides whether this account may flip that switch - polkit's
// enable-disable-network - and a refusal has to arrive as a refusal. A key that
// silently did nothing is the one outcome a security action must not have, and
// this is the only place NetworkManager's own answer is in hand.
func TestARefusedCutSaysSoRatherThanClaimingTheNetworkIsGone(t *testing.T) {
	f := home()
	m := serve(t, f)
	f.setFail("Enable", "org.freedesktop.NetworkManager.PermissionDenied")

	err := m.Kill(true)
	if err == nil {
		t.Fatal("NetworkManager refused and Kill said nothing, so the network is up and the key looked like it worked")
	}
	// NetworkManager's own answer, not one written here: the name says which
	// refusal it was and the body is what a person reads.
	var derr dbus.Error
	if !errors.As(err, &derr) || derr.Name != "org.freedesktop.NetworkManager.PermissionDenied" {
		t.Errorf("the refusal is %#v, and it should be the one NetworkManager sent", err)
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Error("the refusal has nothing in it to read")
	}
	if st, _ := m.Status(); st.Killed {
		t.Error("a refused cut still reads as cut")
	}
}
