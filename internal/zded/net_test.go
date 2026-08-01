package zded

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/link"
)

// A NetworkManager that is not there, and one that is. Both are what the
// daemon has to work against, and neither needs a system bus.
type fakeLink struct {
	mu       sync.Mutex
	status   link.Status
	networks []link.Network
	// joinErr is what Connect answers, which is where every interesting case
	// lives: a refusal, a wait that ran out, a join that worked.
	joinErr error
	// got is what Connect was handed, so a test can prove the secret went to
	// NetworkManager and nowhere else.
	got []string
}

func (f *fakeLink) Status() (link.Status, error) { return f.status, nil }
func (f *fakeLink) List() ([]link.Network, error) {
	return append([]link.Network(nil), f.networks...), nil
}

func (f *fakeLink) Connect(ssid, secret string) error {
	f.mu.Lock()
	f.got = append(f.got, ssid, secret)
	f.mu.Unlock()
	return f.joinErr
}

func (f *fakeLink) Disconnect() error { return nil }

func (f *fakeLink) handed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

// withLink gives a server a network side, the way the daemon gives itself one.
func withLink(s *Server, m link.Manager, err error) {
	s.openLink = func() (link.Manager, error) { return m, err }
}

// A desktop has no NetworkManager, and that is not the same as being offline -
// it is online through something else, and zde simply cannot say. If this
// regresses, every machine that is not a laptop gets a bar that says "no
// network" while the browser works, which is the one lie the bar's whole
// known/unknown pattern exists to prevent.
func TestNoNetworkManagerIsNotOffline(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	withLink(s, nil, link.ErrNoManager)

	resp := s.Dispatch(Request{Method: "net.status"})
	if resp.Error != "" {
		t.Fatalf("net.status on a machine with no NetworkManager: %s", resp.Error)
	}
	var st link.Status
	if err := json.Unmarshal(resp.Ok, &st); err != nil {
		t.Fatal(err)
	}
	if st.Kind != link.KindAbsent {
		t.Errorf("kind = %q, and a machine with no manager is not a machine with no network", st.Kind)
	}

	// And with a manager that is there and has nothing connected, the answer is
	// the other one. Two states, told apart.
	s2 := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	withLink(s2, &fakeLink{status: link.Status{Kind: link.KindNone, Wifi: true}}, nil)
	resp = s2.Dispatch(Request{Method: "net.status"})
	if err := json.Unmarshal(resp.Ok, &st); err != nil {
		t.Fatal(err)
	}
	if st.Kind != link.KindNone {
		t.Errorf("kind = %q for a manager with nothing connected", st.Kind)
	}
}

// A machine with no NetworkManager is asked about once, and not on every poll.
//
// The bar reads the link every five seconds for as long as the session runs, and
// every zde desktop has no NetworkManager: layer 0 installs it for laptops only.
// Each of those questions opened a private system bus connection, ran a SASL
// handshake, said Hello, asked who owns the name and closed again - seventeen
// thousand times a day, measured, for an answer that changes when somebody
// rebuilds the machine. Break this and that comes back, and with it one
// abandoned dial every five seconds against a bus that has stopped answering.
func TestAnAbsentNetworkManagerIsNotDialledAgainOnEveryPoll(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	dials := 0
	s.openLink = func() (link.Manager, error) {
		dials++
		return nil, link.ErrNoManager
	}

	for i := 0; i < 20; i++ {
		resp := s.Dispatch(Request{Method: "net.status"})
		if resp.Error != "" {
			t.Fatalf("net.status: %s", resp.Error)
		}
		var st link.Status
		if err := json.Unmarshal(resp.Ok, &st); err != nil {
			t.Fatal(err)
		}
		// The same answer every time: remembering an absence must not turn
		// "there is no manager here" into a refusal.
		if st.Kind != link.KindAbsent {
			t.Fatalf("kind = %q on poll %d", st.Kind, i)
		}
	}
	if dials != 1 {
		t.Errorf("dialled %d times over twenty polls, want once", dials)
	}

	// Believed for a while and not for ever: a machine that gains a manager has
	// to be able to say so without a new session.
	s.noManagerAt = time.Now().Add(-2 * noManagerFor)
	s.openLink = func() (link.Manager, error) {
		dials++
		return &fakeLink{status: link.Status{Kind: link.KindWifi, Wifi: true, SSID: "vshop"}}, nil
	}
	resp := s.Dispatch(Request{Method: "net.status"})
	var st link.Status
	if err := json.Unmarshal(resp.Ok, &st); err != nil {
		t.Fatal(err)
	}
	if st.Kind != link.KindWifi {
		t.Errorf("kind = %q once the manager arrived: nothing ever asked again", st.Kind)
	}
}

// The secret is the security-sensitive half of this feature: it must reach
// NetworkManager and nothing else. If this regresses, a wifi password ends up
// in the journal that zded keeps for ever on disk, or in the daemon's log,
// which on a real machine is the systemd journal - readable by every admin of
// the machine and by anything that ships logs somewhere.
func TestJoiningKeepsTheSecretOutOfEverythingZdeWritesDown(t *testing.T) {
	const secret = "correct-horse-battery-staple"

	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	path := filepath.Join(t.TempDir(), "journal.jsonl")
	jrn, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()

	f := &fakeLink{status: link.Status{Kind: link.KindNone, Wifi: true}}
	s := New("test", jrn, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	withLink(s, f, nil)

	// Something on the queue first, so the journal has bytes in it: a test that
	// finds no secret in an empty file has proved nothing.
	if resp := s.Dispatch(Request{Method: "queue.add", Args: []string{"buy milk"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	resp := s.Dispatch(Request{Method: "net.connect", Args: []string{"vshop-wifi", secret}})
	if resp.Error != "" {
		t.Fatalf("net.connect: %s", resp.Error)
	}

	// It did reach NetworkManager - otherwise the rest of this test passes by
	// doing nothing at all.
	if got := f.handed(); len(got) != 2 || got[0] != "vshop-wifi" || got[1] != secret {
		t.Fatalf("NetworkManager was handed %q", got)
	}

	if strings.Contains(string(resp.Ok), secret) {
		t.Errorf("the answer carries the password: %s", resp.Ok)
	}
	if strings.Contains(logged.String(), secret) {
		t.Errorf("the daemon's log carries the password: %s", logged.String())
	}
	if err := jrn.Close(); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 {
		t.Fatal("the journal is empty, so this test proves nothing")
	}
	if bytes.Contains(written, []byte(secret)) {
		t.Errorf("the journal on disk carries the password: %s", written)
	}
}

// A refusal has to say which refusal, and it still has to not say the
// password. If this regresses, either a wrong password looks like nothing
// happening - the failure this branch was written to avoid - or the error
// message becomes the leak, since an error is the one string that gets logged,
// printed and pasted into bug reports.
func TestARefusedJoinSaysWhyAndStillNotTheSecret(t *testing.T) {
	const secret = "hunter2-and-a-bit"
	f := &fakeLink{joinErr: errors.New("the password was refused")}
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	withLink(s, f, nil)

	resp := s.Dispatch(Request{Method: "net.connect", Args: []string{"vshop-wifi", secret}})
	if resp.Error == "" {
		t.Fatal("a refused join answered as success")
	}
	if !strings.Contains(resp.Error, "password was refused") {
		t.Errorf("the refusal does not say what NetworkManager said: %q", resp.Error)
	}
	if !strings.Contains(resp.Error, "vshop-wifi") {
		t.Errorf("the refusal does not say which network: %q", resp.Error)
	}
	if strings.Contains(resp.Error, secret) {
		t.Errorf("the refusal carries the password: %q", resp.Error)
	}
}

// NetworkManager often has not decided within the time a keybind can wait, and
// that is not a join. If this regresses, the widget closes saying it joined a
// network it may never join, and the bar and the widget disagree about the
// machine.
func TestAJoinThatIsStillTryingIsNotAJoin(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	withLink(s, &fakeLink{joinErr: link.ErrStillTrying}, nil)

	resp := s.Dispatch(Request{Method: "net.connect", Args: []string{"vshop-wifi"}})
	if resp.Error != "" {
		t.Fatalf("still trying was reported as a failure: %s", resp.Error)
	}
	var said string
	if err := json.Unmarshal(resp.Ok, &said); err != nil {
		t.Fatal(err)
	}
	if said != "joining vshop-wifi" {
		t.Errorf("answer = %q, which reads like a network that is up", said)
	}
}

// net.connect takes a network and, at most, a password. If this regresses, the
// verb grows a third argument nobody reads - which is exactly where a secret
// ends up being passed by something that could not find the right place for it.
func TestJoiningTakesANetworkAndAtMostAPassword(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	withLink(s, &fakeLink{}, nil)

	for _, args := range [][]string{{}, {"a", "b", "c"}} {
		if resp := s.Dispatch(Request{Method: "net.connect", Args: args}); resp.Error == "" {
			t.Errorf("net.connect %q was accepted", args)
		}
	}
}

// The connections key is the desk switcher's bargain: a surface where there is
// one, and the list printed where there is not. If this regresses, the key does
// nothing at all on a session whose shell has died - and there is no way to
// test any of this without a compositor.
func TestConnectionsShowsASurfaceOrHandsBackTheList(t *testing.T) {
	f := &fakeLink{
		status:   link.Status{Kind: link.KindWifi, SSID: "vshop", Signal: 71, Wifi: true},
		networks: []link.Network{{SSID: "vshop", Signal: 71, Secure: true, Saved: true, Active: true}},
	}
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code", output: "DP-1"}, nil)
	withLink(s, f, nil)

	// Nobody listening: the caller gets everything it needs to print.
	resp := s.Dispatch(Request{Method: "net.connections"})
	if resp.Error != "" {
		t.Fatalf("net.connections: %s", resp.Error)
	}
	var cn Connections
	if err := json.Unmarshal(resp.Ok, &cn); err != nil {
		t.Fatal(err)
	}
	if cn.Shown {
		t.Error("nothing was listening and the answer claims a surface was shown")
	}
	if cn.Link.SSID != "vshop" || len(cn.Networks) != 1 {
		t.Errorf("the answer does not carry the list to print: %+v", cn)
	}

	// A listener, and the event carries what the surface needs to draw without
	// asking anything else.
	rec := &recorder{}
	k := &sink{w: rec}
	s.listen(k)
	resp = s.Dispatch(Request{Method: "net.connections"})
	if resp.Error != "" {
		t.Fatalf("net.connections with a listener: %s", resp.Error)
	}
	var got struct{ Event Event }
	line := strings.TrimSpace(rec.String())
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the event is not one line of json: %q", line)
	}
	if got.Event.Kind != EventConnections {
		t.Errorf("kind = %q", got.Event.Kind)
	}
	if got.Event.Link == nil || got.Event.Link.SSID != "vshop" {
		t.Errorf("the event does not say what the link is: %+v", got.Event)
	}
	if len(got.Event.Networks) != 1 || got.Event.Networks[0].SSID != "vshop" {
		t.Errorf("the event does not carry the networks: %+v", got.Event.Networks)
	}
	if got.Event.Output != "DP-1" {
		t.Errorf("the event does not say which screen: %q", got.Event.Output)
	}
}

// A machine with no NetworkManager still opens the widget, with the reason on
// it. If this regresses, Mod+Shift+c is a key that does nothing on every
// desktop that is not a laptop, which is indistinguishable from a broken bind.
func TestConnectionsOpensOnAMachineWithNoManager(t *testing.T) {
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	withLink(s, nil, link.ErrNoManager)

	resp := s.Dispatch(Request{Method: "net.connections"})
	if resp.Error != "" {
		t.Fatalf("net.connections with no NetworkManager: %s", resp.Error)
	}
	var cn Connections
	if err := json.Unmarshal(resp.Ok, &cn); err != nil {
		t.Fatal(err)
	}
	if cn.Link.Kind != link.KindAbsent {
		t.Errorf("kind = %q", cn.Link.Kind)
	}
	if len(cn.Networks) != 0 {
		t.Errorf("networks = %+v, on a machine with nothing to ask", cn.Networks)
	}
}
