package attn

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

type fakeSink struct {
	got    []Notification
	closed []uint64
	err    error
	nextID uint32
}

func (f *fakeSink) Arrived(n Notification) (uint64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.got = append(f.got, n)
	f.nextID++
	return uint64(f.nextID), nil
}

func (f *fakeSink) Closed(id uint64) error {
	f.closed = append(f.closed, id)
	return nil
}

// The object on the bus, with a server behind it and no connection: every
// method here is reachable without one, which is what makes them testable.
func notifier(sink Sink) *notifications {
	return &notifications{server: &Server{sink: sink, version: "test", mine: map[owned]uint64{}}}
}

// closure watches the NotificationClosed signals a server would put on the
// bus, which is the only way a client learns its notification is gone.
type closure struct{ id, reason uint64 }

func watched(sink Sink) (*notifications, *[]closure) {
	n := notifier(sink)
	var seen []closure
	n.server.emit = func(id uint64, reason uint32) {
		seen = append(seen, closure{id, uint64(reason)})
	}
	return n, &seen
}

const peer = dbus.Sender(":1.7")

func TestNotifyPutsItOnTheQueue(t *testing.T) {
	sink := &fakeSink{}
	id, derr := notifier(sink).Notify(peer, "Fractal", 0, "icon", "Ilya: about the invoice", "body text", nil,
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
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "", "the build failed", nil, nil, -1); derr != nil {
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
		{"bell\x07inside", "bell inside"},
		{"zwj \u200d joined", "zwj \u200d joined"},
		{"collapse   the    spaces", "collapse the spaces"},
	} {
		sink.got = nil
		if _, derr := notifier(sink).Notify(peer, "app", 0, "", tc.in, "", nil, nil, -1); derr != nil {
			t.Fatalf("%q: %v", tc.in, derr)
		}
		if got := sink.got[0].Text; got != tc.want {
			t.Errorf("%q became %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Characters, not bytes, and the same number of them whatever alphabet they
// are written in: counting bytes gives a Russian or Japanese notification a
// half or a third of what an English one keeps.
func TestNotifyBoundsTheLength(t *testing.T) {
	for _, alphabet := range []string{"a", "я", "字"} {
		sink := &fakeSink{}
		if _, derr := notifier(sink).Notify(peer, "app", 0, "", strings.Repeat(alphabet, 900), "", nil, nil, -1); derr != nil {
			t.Fatal(derr)
		}
		if n := len([]rune(sink.got[0].Text)); n != summaryMax {
			t.Errorf("%q: kept %d characters, want %d", alphabet, n, summaryMax)
		}
	}
}

// Neither summary nor body is a notification that says nothing, and a queue
// item that says nothing cannot be acted on or recognised.
func TestNotifyWithNothingToSay(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "   ", "\n\t", nil, nil, -1); derr == nil {
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
	n := notifier(sink)
	first, derr := n.Notify(peer, "curl", 0, "", "downloading 1%", "", nil, nil, -1)
	if derr != nil {
		t.Fatal(derr)
	}
	if _, derr := n.Notify(peer, "curl", first, "", "downloading 2%", "", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if len(sink.closed) != 1 || sink.closed[0] != uint64(first) {
		t.Errorf("closed %v, want the item it replaced", sink.closed)
	}
}

// Every volume OSD reuses one id of its own choosing - notify-send -r 42, and
// dunstify's -r before it. Answering with a fresh id each time and matching
// only on that turns one OSD into a hundred queue items.
func TestNotifyHonoursASendersOwnID(t *testing.T) {
	sink := &fakeSink{}
	n := notifier(sink)
	for _, v := range []string{"10%", "20%", "30%"} {
		if _, derr := n.Notify(peer, "volume", 42, "", "Volume "+v, "", nil, nil, -1); derr != nil {
			t.Fatal(derr)
		}
	}
	// Three sent, two replaced: one item is left standing.
	if len(sink.closed) != 2 {
		t.Errorf("closed %v, want the two it superseded", sink.closed)
	}
}

// A notification id is small and guessable, and the queue holds reminders a
// person typed. Replacing is scoped to the connection that made the thing:
// the bus hands out that name, so unlike an app name it cannot be borrowed.
func TestNotifyWillNotReplaceSomebodyElses(t *testing.T) {
	sink := &fakeSink{}
	n := notifier(sink)
	mine, derr := n.Notify(peer, "curl", 0, "", "mine", "", nil, nil, -1)
	if derr != nil {
		t.Fatal(derr)
	}
	other := dbus.Sender(":1.99")
	if _, derr := n.Notify(other, "impostor", mine, "", "yours now", "", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if len(sink.closed) != 0 {
		t.Errorf("closed %v on somebody else's word", sink.closed)
	}
}

// The same, for the method whose whole job is taking something off.
func TestCloseNotificationOnlyClosesYourOwn(t *testing.T) {
	sink := &fakeSink{}
	n := notifier(sink)
	mine, _ := n.Notify(peer, "curl", 0, "", "mine", "", nil, nil, -1)

	if derr := n.CloseNotification(dbus.Sender(":1.99"), mine); derr != nil {
		t.Fatal(derr)
	}
	if len(sink.closed) != 0 {
		t.Errorf("closed %v for a connection that never sent it", sink.closed)
	}
	// An id nobody was ever given closes nothing either - including the ids of
	// reminders somebody typed, which start at 1 like everything else.
	if derr := n.CloseNotification(peer, 1234); derr != nil {
		t.Fatal(derr)
	}
	if len(sink.closed) != 0 {
		t.Errorf("closed %v for an id it never handed out", sink.closed)
	}
	// Its own, it closes.
	if derr := n.CloseNotification(peer, mine); derr != nil {
		t.Fatal(derr)
	}
	if len(sink.closed) != 1 || sink.closed[0] != uint64(mine) {
		t.Errorf("closed %v, want the one it was given", sink.closed)
	}
}

// The id an app is told is the id the queue gave it, or closing and replacing
// address the wrong thing.
func TestNotifyAnswersWithTheQueuesID(t *testing.T) {
	sink := &fakeSink{nextID: 40}
	id, derr := notifier(sink).Notify(peer, "app", 0, "", "hello", "", nil, nil, -1)
	if derr != nil {
		t.Fatal(derr)
	}
	if id != 41 {
		t.Errorf("id = %d, want the sink's own", id)
	}
}

// A dash in the sender column means a person typed it, so an app that sends
// nothing - or sends a dash - must not land there looking hand-written.
func TestSenderCannotLookHandTyped(t *testing.T) {
	for _, app := range []string{"", "-", "   "} {
		sink := &fakeSink{}
		if _, derr := notifier(sink).Notify(peer, app, 0, "", "hello", "", nil, nil, -1); derr != nil {
			t.Fatal(derr)
		}
		if got := sink.got[0].From; got != string(peer) {
			t.Errorf("app %q was recorded as %q, want the bus name it came from", app, got)
		}
	}
}

// The body is kept even though nothing shows it yet: a notification is meant
// to land in history with its full text, and the center that will show it does
// not exist.
func TestNotifyKeepsTheBody(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "the build failed", "on the third try", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if sink.got[0].Text != "the build failed" || sink.got[0].Body != "on the third try" {
		t.Errorf("notification = %+v, want both halves kept", sink.got[0])
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
		if _, derr := notifier(sink).Notify(peer, "app", 0, "", "hello", "", nil, hints, -1); derr != nil {
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
	id, derr := notifier(sink).Notify(peer, "app", 0, "", "hello", "", nil, nil, -1)
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

// A client blocked on its notification's closure - notify-send --wait is one -
// learns it is gone from this signal and from nothing else. Finishing the
// queue item is a closure the sender did not ask for, and the spec has a
// reason number for exactly that.
func TestDismissedSaysSoOnTheBus(t *testing.T) {
	n, seen := watched(&fakeSink{})
	id, derr := n.Notify(peer, "app", 0, "", "waiting on you", "", nil, nil, -1)
	if derr != nil {
		t.Fatal(derr)
	}
	n.server.Dismissed(uint64(id))
	if len(*seen) != 1 || (*seen)[0].id != uint64(id) || (*seen)[0].reason != ReasonDismissed {
		t.Errorf("signals = %+v, want one dismissal for %d", *seen, id)
	}
	// And the sender's name for it is forgotten, so the number cannot later
	// address whatever item ends up with that id.
	if _, ok := n.server.lookup(owned{peer, id}); ok {
		t.Error("the id still points at something after it was finished")
	}
}

// The spec wants the signal whether or not there was anything to close.
func TestCloseNotificationAlwaysSignals(t *testing.T) {
	n, seen := watched(&fakeSink{})
	if derr := n.CloseNotification(peer, 999); derr != nil {
		t.Fatal(derr)
	}
	if len(*seen) != 1 || (*seen)[0].reason != ReasonClosed {
		t.Errorf("signals = %+v, want one closure by the sender", *seen)
	}
}

// What goes on the bus is what the spec defines and nothing else.
//
// ExportAll publishes every exported method of whatever it is handed, with no
// filtering at all. Handing it the Server published Close, and any peer on the
// session bus could then end notifications for the rest of the session by
// calling it - a bus a sandboxed app is meant to be able to reach
// (docs/vision.md, principle 7). A method added to the wrong type is a quiet
// way to do that again, so the method set is pinned here.
func TestOnlyTheSpecIsOnTheBus(t *testing.T) {
	want := map[string]bool{
		"Notify":               true,
		"CloseNotification":    true,
		"GetCapabilities":      true,
		"GetServerInformation": true,
	}
	typ := reflect.TypeOf(&notifications{})
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if !want[name] {
			t.Errorf("%s is exported to the session bus and is not part of the spec", name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("%s is missing from the object on the bus", name)
	}
}

// The default action is the one a list of rows can offer: choosing the
// notification itself. Reading it off the pairs the spec sends - key, label,
// key, label - is what lets the center say which rows Enter can do anything
// with, and a center that offered every row would refuse most of them after
// the keypress rather than before it.
func TestNotifyNoticesTheDefaultAction(t *testing.T) {
	for _, tc := range []struct {
		actions []string
		want    bool
	}{
		{nil, false},
		{[]string{"default", "Open"}, true},
		{[]string{"reply", "Reply", "default", "Open"}, true},
		// A label is not a key. Reading every position would make "default" as
		// a button's text look like an action that can be invoked.
		{[]string{"open", "default"}, false},
		{[]string{"archive", "Archive"}, false},
	} {
		sink := &fakeSink{}
		if _, derr := notifier(sink).Notify(peer, "app", 0, "", "hello", "", tc.actions, nil, -1); derr != nil {
			t.Fatal(derr)
		}
		if got := sink.got[0].Action; got != tc.want {
			t.Errorf("actions %v gave Action = %v, want %v", tc.actions, got, tc.want)
		}
	}
}

// Invoking is how the notification center acts on a row: the sender hears
// ActionInvoked with the id it was given and the key the spec fixes, which is
// the only thing that makes a notification something you can answer rather
// than only something you can read.
func TestInvokeTellsTheSender(t *testing.T) {
	n, _ := watched(&fakeSink{})
	var fired []string
	n.server.act = func(id uint64, key string) {
		fired = append(fired, strconv.FormatUint(id, 10)+" "+key)
	}
	id, derr := n.Notify(peer, "Fractal", 0, "", "Ilya: about the invoice", "",
		[]string{"default", "Open"}, nil, -1)
	if derr != nil {
		t.Fatal(derr)
	}
	if err := n.server.Invoke(uint64(id)); err != nil {
		t.Fatalf("invoking a notification whose sender is still there: %v", err)
	}
	want := strconv.FormatUint(uint64(id), 10) + " " + DefaultAction
	if len(fired) != 1 || fired[0] != want {
		t.Errorf("emitted %v, want one %q", fired, want)
	}
}

// A signal is a broadcast: one sent for an app that has exited goes out and is
// heard by nobody, and the person is left looking at a row that did something
// invisible. The refusal is what the center puts on the screen.
func TestInvokeRefusesWhenTheAppHasGone(t *testing.T) {
	n, _ := watched(&fakeSink{})
	fired := 0
	n.server.act = func(uint64, string) { fired++ }
	n.server.holds = func(dbus.Sender) bool { return false }
	id, derr := n.Notify(peer, "app", 0, "", "gone by now", "", []string{"default", "Open"}, nil, -1)
	if derr != nil {
		t.Fatal(derr)
	}
	err := n.server.Invoke(uint64(id))
	if err == nil {
		t.Fatal("invoking an action on an app that has exited was reported as done")
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("refusal = %q, want it to say the app has gone", err)
	}
	if fired != 0 {
		t.Errorf("emitted %d signals into nothing", fired)
	}
}

// An id nothing is holding any more - replaced, or finished - has no sender to
// tell. Emitting anyway would address whatever notification later takes that
// number, which is the same reuse the replaces path is scoped to sender to
// prevent.
func TestInvokeRefusesAnIDNobodyHolds(t *testing.T) {
	n, _ := watched(&fakeSink{})
	fired := 0
	n.server.act = func(uint64, string) { fired++ }
	if err := n.server.Invoke(4242); err == nil {
		t.Error("invoking an id the server never handed out was reported as done")
	}
	if fired != 0 {
		t.Errorf("emitted %d signals for an id nobody holds", fired)
	}
}
