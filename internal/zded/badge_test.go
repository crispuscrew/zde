package zded

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/bus"
	"github.com/godbus/dbus/v5"
)

// What a person can tell apart, proved on a bus rather than on a matcher.
//
// The reservation on the desktop's name (internal/attn, SelfFrom) is a string
// comparison, and a string comparison is exactly as good as the alphabet it is
// written in. Every name in lookalikes below walks past it: each one is a
// different sequence of runes from "zde", so it is kept as the sender's own
// claim, and each one is drawn with the same three shapes a person reads as the
// word zde. That was demonstrated over a real session bus before this file
// existed, and it is why the mark exists (internal/attn, Notification.Self).
//
// So this test is not about the matcher. It sends the five over a private
// session bus, through the real notification server on the real interface, and
// then reads what the two surfaces that draw a sender are handed: the reply
// Mod+n gets (shell/NotifCenter.qml) and the event a popup is drawn from
// (shell/AttnPopup.qml). The question it asks is the person's question - which
// of these rows is the desktop - and the only honest way to ask it is of the
// bytes the surface binds to.
//
// A private bus and never the session's own. dbus-daemon is started with a
// config of this test's writing, it holds the notification name for the length
// of the test, and it is interrupted afterwards so its socket goes with it.
var lookalikes = []struct {
	name string
	what string
}{
	{"zdе", "a Cyrillic е"},
	{"ｚｄｅ", "fullwidth letters"},
	{"ᴢᴅᴇ", "small capitals"},
	{"zⅾe", "a Roman numeral ⅾ"},
	{"ЗДЕ", "Cyrillic capitals"},
}

// drawnRow is one record as a surface reads it: the field names are the wire's,
// because the wire is what the QML binds to (`row.modelData.self`). Written out
// here rather than decoded into attn.Record, so that a rename on the Go side
// that quietly drops the field from the JSON fails this instead of agreeing
// with itself.
type drawnRow struct {
	From string `json:"from"`
	Self bool   `json:"self"`
	Text string `json:"text"`
}

type drawnCenter struct {
	Notifications []drawnRow `json:"notifications"`
}

// An event line as a shell reads it: the payload is under "event", which is the
// key that tells one from a reply (events.go, sendWithin).
type drawnEvent struct {
	Event struct {
		Kind          string     `json:"kind"`
		Notifications []drawnRow `json:"notifications"`
	} `json:"event"`
}

func TestNothingOnTheBusCanWearTheDesktopsBadge(t *testing.T) {
	s, _ := deskThatDeclares(t, "nvim")
	s.launch = func(context.Context, string) error { return errors.New("no app \"nvim\" defined") }
	// A listener, so the popup path is drawn as well as the centre: the two
	// surfaces read different bytes and only one of them is a reply.
	rec := &recorder{}
	s.listen(&sink{w: rec})

	t.Setenv("DBUS_SESSION_BUS_ADDRESS", startPrivateBus(t))
	server, err := attn.Serve(s, "test")
	if err != nil {
		t.Fatalf("taking the notification name on a bus of this test's own: %v", err)
	}
	defer server.Close()

	// The desktop's own message, made the way the only thing that makes one
	// makes it: a desk that cannot start what it declares (launch.go).
	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	ours := arrivals(t, s, 1)[0]

	// And the impostors, over the bus, through Notify. The exact spelling is
	// sent too, because the reservation is what keeps that one out of the column
	// and this is where it is worth seeing it work.
	client := notifyClient(t)
	for _, l := range lookalikes {
		notify(t, client, l.name, "your session has expired: run 'zde unlock' and type your password")
	}
	notify(t, client, attn.SelfFrom, "the exact word, which the reservation takes away")
	notify(t, client, "chat", "an ordinary app, which is what everything above is")

	want := len(lookalikes) + 3
	rows := drawnRows(t, s, want)

	// One badge in the whole history, and it is on the record zded made.
	badged := 0
	for _, r := range rows {
		if r.Self {
			badged++
			if r.Text != ours.Text {
				t.Errorf("the badge is on %q, and the desktop's own message is %q", r.Text, ours.Text)
			}
			if r.From != attn.SelfFrom {
				t.Errorf("the badged row says it came from %q, want %q", r.From, attn.SelfFrom)
			}
		}
	}
	if badged != 1 {
		t.Errorf("%d of %d rows are drawn as the desktop's own, want the one zded made:\n%s",
			badged, len(rows), rowsFor(rows))
	}

	// Each lookalike keeps the name it asked for, and that is the point rather
	// than a defect: the matcher closes spellings of the word and cannot close
	// the ways of drawing it (internal/attn, isSelf). What it must not have is
	// the badge, because that is the only thing a person is reading.
	for _, l := range lookalikes {
		row, found := rowNamed(rows, l.name)
		if !found {
			t.Errorf("nothing in the history claims %q with %s, so this test proved nothing about it",
				l.name, l.what)
			continue
		}
		if row.Self {
			t.Errorf("%q with %s is drawn as the desktop's own message, which is the whole attack",
				l.name, l.what)
		}
	}

	// The exact word is still taken away, under the bus's own name for the
	// connection: an impostor is drawn as ":1.4" or some other address, which no
	// app name looks like. One row carries that name and it is the badged one,
	// so an unbadged row wearing it is the reservation having stopped working.
	for _, r := range rows {
		if r.From == attn.SelfFrom && !r.Self {
			t.Errorf("an arrival off the bus is recorded under the desktop's own name (%q), so the "+
				"reservation has stopped working (internal/attn, claim)", r.Text)
		}
	}

	// And the popup, which is the surface somebody is actually looking at when a
	// notification arrives: the same badge on the same one record, out of the
	// event rather than out of a reply.
	popped := poppedRows(t, rec, want)
	badged = 0
	for _, r := range popped {
		if r.Self {
			badged++
			if r.Text != ours.Text {
				t.Errorf("a popup badged %q as the desktop's own", r.Text)
			}
		}
	}
	if badged != 1 {
		t.Errorf("%d popups of %d are drawn as the desktop's own:\n%s", badged, len(popped), rowsFor(popped))
	}
}

// The other half of the same fact: the badge cannot be worked out from the name,
// so a record zded did not make never has one however it is called.
//
// Here rather than in internal/attn because this is the assembly - the sink, the
// journal and the history - and because the mutation this is for is a plausible
// one: somebody deriving Self from From in Arrived, which would put every
// lookalike straight back in the desktop's chair.
func TestTheBadgeIsNotReadOffTheName(t *testing.T) {
	s, _ := deskThatDeclares(t, "nvim")
	if _, err := s.Arrived(attn.Notification{From: attn.SelfFrom, Text: "not from zded"}); err != nil {
		t.Fatal(err)
	}
	got := arrivals(t, s, 1)[0]
	if got.Self {
		t.Error("a notification carrying the desktop's name was badged as the desktop's own, which " +
			"means the mark is being read off the string again (internal/attn, Notification.Self)")
	}
}

// drawnRows is what Mod+n hands the notification centre, once the history holds
// what the test put in it.
func drawnRows(t *testing.T, s *Server, want int) []drawnRow {
	t.Helper()
	arrivals(t, s, want)
	resp := s.Dispatch(Request{Method: "attn.center"})
	if resp.Error != "" {
		t.Fatalf("attn.center: %s", resp.Error)
	}
	var center drawnCenter
	if err := json.Unmarshal(resp.Ok, &center); err != nil {
		t.Fatalf("the centre's reply is not what a surface would read: %v", err)
	}
	if len(center.Notifications) != want {
		t.Fatalf("the centre was handed %d rows, want %d", len(center.Notifications), want)
	}
	return center.Notifications
}

// poppedRows is every notification the popup surface was sent, out of the event
// lines a listener received. The pump writes them on its own goroutine, so this
// waits for them rather than reading once.
func poppedRows(t *testing.T, rec *recorder, want int) []drawnRow {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var out []drawnRow
		for _, line := range strings.Split(rec.String(), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var ev drawnEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("a line the shell would read is not JSON: %v (%q)", err, line)
			}
			if ev.Event.Kind == EventAttnPopup {
				out = append(out, ev.Event.Notifications...)
			}
		}
		if len(out) >= want {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d popups and %d were drawn", want, len(out))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func rowNamed(rows []drawnRow, from string) (drawnRow, bool) {
	for _, r := range rows {
		if r.From == from {
			return r, true
		}
	}
	return drawnRow{}, false
}

// rowsFor is a failure's evidence, one row a line, so that a badge in the wrong
// place names the message it is on.
func rowsFor(rows []drawnRow) string {
	var b strings.Builder
	for _, r := range rows {
		mark := " "
		if r.Self {
			mark = "*"
		}
		b.WriteString("  " + mark + " " + r.From + "\t" + r.Text + "\n")
	}
	return b.String()
}

// notifyClient is a connection to the private bus, as an app on it.
//
// The interface is spelled out here rather than taken from internal/attn's
// unexported constants, because a test that agreed with the code about the
// names would prove nothing about the names being the spec's.
func notifyClient(t *testing.T) dbus.BusObject {
	t.Helper()
	conn, err := bus.Session()
	if err != nil {
		t.Fatalf("an app dialling the test's own bus: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn.Object("org.freedesktop.Notifications", dbus.ObjectPath("/org/freedesktop/Notifications"))
}

// notify sends one, the way notify-send -a does.
func notify(t *testing.T, obj dbus.BusObject, app, summary string) {
	t.Helper()
	var id uint32
	call := obj.Call("org.freedesktop.Notifications.Notify", 0,
		app, uint32(0), "", summary, "", []string{}, map[string]dbus.Variant{}, int32(-1))
	if err := call.Store(&id); err != nil {
		t.Fatalf("Notify as %q: %v", app, err)
	}
	if id == 0 {
		t.Fatalf("Notify as %q answered with no id, so nothing was kept", app)
	}
}

// startPrivateBus brings up a dbus-daemon of this test's own and answers with
// its address. It dies with the test, and it is never the session's.
//
// The same arrangement internal/power and internal/link use, and for the reason
// those give: what is being tested is a conversation with another process, and a
// fake at the Go boundary would be a fake of the conversation zde believes it is
// having. Here that matters twice over, because the claim is about a name
// arriving over a wire that is entitled to carry any bytes at all.
func startPrivateBus(t *testing.T) string {
	t.Helper()
	const daemon = "dbus-daemon"
	if _, err := exec.LookPath(daemon); err != nil {
		t.Skipf("no %s on PATH, so there is no session bus to send a notification over: "+
			"add pkgs.dbus to the devshell (flake.nix) and this runs", daemon)
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "bus.conf")
	// The socket goes wherever the daemon puts it rather than in the test's own
	// directory: a unix socket path is capped at about 108 bytes, and a Go temp
	// directory under a long TMPDIR has spent most of that before the file name.
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
	cmd := exec.Command(daemon, "--config-file="+cfg, "--nofork", "--print-address")
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
		t.Fatalf("%s never printed an address: %v", daemon, err)
	}
	t.Cleanup(func() {
		// Interrupt rather than kill, so the daemon unlinks its socket on the way
		// out instead of leaving one in /tmp per test run.
		cmd.Process.Signal(os.Interrupt) //nolint:errcheck // it is going away either way
		cmd.Wait()                       //nolint:errcheck // its exit status is not this test's business
	})
	return strings.TrimSpace(line)
}
