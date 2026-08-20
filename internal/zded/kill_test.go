package zded

import (
	"errors"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/link"
)

func killServer(t *testing.T, st link.Status) (*Server, *fakeLink) {
	t.Helper()
	f := &fakeLink{status: st}
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	withLink(s, f, nil)
	return s, f
}

// One action, both ways. The person who cut the network is the person who has
// to put it back, from the same keyboard, on a laptop with no cable - so which
// way the toggle goes is read off NetworkManager's own switch and not off a bit
// zded is holding.
//
// The mutation this catches: a netKill that always cuts. Pressed twice, that
// leaves a machine offline with the key that looked like the way out having
// done nothing.
func TestCuttingTheNetworkGoesBothWaysFromOneAction(t *testing.T) {
	s, f := killServer(t, link.Status{Kind: link.KindWifi, SSID: "home"})

	resp := s.Dispatch(Request{Method: "net.kill"})
	if resp.Error != "" {
		t.Fatalf("net.kill: %s", resp.Error)
	}
	if said := string(resp.Ok); !strings.Contains(said, "cut") {
		t.Errorf("cutting answered %s, and a person has to be able to read which way it went", said)
	}

	resp = s.Dispatch(Request{Method: "net.kill"})
	if resp.Error != "" {
		t.Fatalf("net.kill again: %s", resp.Error)
	}
	if said := string(resp.Ok); !strings.Contains(said, "back") {
		t.Errorf("the second press answered %s, and it is the way back", said)
	}
	if got := f.asked(); len(got) != 2 || !got[0] || got[1] {
		t.Errorf("NetworkManager was asked to cut %v, want a cut and then a restore", got)
	}
}

// The bar's half of it, and the reason this verb is safe to have. A cut machine
// reads as "no network" everywhere - that is what NetworkManager reports - so
// without the switch coming back in the status there is nothing on the screen
// that tells a person zde did this rather than the wifi dying.
//
// The mutation: drop Killed from what net.status answers. The toggle still
// works and the desktop stops being able to say why it is offline.
func TestTheStatusSaysTheNetworkWasCutRatherThanLost(t *testing.T) {
	s, _ := killServer(t, link.Status{Kind: link.KindWifi, SSID: "home"})

	if resp := s.Dispatch(Request{Method: "net.kill"}); resp.Error != "" {
		t.Fatalf("net.kill: %s", resp.Error)
	}
	resp := s.Dispatch(Request{Method: "net.status"})
	if resp.Error != "" {
		t.Fatalf("net.status: %s", resp.Error)
	}
	if !strings.Contains(string(resp.Ok), `"killed":true`) {
		t.Errorf("net.status answers %s after a cut, and the bar cannot tell that from a network that broke", resp.Ok)
	}
}

// A machine with no NetworkManager is refused, and refused in words. Everywhere
// else in the network side an absent manager is a state the bar reports; here
// the caller asked for the network to be cut, and answering "done" would be a
// security action claiming an effect it did not have.
//
// The mutation: answer ok on ErrNoManager, the way net.status does. Every
// desktop then has a kill switch that reports success and cuts nothing.
func TestCuttingRefusesWhenThereIsNoNetworkManagerToCutWith(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	withLink(s, nil, link.ErrNoManager)

	resp := s.Dispatch(Request{Method: "net.kill"})
	if resp.Error == "" {
		t.Fatalf("net.kill with no NetworkManager answered %s, and nothing was cut", resp.Ok)
	}
	if !strings.Contains(resp.Error, "NetworkManager") {
		t.Errorf("the refusal reads %q, and it should say what is missing", resp.Error)
	}
}

// NetworkManager refusing - polkit, an account not in the networkmanager group -
// comes back as a refusal rather than as a key that did nothing (README,
// Missing).
func TestARefusedCutIsReportedAndNotSwallowed(t *testing.T) {
	s, f := killServer(t, link.Status{Kind: link.KindWired})
	f.killErr = errors.New("Not authorized to enable/disable networking")

	resp := s.Dispatch(Request{Method: "net.kill"})
	if resp.Error == "" {
		t.Fatalf("NetworkManager refused and net.kill answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "Not authorized") {
		t.Errorf("the refusal reads %q, and NetworkManager's own reason is the half worth reading", resp.Error)
	}
}

// A toggle takes no argument. The word after it would be the thing to get wrong
// in the moment somebody is reaching for this key.
func TestKillTakesNoArguments(t *testing.T) {
	s, _ := killServer(t, link.Status{Kind: link.KindWifi})

	if resp := s.Dispatch(Request{Method: "net.kill", Args: []string{"on"}}); resp.Error == "" {
		t.Errorf("net.kill on answered %s, and there is no such spelling", resp.Ok)
	}
}
