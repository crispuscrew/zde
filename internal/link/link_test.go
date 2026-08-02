package link

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// A building has three access points broadcasting one network, and a list that
// showed each of them would offer the same choice three times and push
// everything else off the screen. If this regresses, the widget in an office
// becomes unusable in exactly the place it is needed most.
func TestOneRowPerNetworkAndTheStrongestReadingWins(t *testing.T) {
	got := strongestFirst([]Network{
		{SSID: "vshop", Signal: 40, Secure: true},
		{SSID: "vshop", Signal: 71, Saved: true},
		{SSID: "vshop", Signal: 12, Active: true},
	})
	if len(got) != 1 {
		t.Fatalf("three access points for one network became %d rows: %+v", len(got), got)
	}
	if got[0].Signal != 71 {
		t.Errorf("signal = %d, and the strongest reading was 71", got[0].Signal)
	}
	// The flags belong to the network, not to whichever access point happened
	// to be strongest: a saved network you are standing on must not read as
	// unsaved because the loudest radio in the room was not the one you joined.
	if !got[0].Saved || !got[0].Active || !got[0].Secure {
		t.Errorf("the flags did not survive the merge: %+v", got[0])
	}
}

// The order is the whole of the choice a keyboard makes: press Enter and you
// join row one. If this regresses, Enter joins whichever network
// NetworkManager listed first, which is nobody's idea of the best one.
func TestNetworksComeStrongestFirst(t *testing.T) {
	got := strongestFirst([]Network{
		{SSID: "far", Signal: 20},
		{SSID: "near", Signal: 90},
		{SSID: "middle", Signal: 55},
	})
	var names []string
	for _, n := range got {
		names = append(names, n.SSID)
	}
	if strings.Join(names, " ") != "near middle far" {
		t.Errorf("order = %v", names)
	}
}

// An SSID is 32 bytes chosen by whoever is broadcasting, and everything that
// reads this list is line and column based - the CLI prints tab separated
// rows. If this regresses, anybody within range of the machine can put an
// extra row in the list a person is about to press Enter on.
func TestAnSsidThatWouldForgeARowIsNotOffered(t *testing.T) {
	for _, raw := range []string{
		"good\tsecure\there\tevil",
		"good\nevil",
		"bell\x07",
	} {
		if name, ok := printableSSID([]byte(raw)); ok {
			t.Errorf("%q was offered as %q", raw, name)
		}
	}
	// And the ordinary names still work, spaces and all: a network called
	// "The Coffee Place" is not an attack.
	if name, ok := printableSSID([]byte("The Coffee Place")); !ok || name != "The Coffee Place" {
		t.Errorf("printableSSID(ordinary name) = %q, %v", name, ok)
	}
	// A hidden network broadcasts no name at all, and joining one means typing
	// something nothing showed you - which is not what this list is for.
	if _, ok := printableSSID(nil); ok {
		t.Error("a network with no name was offered")
	}
}

// Whether a network needs a password is what decides whether joining it stops
// to ask for one. If this regresses, either every join prompts (including the
// open cafe network, where the prompt is a lie) or none does.
func TestWhetherANetworkIsSecuredComesFromItsFlags(t *testing.T) {
	if secured(0, 0, 0) {
		t.Error("an access point advertising nothing was read as secured")
	}
	// WEP sets privacy alone; WPA sets its own word; WPA2 and WPA3 set RSN.
	// One of the three is enough, and an access point that sets only one of
	// them is ordinary rather than exotic.
	if !secured(0x1, 0, 0) || !secured(0, 0x100, 0) || !secured(0, 0, 0x100) {
		t.Error("a secured access point was read as open")
	}
}

// A refusal a person cannot act on is the same as silence. If this regresses,
// a wrong password and a network that went out of range become the same
// shrug, which is the failure this whole branch was told not to have.
func TestARefusalSaysWhichRefusal(t *testing.T) {
	if got := refusal(7, true); !strings.Contains(got, "password was refused") {
		t.Errorf("NO_SECRETS after a password = %q", got)
	}
	if got := refusal(53, true); !strings.Contains(got, "range") {
		t.Errorf("SSID_NOT_FOUND = %q, which does not say it is not there", got)
	}
	// A reason nobody has mapped keeps its number rather than being guessed
	// at: the number is what somebody can look up, and a wrong guess sends
	// them to the router for a problem that is in the room.
	if got := refusal(214, true); !strings.Contains(got, "214") {
		t.Errorf("an unmapped reason came back as %q, with no way to look it up", got)
	}
}

// NetworkManager says NO_SECRETS both when the password it was given was
// rejected and when it wanted one that nobody had. Reading "the password was
// refused" when you were never asked for a password sends a person to change a
// password that was never the problem, which is the opposite of what to do.
//
// If this regresses, joining an open network that turns out not to be open, or
// a saved profile whose stored password NetworkManager will not use, both
// accuse a password nobody typed.
func TestARefusalWithNoPasswordOfferedAsksForOne(t *testing.T) {
	got := refusal(7, false)
	if strings.Contains(got, "refused") {
		t.Errorf("no password was offered and the refusal is about one being refused: %q", got)
	}
	if !strings.Contains(got, "wants a password") {
		t.Errorf("NO_SECRETS with nothing offered = %q, which does not say what to do", got)
	}
}

// The active connection is removed when a join fails, so asking about it comes
// back as "no such object" - which is an answer, not a broken bus. If this
// regresses, every refusal is reported as a D-Bus error about a path instead
// of as the reason NetworkManager gave.
func TestAVanishedObjectIsAnAnswerAndNotABrokenBus(t *testing.T) {
	if !gone(dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject"}) {
		t.Error("an object that is not there any more was not recognised")
	}
	if gone(errors.New("connection closed")) {
		t.Error("a dead connection was read as an object that went away")
	}
	if gone(nil) {
		t.Error("no error at all was read as an object that went away")
	}
}

// Every enumeration here skips an object that will not answer, because an
// access point that stopped broadcasting between the listing and the question
// is ordinary. A budget that has run out looks identical from inside the loop
// and means the opposite: every object after it will be skipped too. If this
// regresses, a NetworkManager gone slow answers with three networks out of
// thirty and nothing says the list is short - and a network missing from a list
// is indistinguishable from one out of range, so the answer to it is to press
// the key again and get the same short list.
func TestASpentBudgetIsNotAVanishedAccessPoint(t *testing.T) {
	live, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := carryOn(live, "what is in range"); err != nil {
		t.Errorf("one access point that went away ended the whole list: %v", err)
	}

	spent, stop := context.WithCancel(context.Background())
	stop()
	err := carryOn(spent, "what is in range")
	if err == nil {
		t.Fatal("the budget ran out and the list came back looking whole")
	}
	// And it says which question ran out, because "NetworkManager was slow" is
	// not something anybody can do anything with.
	if !strings.Contains(err.Error(), "what is in range") {
		t.Errorf("the refusal does not say what is missing from the answer: %v", err)
	}
}

// The last thing between a password and everything that reads an error: the
// daemon's answer, the CLI's stderr, a bug report. NetworkManager names the
// property it did not like rather than the value, so this has never had
// anything to do - which is the only condition under which an assertion is
// worth having, and no reason to take it out.
//
// If this regresses, the one message that reaches a person unedited is the one
// message that can carry their wifi password.
func TestAnAnswerThatQuotesThePasswordIsNotRepeated(t *testing.T) {
	const secret = "correct-horse-battery-staple"

	kept := errors.New("802-11-wireless-security.psk: property is invalid")
	if got := withoutSecret(kept, secret); got != kept {
		t.Errorf("an ordinary refusal was replaced: %v", got)
	}
	quoted := errors.New("cannot use psk " + secret + ": too short")
	got := withoutSecret(quoted, secret)
	if strings.Contains(got.Error(), secret) {
		t.Errorf("the password came through the last check: %v", got)
	}
	if got.Error() == "" {
		t.Error("the answer was replaced with nothing, so nobody is told anything")
	}
	// And an error from a join that carried no password at all is untouched,
	// since there is nothing to compare it against.
	if got := withoutSecret(kept, ""); got != kept {
		t.Errorf("an open network's refusal was replaced: %v", got)
	}
}
