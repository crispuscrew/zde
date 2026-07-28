package attn

import (
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

type fakeSink struct {
	got    []Notification
	closed []uint32
	err    error
	nextID uint32
}

func (f *fakeSink) Arrived(n Notification) (uint32, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.got = append(f.got, n)
	f.nextID++
	return f.nextID, nil
}

func (f *fakeSink) Closed(id uint32) error {
	f.closed = append(f.closed, id)
	return nil
}

func notifier(sink Sink) *Server { return &Server{sink: sink, version: "test"} }

func TestNotifyPutsItOnTheQueue(t *testing.T) {
	sink := &fakeSink{}
	id, derr := notifier(sink).Notify("Fractal", 0, "icon", "Ilya: about the invoice", "body text", nil,
		map[string]dbus.Variant{"urgency": dbus.MakeVariant(byte(2))}, -1)
	if derr != nil {
		t.Fatal(derr)
	}
	if id != 1 {
		t.Errorf("id = %d, want the one the sink gave", id)
	}
	if len(sink.got) != 1 {
		t.Fatalf("sink got %d notifications", len(sink.got))
	}
	n := sink.got[0]
	if n.Text != "Ilya: about the invoice" || n.From != "Fractal" || !n.Urgent {
		t.Errorf("notification = %+v", n)
	}
}

// Some apps put everything in the body. Something beats an item that says
// nothing at all.
func TestNotifyFallsBackToTheBody(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify("app", 0, "", "", "the build failed", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if sink.got[0].Text != "the build failed" {
		t.Errorf("text = %q, want the body", sink.got[0].Text)
	}
}

// An app's notification is the only copy there will ever be of something that
// already happened, so what cannot be printed is normalised rather than
// refused - the opposite of what a person typing a reminder gets.
func TestNotifyNormalisesWhatItCannotPrint(t *testing.T) {
	sink := &fakeSink{}
	for _, tc := range []struct{ in, want string }{
		{"two\nlines", "two lines"},
		{"tabbed\tacross", "tabbed across"},
		{"  padded  ", "padded"},
		{"bell\x07inside", "bellinside"},
		{"collapse   the    spaces", "collapse the spaces"},
	} {
		sink.got = nil
		if _, derr := notifier(sink).Notify("app", 0, "", tc.in, "", nil, nil, -1); derr != nil {
			t.Fatalf("%q: %v", tc.in, derr)
		}
		if got := sink.got[0].Text; got != tc.want {
			t.Errorf("%q became %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNotifyBoundsTheLength(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify("app", 0, "", strings.Repeat("я", 900), "", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if n := len([]rune(sink.got[0].Text)); n > summaryMax {
		t.Errorf("kept %d characters, want at most %d", n, summaryMax)
	}
}

// Neither summary nor body is a notification that says nothing, and a queue
// item that says nothing cannot be acted on or recognised.
func TestNotifyWithNothingToSay(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify("app", 0, "", "   ", "\n\t", nil, nil, -1); derr == nil {
		t.Error("a notification with no text was accepted")
	}
	if len(sink.got) != 0 {
		t.Errorf("sink got %+v", sink.got)
	}
}

// One download, many updates, one item: the spec's own mechanism, and without
// it a progress bar becomes a hundred reminders.
func TestNotifyReplacesTakesTheOldOneOff(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify("app", 0, "", "downloading 1%", "", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	// The sink is what actually drops it; what this pins is that the id is
	// carried through rather than quietly ignored.
	if _, derr := notifier(sink).Notify("app", 7, "", "downloading 2%", "", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if got := sink.got[1].Replaces; got != 7 {
		t.Errorf("replaces = %d, want the id the app gave", got)
	}
}

// An app that cannot send a byte does not get to interrupt.
func TestUrgencyDefaultsToNormal(t *testing.T) {
	for _, hints := range []map[string]dbus.Variant{
		nil,
		{},
		{"urgency": dbus.MakeVariant("critical")},
		{"urgency": dbus.MakeVariant(int32(2))},
	} {
		sink := &fakeSink{}
		if _, derr := notifier(sink).Notify("app", 0, "", "hello", "", nil, hints, -1); derr != nil {
			t.Fatal(derr)
		}
		if sink.got[0].Urgent {
			t.Errorf("hints %v made it urgent", hints)
		}
	}
}

// A sink that cannot keep it must not answer with an id, or the app believes
// there is something to close later.
func TestNotifyReportsASinkThatRefused(t *testing.T) {
	sink := &fakeSink{err: errors.New("no journal")}
	id, derr := notifier(sink).Notify("app", 0, "", "hello", "", nil, nil, -1)
	if derr == nil {
		t.Fatal("a refused notification was reported as accepted")
	}
	if id != 0 {
		t.Errorf("id = %d, want none", id)
	}
}

// What this server does, and only that. Claiming actions would make apps send
// buttons nothing can press.
func TestCapabilitiesAreOnlyWhatIsTrue(t *testing.T) {
	caps, derr := notifier(&fakeSink{}).GetCapabilities()
	if derr != nil {
		t.Fatal(derr)
	}
	for _, c := range caps {
		switch c {
		case "body", "persistence":
		default:
			t.Errorf("claims %q, which nothing here does", c)
		}
	}
	name, vendor, version, spec, derr := notifier(&fakeSink{}).GetServerInformation()
	if derr != nil || name != "zded" || vendor != "zde" || version != "test" || spec != specLevel {
		t.Errorf("server information = %q %q %q %q", name, vendor, version, spec)
	}
}
