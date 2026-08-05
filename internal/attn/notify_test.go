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
	// And what it becomes is a summary, with a summary's bounds: it is going on
	// one line of the queue, where a newline would turn one item into two and
	// the second would have no id - and the body's own bound is more than ten
	// times as long.
	sink.got = nil
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "",
		strings.Repeat("word\n", 400), nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	got := sink.got[0].Text
	if strings.Contains(got, "\n") {
		t.Errorf("the summary taken from a body has newlines in it: %q", got)
	}
	// No more than a summary's worth. Not exactly it: the bound is applied
	// while the words are still being joined, so where it lands depends on
	// where the last word ended.
	if n := len([]rune(got)); n > summaryMax || n == 0 {
		t.Errorf("kept %d characters as the summary, want between one and the summary bound of %d", n, summaryMax)
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

// What zde sends itself goes through the same bounds as what an app sends. It
// is the one arrival nobody on the bus wrote, and it carries a program's own
// output, so a launch that failed with a screenful of it must not be the one
// record in the history with no bound on it.
func TestWhatZdeSendsItselfIsBoundedLikeAnythingElse(t *testing.T) {
	n := Local("zde", strings.Repeat("s", summaryMax*2), strings.Repeat("b", bodyMax*2))
	if got := len([]rune(n.Text)); got != summaryMax {
		t.Errorf("summary kept %d characters, want %d", got, summaryMax)
	}
	if got := len([]rune(n.Body)); got != bodyMax {
		t.Errorf("body kept %d characters, want %d", got, bodyMax)
	}
	if n.From != "zde" {
		t.Errorf("from = %q, want the desktop saying it is the sender", n.From)
	}
	// And the body keeps its lines, because it is a list of what went wrong
	// and one line per thing is the shape of it.
	if body := Local("zde", "two", "one\ntwo").Body; body != "one\ntwo" {
		t.Errorf("body = %q, want its lines as they were written", body)
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

// What this server does, and only that. "actions" is in the list because every
// action a sender declares is offered in the notification center; it must stay
// out of the list the day that stops being true, because an app reads this to
// decide whether to send buttons at all.
func TestCapabilitiesAreOnlyWhatIsTrue(t *testing.T) {
	caps, derr := notifier(&fakeSink{}).GetCapabilities()
	if derr != nil {
		t.Fatal(derr)
	}
	claimed := map[string]bool{}
	for _, c := range caps {
		switch c {
		case "actions", "body", "persistence":
			claimed[c] = true
		default:
			t.Errorf("claims %q, which nothing here does", c)
		}
	}
	if !claimed["actions"] {
		t.Error("the center offers every action a sender declares and this does not say so, " +
			"so apps that ask first will never send one")
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

// Every action a sender declares is kept, in the order it sent them, because
// every one of them is offered in the center - which is what makes claiming the
// spec's "actions" capability true. Keeping only the default was the version of
// this that could not honestly claim it.
func TestNotifyKeepsEveryAction(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify(peer, "Fractal", 0, "", "Ilya: about the invoice", "",
		[]string{"default", "Open", "reply", "Reply", "archive", "Archive"}, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	want := []Action{{"default", "Open"}, {"reply", "Reply"}, {"archive", "Archive"}}
	if !reflect.DeepEqual(sink.got[0].Actions, want) {
		t.Errorf("actions = %+v, want %+v in the order they were sent", sink.got[0].Actions, want)
	}
	if sink.got[0].Extra != 0 {
		t.Errorf("extra = %d, want none: all three were kept", sink.got[0].Extra)
	}
}

// The keys are the even positions and the labels the odd ones. Reading every
// position would offer a button's text as an action key, and pressing it would
// tell the app something it never said it understood.
func TestNotifyReadsActionsAsPairs(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "hello", "",
		[]string{"open", "default"}, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	got := sink.got[0].Actions
	if len(got) != 1 || got[0].Key != "open" || got[0].Label != "default" {
		t.Errorf("actions = %+v, want one action keyed open and labelled default", got)
	}
	// And a key with no label is the sender's mistake, not a reason to drop
	// something it declared: its key becomes what a person reads.
	sink.got = nil
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "hello", "",
		[]string{"reply"}, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if got := sink.got[0].Actions; len(got) != 1 || got[0].Key != "reply" || got[0].Label != "reply" {
		t.Errorf("actions = %+v, want the key standing in for the missing label", got)
	}
}

// The list comes from an app on the session bus and nothing stops one sending a
// thousand. It is bounded at what the center can offer with one keypress each,
// and what was declared beyond it is counted so the surface can say so rather
// than showing a list that quietly stops.
func TestNotifyBoundsTheActions(t *testing.T) {
	var sent []string
	for i := 0; i < actionsMax+3; i++ {
		sent = append(sent, "key"+strconv.Itoa(i), "Label "+strconv.Itoa(i))
	}
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "hello", "", sent, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	got := sink.got[0]
	if len(got.Actions) != actionsMax {
		t.Errorf("kept %d actions, want the bound of %d", len(got.Actions), actionsMax)
	}
	if got.Extra != 3 {
		t.Errorf("extra = %d, want the 3 that did not fit", got.Extra)
	}
}

// Invoking is how the center acts on a row: the sender hears ActionInvoked with
// the id it was given and the key it declared. The key that comes back is the
// one that was pressed and not always the default - a center that offers three
// buttons and always sends "default" would archive what somebody meant to
// reply to.
func TestInvokeTellsTheSenderWhichAction(t *testing.T) {
	for _, key := range []string{DefaultAction, "reply"} {
		n, _ := watched(&fakeSink{})
		var fired []string
		n.server.act = func(id uint64, key string) {
			fired = append(fired, strconv.FormatUint(id, 10)+" "+key)
		}
		id, derr := n.Notify(peer, "Fractal", 0, "", "Ilya: about the invoice", "",
			[]string{"default", "Open", "reply", "Reply"}, nil, -1)
		if derr != nil {
			t.Fatal(derr)
		}
		if err := n.server.Invoke(uint64(id), key); err != nil {
			t.Fatalf("invoking %q on a sender that is still there: %v", key, err)
		}
		want := strconv.FormatUint(uint64(id), 10) + " " + key
		if len(fired) != 1 || fired[0] != want {
			t.Errorf("emitted %v, want one %q", fired, want)
		}
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
	err := n.server.Invoke(uint64(id), DefaultAction)
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
	if err := n.server.Invoke(4242, DefaultAction); err == nil {
		t.Error("invoking an id the server never handed out was reported as done")
	}
	if fired != 0 {
		t.Errorf("emitted %d signals for an id nobody holds", fired)
	}
}

// What the server remembers about senders is bounded by what can still be
// addressed. It used to grow by one entry for every notification a session ever
// received, and the modes are what made that unprunable: a notification a mode
// keeps off the queue is one nobody can finish, so nothing else would ever
// reach it. Half a million notifications is a long uptime, not an attack.
func TestForgettingLeavesNothingBehind(t *testing.T) {
	n := notifier(&fakeSink{})
	for i := 0; i < 50; i++ {
		id, derr := n.Notify(peer, "app", 42, "", "one of many", "", nil, nil, -1)
		if derr != nil {
			t.Fatal(derr)
		}
		n.server.Forget(uint64(id))
	}
	if len(n.server.mine) != 0 || len(n.server.by) != 0 {
		t.Errorf("after forgetting everything: %d names and %d items still remembered",
			len(n.server.mine), len(n.server.by))
	}
}

// Forgetting one notification must not take another's names with it.
//
// The collision is real and not theoretical: a sender that reuses a fixed id
// (notify-send -r 3) has named a notification 3, and the journal will hand the
// number 3 to some later notification from anybody, including that same sender.
// The name then belongs to the newer one, and a table that still listed it
// under the older would delete a live entry when the older was forgotten - so
// the app could no longer close or replace the notification it is holding.
func TestForgettingOneLeavesTheOthersAddressable(t *testing.T) {
	n := notifier(&fakeSink{})
	// Named 3 by its sender, and given 1 by the journal.
	older, derr := n.Notify(peer, "app", 3, "", "the old one", "", nil, nil, -1)
	if derr != nil {
		t.Fatal(derr)
	}
	// Two more, so that the second of them is given 3 by the journal - the
	// number the first one is already known by.
	var newer uint32
	for _, text := range []string{"another", "the one that gets id 3"} {
		newer, derr = n.Notify(peer, "app", 0, "", text, "", nil, nil, -1)
		if derr != nil {
			t.Fatal(derr)
		}
	}
	if newer != 3 {
		t.Fatalf("the third notification got id %d, so this test is not testing the collision", newer)
	}

	n.server.Forget(uint64(older))
	if got, ok := n.server.lookup(owned{peer, newer}); !ok || got != uint64(newer) {
		t.Errorf("forgetting %d took the name %d with it: the app can no longer close its own notification", older, newer)
	}
	if _, ok := n.server.senderOf(uint64(newer)); !ok {
		t.Error("the live notification has no sender any more, so its actions cannot be invoked")
	}
}

// The body is what somebody comes back to the center to read, so it arrives
// whole. Bounded at the summary's 300 characters it was cutting an ordinary
// two-paragraph message in half, while five files said notifications land in
// history with what was sent.
func TestNotifyKeepsAWholeBody(t *testing.T) {
	sink := &fakeSink{}
	ordinary := strings.Repeat("word ", 300) // 1500 characters, five times the summary bound
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "the build failed", ordinary, nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if n := len([]rune(sink.got[0].Body)); n != len([]rune(strings.TrimSpace(ordinary))) {
		t.Errorf("kept %d characters of a %d character body", n, len([]rune(ordinary)))
	}
	// And it is still a bound, because the body is whatever an app felt like
	// sending and it is what decides how big the history gets.
	sink.got = nil
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "hello", strings.Repeat("я", bodyMax+500), nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if n := len([]rune(sink.got[0].Body)); n != bodyMax {
		t.Errorf("kept %d characters, want the bound of %d", n, bodyMax)
	}
}

// And with its lines. A body is where the paragraph goes, and one flattened
// into a single line is the shape of the message lost - which is the half the
// center exists to show.
func TestNotifyKeepsTheBodysLines(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "three things",
		"first\n\n  second  \nthird", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if got := sink.got[0].Body; got != "first\nsecond\nthird" {
		t.Errorf("body = %q, want its three lines with the blank space between them collapsed", got)
	}
	// The summary is still one line: it goes on one line of the queue, where a
	// newline would turn one item into two and the second would have no id.
	sink.got = nil
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "two\nlines", "", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if got := sink.got[0].Text; got != "two lines" {
		t.Errorf("summary = %q, want it flattened to one line", got)
	}
}

// A notification with no summary is ordinary - notify-send with one argument
// sends one - and it used to be the one shape where zde kept less than the app
// sent: the body was promoted to the summary, bounded at the summary's 300
// characters, and then thrown away. Everything past the first line of a
// message that arrived whole was gone.
func TestNotifyKeepsTheBodyOfASummarylessNotification(t *testing.T) {
	sink := &fakeSink{}
	long := strings.Repeat("word ", 400) // 2000 characters, and no summary at all
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "", long, nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	got := sink.got[0]
	// The row is a row: bounded like the summary it is standing in for.
	if n := len([]rune(got.Text)); n > summaryMax || n == 0 {
		t.Errorf("summary is %d characters, want between one and %d", n, summaryMax)
	}
	// And the message is all there, under the body's own bound.
	if n := len([]rune(got.Body)); n != len([]rune(strings.TrimSpace(long))) {
		t.Errorf("kept %d characters of body, want the whole %d that arrived",
			n, len([]rune(strings.TrimSpace(long))))
	}
}

// And when the whole message fits on the line, there is nothing left to show
// under it: the center would draw the same sentence twice, once as the row and
// once as its body.
func TestNotifyDoesNotRepeatAShortBody(t *testing.T) {
	sink := &fakeSink{}
	if _, derr := notifier(sink).Notify(peer, "app", 0, "", "", "the build failed", nil, nil, -1); derr != nil {
		t.Fatal(derr)
	}
	if got := sink.got[0]; got.Text != "the build failed" || got.Body != "" {
		t.Errorf("notification = %+v, want the line as the summary and nothing repeated under it", got)
	}
}
