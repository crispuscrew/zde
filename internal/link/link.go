// Package link is what this machine is on the network with: the link it has
// right now, the wifi networks it can see, and joining one of them.
//
// Called link and not net because the standard library owns that word: a zde
// package called net would be renamed at every import site that also dials a
// socket, which is most of them.
//
// Everything here goes through NetworkManager, which layer 0 installs
// (nix/system.nix, the laptop block). A machine without it is a state and not
// a failure - see ErrNoManager - because plenty of machines are online through
// something else entirely, and a widget that called that "offline" would be
// lying about a working network.
//
// What is not here: bluetooth (a pairing model of its own, and the keymap's
// description promises it before anything does it), VPNs, hidden networks, and
// enterprise (802.1x) authentication. Each is a separate conversation with
// NetworkManager, and half a conversation is worse than none.
package link

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The four things the link can be. Four and not three: "there is no
// NetworkManager here" is not "there is no network here", and the bar has to
// be able to say so (docs/vision.md, principle 4 - what a keypress depends on
// is on the bar, and a wifi meter showing four bars after NetworkManager died
// is the kind of stale truth that principle exists to stop).
const (
	KindWifi  = "wifi"
	KindWired = "wired"
	// KindNone is NetworkManager answering, and nothing connected.
	KindNone = "none"
	// KindAbsent is no NetworkManager on this machine at all.
	KindAbsent = "absent"
)

// Status is the link as it stands.
type Status struct {
	Kind string `json:"kind"`
	// SSID and Signal are the wifi half, and empty for everything else.
	// Signal is NetworkManager's own 0-100, not dBm: it is what the widget
	// draws and what a person compares two rows by.
	SSID   string `json:"ssid,omitempty"`
	Signal int    `json:"signal,omitempty"`
	// Wifi says whether this machine has a radio at all. Without one an empty
	// list of networks means something quite different from "nothing is in
	// range", and only this can tell the two apart.
	Wifi bool `json:"wifi"`
	// Killed is NetworkManager's networking switch, off. It is beside Kind
	// rather than a fifth Kind because the two answer different questions and a
	// cut machine answers both: Kind is none, and this is why.
	//
	// Read back from NetworkManager on every Status rather than remembered by
	// whoever flipped it. A daemon that remembered would go on saying "zde cut
	// this" after somebody ran `nmcli networking on`, and would say nothing at
	// all after a zded restart - and the whole worth of this field is a bar that
	// can tell a cut network from a broken one (docs/vision.md, principle 4).
	Killed bool `json:"killed,omitempty"`
}

// Network is one wifi network as the list shows it: enough to choose by, and
// enough to know whether choosing it will ask for a password.
type Network struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"`
	Secure bool   `json:"secure"`
	// Saved is whether NetworkManager already has a profile for it, which is
	// what decides whether joining needs a password from anybody.
	Saved bool `json:"saved"`
	// Active is the one you are on.
	Active bool `json:"active"`
}

// Manager is the little of NetworkManager that zde needs.
//
// An interface because the daemon has to be testable without a system bus, a
// radio, or an access point in the room - and because the thing behind it is
// allowed to be missing.
type Manager interface {
	Status() (Status, error)
	// List is what is visible, strongest first.
	List() ([]Network, error)
	// Connect joins a network. The secret is empty for an open network, and
	// for one NetworkManager already has a profile for; it is never logged,
	// never journalled, and never put on anybody's command line.
	Connect(ssid, secret string) error
	// Forget drops a saved network. It is how a password that has changed gets
	// typed again - nothing asks for one while a profile is there - and it is
	// the only thing here that deletes a profile somebody else made.
	Forget(ssid string) error
	// Disconnect drops the wifi link, and leaves the profile saved.
	Disconnect() error
	// Kill cuts every link NetworkManager manages, or puts them all back. It is
	// NetworkManager's own networking switch and nothing else: wired and wifi
	// go, saved profiles stay, and the radios stay powered - so a bluetooth
	// keyboard still works to press the key that undoes it (see NM.Kill).
	Kill(cut bool) error
}

// ErrNoManager is a machine with no NetworkManager: no system bus, or nobody
// owning its name. The caller turns this into KindAbsent rather than into an
// error on the screen.
var ErrNoManager = errors.New("no NetworkManager on this machine")

// ErrStillTrying is a join that had not been decided when the wait ran out.
// Not a failure: NetworkManager is still at it, and the bar is where the
// answer arrives (see settle in nm.go for why the wait is short).
var ErrStillTrying = errors.New("NetworkManager is still trying")

// strongestFirst is the order the list is shown in, and it does two jobs.
//
// One access point per row would show the same network three times in a
// building with three of them, so the strongest reading of each name wins and
// the flags are merged - a network is saved if any profile matches it, and it
// is the active one if you are on any of its access points.
//
// Then strongest first, because that is the one worth joining, with the name
// breaking ties so that two networks at the same strength do not swap places
// between two draws. Signal moves on its own, so rows do move; a list you can
// hold still is a filter, and filtering is the palette's job (0.2).
func strongestFirst(seen []Network) []Network {
	best := map[string]Network{}
	order := make([]string, 0, len(seen))
	for _, n := range seen {
		old, had := best[n.SSID]
		if !had {
			order = append(order, n.SSID)
		} else {
			n.Active = n.Active || old.Active
			n.Saved = n.Saved || old.Saved
			n.Secure = n.Secure || old.Secure
			if old.Signal > n.Signal {
				n.Signal = old.Signal
			}
		}
		best[n.SSID] = n
	}
	out := make([]Network, 0, len(best))
	for _, ssid := range order {
		out = append(out, best[ssid])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Signal != out[j].Signal {
			return out[i].Signal > out[j].Signal
		}
		return out[i].SSID < out[j].SSID
	})
	return out
}

// printableSSID is the name to show for an access point, and false for one
// there is no safe name for.
//
// An SSID is up to 32 arbitrary bytes chosen by whoever is broadcasting, and
// everyone within range gets to broadcast. Everything that reads this list is
// line and column based - the CLI prints tab separated rows, the surface draws
// one row per network - so an SSID carrying a tab or a newline is a row
// somebody else wrote, in a list a person is about to press Enter on.
//
// Dropped rather than escaped, and the reason is not tidiness: you cannot join
// a network you cannot name, and a name edited to be printable is no longer the
// name the access point answers to, so a row made safe that way would be a row
// that cannot connect.
func printableSSID(raw []byte) (string, bool) {
	// Empty is a hidden network, which is out of scope on purpose: joining one
	// means typing a name nothing showed you.
	if len(raw) == 0 {
		return "", false
	}
	s := string(raw)
	if !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		// IsPrint takes the ASCII space, so a name with spaces in it is fine;
		// it is tabs, newlines and control characters that go.
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	return s, true
}

// withoutSecret is the last thing a join's answer passes through: an error that
// quotes the password does not get repeated, whatever else it says.
//
// This is an assertion rather than a fix for anything known. NetworkManager
// names the property it did not like - "802-11-wireless-security.psk: property
// is invalid" - and does not appear to echo values, so nothing has ever been
// seen coming through here. It is worth the six lines because this is the one
// boundary where the password and the message are both in hand: an error out of
// here reaches the daemon's answer, the CLI's stderr, and whatever a person
// pastes into a bug report, and a password that gets that far is one nobody can
// take back.
//
// The whole error goes rather than the password being cut out of it. A message
// we understand well enough to edit is one we understand well enough to trust,
// and this exists for the case where neither is true - and editing would also
// mangle an ordinary refusal for anybody whose passphrase happens to be a word
// like "password".
func withoutSecret(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return errors.New("NetworkManager's answer quoted the password, so it is not being repeated")
}

// secured is whether joining this network needs a password.
//
// Read off the flags rather than guessed from the name, and all three words:
// Privacy alone is WEP, WpaFlags is WPA, RsnFlags is WPA2 and WPA3, and an
// access point that sets only one of them is ordinary.
func secured(flags, wpa, rsn int) bool {
	const privacy = 0x1 // NM_802_11_AP_FLAGS_PRIVACY
	return wpa != 0 || rsn != 0 || flags&privacy != 0
}

// refusal turns NetworkManager's reason for giving up into something a person
// can do something about. The numbers are NMDeviceStateReason; the ones not
// named here are printed as themselves rather than guessed at, because a wrong
// diagnosis sends somebody to the router for a problem that is in the room.
//
// offered is whether a password went with the attempt, and it changes what one
// of these means rather than how it is worded. NO_SECRETS is what
// NetworkManager says both when the password it was given was rejected and when
// it needed one and nobody had one to give - and "the password was refused" is
// a bad thing to read when you were never asked for a password, because the
// thing to do about it is the opposite.
func refusal(reason int, offered bool) string {
	switch reason {
	case 7: // NO_SECRETS
		if !offered {
			return "that network wants a password, and none was offered"
		}
		return "the password was refused"
	case 8, 10, 11: // SUPPLICANT_DISCONNECT, SUPPLICANT_FAILED, SUPPLICANT_TIMEOUT
		return "the access point stopped answering, which is usually a wrong password"
	case 15, 16, 17: // DHCP_START_FAILED, DHCP_ERROR, DHCP_FAILED
		return "the network was joined and no address came back"
	case 53: // SSID_NOT_FOUND
		return "that network is not in range any more"
	default:
		return fmt.Sprintf("NetworkManager gave up (reason %d)", reason)
	}
}
