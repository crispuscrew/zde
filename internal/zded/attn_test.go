package zded

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

// arrive sends one notification into a server whose mode has been set, and
// answers with the queue and the history that came of it. One helper because
// every mode test asks the same two questions of the same arrival.
func arrive(t *testing.T, mode string, n attn.Notification) (*Server, []attn.Record) {
	t.Helper()
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	if mode != "" {
		if err := jrn.SetMode(mode); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Arrived(n); err != nil {
		t.Fatalf("the notification was lost: %v", err)
	}
	return s, s.history.Recent()
}

// Work is the default, and in it a notification is a queue item on the desk it
// arrived on. This is the behaviour every other mode is a departure from, so a
// work mode that quietly stopped queueing would make the other two untestable
// as differences.
func TestWorkModeQueuesEverything(t *testing.T) {
	s, seen := arrive(t, "work", attn.Notification{From: "ci", Text: "the build failed"})
	if q := s.jrn.State().Queue; len(q) != 1 || q[0].Text != "the build failed" {
		t.Errorf("queue = %+v, want the arrival on it", q)
	}
	if len(seen) != 1 || !seen[0].Queued {
		t.Errorf("history = %+v, want one record that says it was queued", seen)
	}
}

// Focus keeps the ordinary out of the way and lets the emergency through. The
// one that did not reach the queue is still in the history with its full text,
// which is principle 3: the mode changed what interrupts, not what happened.
func TestFocusModeQueuesOnlyTheUrgent(t *testing.T) {
	s, seen := arrive(t, "focus", attn.Notification{From: "chat", Text: "lunch?", Body: "at one"})
	if q := s.jrn.State().Queue; len(q) != 0 {
		t.Errorf("queue = %+v, want an ordinary arrival kept off it in focus", q)
	}
	if len(seen) != 1 || seen[0].Queued {
		t.Fatalf("history = %+v, want one record that says it was not queued", seen)
	}
	if seen[0].Text != "lunch?" || seen[0].Body != "at one" || seen[0].Desk != "vshop" {
		t.Errorf("record = %+v, want the whole thing kept with the desk it arrived on", seen[0])
	}

	urgent, _ := arrive(t, "focus", attn.Notification{From: "ci", Text: "production is down", Urgent: true})
	if q := urgent.jrn.State().Queue; len(q) != 1 {
		t.Errorf("queue = %+v, want the urgent one through in focus", q)
	}
}

// Quiet is nothing at all reaching the queue, the urgent included: it is the
// mode for a screencast, and one that let critical notifications through would
// be a quiet nobody can share a screen in.
func TestQuietModeQueuesNothing(t *testing.T) {
	s, seen := arrive(t, "quiet", attn.Notification{From: "ci", Text: "production is down", Urgent: true})
	if q := s.jrn.State().Queue; len(q) != 0 {
		t.Errorf("queue = %+v, want nothing on it in quiet", q)
	}
	if len(seen) != 1 || seen[0].Queued || !seen[0].Urgent {
		t.Errorf("history = %+v, want the urgent one recorded and not queued", seen)
	}
}

// An id is an id whatever the mode did with it. The app that sent it addresses
// it by that number on the bus, and a number the queue later hands to a
// reminder somebody typed would let that app close the reminder.
func TestASilencedNotificationStillGetsAnIDOfItsOwn(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	if err := jrn.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}
	silenced, err := s.Arrived(attn.Notification{From: "app", Text: "you missed this"})
	if err != nil {
		t.Fatal(err)
	}
	if silenced == 0 {
		t.Fatal("a notification the mode silenced was given no id, so nothing can address it")
	}
	if err := jrn.SetMode("work"); err != nil {
		t.Fatal(err)
	}
	queued, err := s.Arrived(attn.Notification{From: "app", Text: "and then this"})
	if err != nil {
		t.Fatal(err)
	}
	if queued == silenced {
		t.Errorf("the queue handed out id %d, which a silenced notification already has", queued)
	}
}

// The mode is read from the journal on every arrival rather than remembered in
// the daemon, which is what makes `zde attn quiet` take effect on the next
// notification instead of on the next restart.
func TestChangingTheModeChangesTheNextArrival(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	if _, err := s.Arrived(attn.Notification{From: "app", Text: "before"}); err != nil {
		t.Fatal(err)
	}
	if resp := s.Dispatch(Request{Method: "attn.quiet"}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if _, err := s.Arrived(attn.Notification{From: "app", Text: "after"}); err != nil {
		t.Fatal(err)
	}
	q := jrn.State().Queue
	if len(q) != 1 || q[0].Text != "before" {
		t.Errorf("queue = %+v, want only what arrived before the mode changed", q)
	}
}

// Mod+q is one key for both directions: into quiet, and out of it into work.
// Out of focus it goes to quiet first, because the key means "silence now" -
// and out of quiet it never goes back to focus, since a toggle that answered
// with two different modes depending on invisible history is not a toggle.
func TestQuietTogglesAgainstWork(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	for _, tc := range []struct{ from, want string }{
		{"work", "quiet"},
		{"quiet", "work"},
		{"focus", "quiet"},
	} {
		if err := jrn.SetMode(tc.from); err != nil {
			t.Fatal(err)
		}
		var got Attn
		call(t, s, Request{Method: "attn.quiet"}, &got)
		if got.Mode != tc.want {
			t.Errorf("quiet from %s went to %s, want %s", tc.from, got.Mode, tc.want)
		}
		if now := jrn.State().Mode; now != tc.want {
			t.Errorf("the journal says %s after toggling from %s, want %s", now, tc.from, tc.want)
		}
	}
}

// A mode nobody has heard of is refused rather than written down: a typo that
// reached the journal would be read back as the default for the rest of the
// session, and the person who typed it would be told nothing.
func TestSettingAModeThatIsNotOne(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	resp := s.Dispatch(Request{Method: "attn.mode", Args: []string{"silent"}})
	if resp.Error == "" {
		t.Fatal("\"silent\" was accepted as a mode")
	}
	if got := jrn.State().Mode; got != "" {
		t.Errorf("the journal now says %q", got)
	}
}

// `zde status` is what somebody runs when a key did nothing, and "why has
// nothing arrived all afternoon" has exactly one cheap answer.
func TestStatusSaysWhichMode(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	if st := s.status(); st.Mode != "work" {
		t.Errorf("a session nobody has told says %q, want work", st.Mode)
	}
	if err := jrn.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}
	if st := s.status(); st.Mode != "quiet" {
		t.Errorf("status says %q while the journal says quiet", st.Mode)
	}
}

// Finishing something takes it off the queue and marks it in the history. A
// center still showing it as waiting sends somebody back to a desk for
// something that is done.
func TestFinishingMarksTheHistory(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	id, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"})
	if err != nil {
		t.Fatal(err)
	}
	if resp := s.Dispatch(Request{Method: "queue.done", Args: []string{strconv.FormatUint(id, 10)}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	seen := s.history.Recent()
	if len(seen) != 1 || !seen[0].Dismissed {
		t.Errorf("history = %+v, want the record marked as no longer waiting", seen)
	}
}

// The same id addresses a notification the mode kept out of the queue, so the
// center's dismiss works on the rows that never waited anywhere - which in
// quiet mode is all of them.
func TestASilencedNotificationCanBeDismissed(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	if err := jrn.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}
	id, err := s.Arrived(attn.Notification{From: "app", Text: "you missed this"})
	if err != nil {
		t.Fatal(err)
	}
	if resp := s.Dispatch(Request{Method: "queue.done", Args: []string{strconv.FormatUint(id, 10)}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	seen := s.history.Recent()
	if len(seen) != 1 || !seen[0].Dismissed {
		t.Errorf("history = %+v, want the silenced one marked", seen)
	}
}

// With no shell listening, Mod+n has to print rather than ask a surface that is
// not there - the same bargain the desk switcher makes, and the reason the CLI
// can answer "what did I miss" on a session whose shell has died.
func TestCenterFallsBackToTheList(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	var got Center
	call(t, s, Request{Method: "attn.center"}, &got)
	if got.Shown {
		t.Error("nothing was listening and the center says it was shown")
	}
	if len(got.Notifications) != 1 || got.Notifications[0].Text != "the build failed" {
		t.Errorf("center = %+v, want the history to print", got.Notifications)
	}
}

// A row nobody can act on is a row to say so about: a notice is not a button,
// and a key that did nothing on it would read as the surface being broken.
func TestInvokingWhatHasNoAction(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	told := []uint64{}
	invoked := []string{}
	s.Watching(tellTale{ids: &told, invoked: &invoked})
	id, err := s.Arrived(attn.Notification{From: "app", Text: "no button on this"})
	if err != nil {
		t.Fatal(err)
	}
	resp := s.Dispatch(Request{Method: "attn.invoke", Args: []string{strconv.FormatUint(id, 10), attn.DefaultAction}})
	if resp.Error == "" {
		t.Fatal("a notification with no actions was reported as invoked")
	}
	if len(invoked) != 0 {
		t.Errorf("told the bus about %v anyway", invoked)
	}
}

// Every action the sender declared is pressable, not only the default. The
// center offers them a key each, so the one that was pressed is the one that
// has to reach the app - anything else archives what somebody meant to reply
// to.
func TestInvokingAnActionThatIsNotTheDefault(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	told := []uint64{}
	invoked := []string{}
	s.Watching(tellTale{ids: &told, invoked: &invoked})
	id, err := s.Arrived(attn.Notification{From: "Fractal", Text: "Ilya: about the invoice", Actions: []attn.Action{
		{Key: attn.DefaultAction, Label: "Open"},
		{Key: "reply", Label: "Reply"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{attn.DefaultAction, "reply"} {
		invoked = nil
		resp := s.Dispatch(Request{Method: "attn.invoke", Args: []string{strconv.FormatUint(id, 10), key}})
		if resp.Error != "" {
			t.Fatalf("invoking %q, which the sender declared: %s", key, resp.Error)
		}
		want := strconv.FormatUint(id, 10) + " " + key
		if len(invoked) != 1 || invoked[0] != want {
			t.Errorf("invoked %v, want %q", invoked, want)
		}
	}
}

// A key the notification never offered is refused. The other end of this socket
// is a surface, and passing whatever it says through to the bus would let zde
// tell an app that a button was pressed which that app never put on anything.
func TestInvokingAKeyTheSenderNeverOffered(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	told := []uint64{}
	invoked := []string{}
	s.Watching(tellTale{ids: &told, invoked: &invoked})
	id, err := s.Arrived(attn.Notification{From: "Fractal", Text: "Ilya: about the invoice", Actions: []attn.Action{
		{Key: attn.DefaultAction, Label: "Open"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resp := s.Dispatch(Request{Method: "attn.invoke", Args: []string{strconv.FormatUint(id, 10), "delete-everything"}})
	if resp.Error == "" {
		t.Fatal("a key nobody declared was passed to the bus")
	}
	if len(invoked) != 0 {
		t.Errorf("told the bus about %v anyway", invoked)
	}
}

// What the bus says about an app that has exited is what the surface shows.
// Swallowing it would leave somebody looking at a row that did something
// invisible.
func TestInvokeReportsWhatTheBusSaid(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	told := []uint64{}
	s.Watching(tellTale{ids: &told, err: errors.New("the app that sent this has exited")})
	id, err := s.Arrived(attn.Notification{From: "app", Text: "gone by now", Actions: []attn.Action{
		{Key: attn.DefaultAction, Label: "Open"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resp := s.Dispatch(Request{Method: "attn.invoke", Args: []string{strconv.FormatUint(id, 10), attn.DefaultAction}})
	if resp.Error != "the app that sent this has exited" {
		t.Errorf("error = %q, want what the bus said", resp.Error)
	}
}

// call dispatches and decodes what came back, failing the test on a refusal.
func call(t *testing.T, s *Server, req Request, out any) {
	t.Helper()
	resp := s.Dispatch(req)
	if resp.Error != "" {
		t.Fatalf("%s: %s", req.Method, resp.Error)
	}
	if out == nil {
		return
	}
	if err := json.Unmarshal(resp.Ok, out); err != nil {
		t.Fatalf("%s: %v", req.Method, err)
	}
}

// A record falling off the end of the history is the moment nothing can address
// that notification any more, so the bus side is told to let go of it. It is the
// only thing that prunes what attn remembers about senders: a notification a
// mode kept off the queue was never on it to be finished.
func TestARecordLeavingTheHistoryForgetsItsSender(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	// Quiet, so nothing is queued and nothing can ever be finished - the shape
	// that made this leak unprunable in the first place.
	if err := jrn.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}
	forgotten := []uint64{}
	s.Watching(tellTale{ids: &[]uint64{}, forgotten: &forgotten})

	var first uint64
	for i := 0; i < attn.HistoryMax+2; i++ {
		id, err := s.Arrived(attn.Notification{From: "app", Text: "one of many"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = id
		}
	}
	if len(forgotten) != 2 || forgotten[0] != first {
		t.Errorf("forgot %v, want the two that fell off the end, oldest first (%d)", forgotten, first)
	}
	// And nothing was forgotten that is still on the list, or the center would
	// be showing rows the bus can no longer act on.
	if seen := s.history.Recent(); len(seen) != attn.HistoryMax {
		t.Errorf("history holds %d, want the bound", len(seen))
	}
}

// Nothing is quiet about the moment zded becomes the notification server.
//
// attn.Serve exports the interface and takes org.freedesktop.Notifications
// before it returns, so from that instant any app on the session bus can call
// Notify, which arrives here - and the daemon is answering its own socket by
// then as well (cmd/zded). Watching is what runs after all of that, and it
// writes the field the arrival path reads.
//
// Break it - the bare assignment this used to be, under a comment claiming
// there was nothing to lock against - and `go test -race` says so from here. An
// interface value is two words, and a torn read of one is not a nil check that
// comes out wrong, it is a call through an address that was never a method
// table.
func TestSettingTheNotifierWhileThingsArriveIsNotARace(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	// Quiet, so each arrival spends an id rather than queueing: the same path,
	// with less written down on the way.
	if err := jrn.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}
	// The history full, because what an arrival reads the notifier for is
	// telling the bus side that a record has fallen off the end of it.
	for i := 0; i < attn.HistoryMax; i++ {
		if _, err := s.Arrived(attn.Notification{From: "app", Text: "one of many"}); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	arriving := make(chan error, 1)
	go func() {
		<-start
		for i := 0; i < 200; i++ {
			if _, err := s.Arrived(attn.Notification{From: "app", Text: "and another"}); err != nil {
				arriving <- err
				return
			}
		}
		arriving <- nil
	}()
	close(start)
	s.Watching(tellTale{ids: &[]uint64{}, forgotten: &[]uint64{}})
	if err := <-arriving; err != nil {
		t.Fatal(err)
	}
}

// eventsOf reads the events of one kind a listener has been sent. The popup
// path writes from a goroutine of its own, so a test that walked the buffer once
// straight after Arrived would be racing the whole point of it.
func eventsOf(rec *recorder, kind string) []Event {
	var out []Event
	for _, line := range strings.Split(rec.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var got struct{ Event Event }
		if json.Unmarshal([]byte(line), &got) != nil {
			continue
		}
		if got.Event.Kind == kind {
			out = append(out, got.Event)
		}
	}
	return out
}

// waitForEvents waits until a listener has been sent at least this many of one
// kind, and answers with what it has either way, so the assertion is about what
// arrived rather than about a timeout.
func waitForEvents(t *testing.T, rec *recorder, kind string, want int) []Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := eventsOf(rec, kind)
		if len(got) >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(time.Millisecond)
	}
}

// A notification arriving is a card on the screen with the sender's own buttons
// on it, and not only a row waiting behind Mod+n. That is the difference between
// the "actions" capability being a claim about reach and one about immediacy: an
// app that sends archive and delete has, until now, been offering them to
// somebody who had to go and look (internal/attn, GetCapabilities).
func TestAnArrivalIsPutInFrontOfYouWithTheSendersButtonsOnIt(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	defer s.Close()
	rec := &recorder{}
	s.listen(&sink{w: rec})

	id, err := s.Arrived(attn.Notification{
		From: "Fractal", Text: "Ilya: about the invoice", Body: "the one from March",
		Actions: []attn.Action{{Key: attn.DefaultAction, Label: "Open"}, {Key: "reply", Label: "Reply"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := waitForEvents(t, rec, EventAttnPopup, 1)
	if len(got) != 1 {
		t.Fatalf("the shell was sent %d popups, want the one that arrived", len(got))
	}
	ev := got[0]
	if len(ev.Notifications) != 1 {
		t.Fatalf("the popup carries %d records, want the one it is about", len(ev.Notifications))
	}
	r := ev.Notifications[0]
	if r.ID != id || r.Text != "Ilya: about the invoice" || r.Body != "the one from March" {
		t.Errorf("popup record = %+v, want the arrival whole", r)
	}
	if len(r.Actions) != 2 || r.Actions[1].Key != "reply" || r.Actions[1].Label != "Reply" {
		t.Errorf("popup actions = %+v, want every one the sender declared, with its label", r.Actions)
	}
	// And on the screen the person is looking at, the way every other surface
	// zded asks for is: a popup on the monitor you are not using is a popup you
	// find out about afterwards (docs/model.md, invariant 1).
	if ev.Output != "DP-1" {
		t.Errorf("popup output = %q, want the screen being looked at", ev.Output)
	}
}

// A popup asks for no acknowledgement, and that is the one thing about it worth
// pinning in the daemon: the token is how a surface is told to take the keyboard
// and report back, and an arrival must never be able to ask for that. Only
// attn.reach does, which is a person pressing a key.
//
// The other half of the same fact: nothing on the arrival path waits. Give it a
// listener that will not finish taking a line and the notification still lands,
// because the hand-off is a buffered channel and a goroutine (see pop).
func TestAPopupAsksForNoKeyboardAndTheArrivalWaitsForNoShell(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	defer s.Close()
	rec := &recorder{}
	s.listen(&sink{w: rec})
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	got := waitForEvents(t, rec, EventAttnPopup, 1)
	if len(got) != 1 {
		t.Fatalf("the shell was sent %d popups, want one", len(got))
	}
	if got[0].Token != "" {
		t.Errorf("the popup carries token %q: an arriving notification can ask for the keyboard", got[0].Token)
	}
}

// Quiet shows nothing at all, and keeps everything. It is the mode for a
// screencast: a card sliding onto the screen is exactly what somebody in it has
// decided is worse than being late. What it must not do is forget, because a
// mode that changed what is recorded would be a mode that decides what happened
// (docs/vision.md, principle 3).
//
// Ordered rather than timed: the pump writes in the order things arrived, so a
// popup for the second one proves the first was never sent.
func TestQuietShowsNoPopupAndStillKeepsWhatArrived(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	defer s.Close()
	rec := &recorder{}
	s.listen(&sink{w: rec})

	if err := jrn.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "production is down", Urgent: true}); err != nil {
		t.Fatal(err)
	}
	if err := jrn.SetMode("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Arrived(attn.Notification{From: "chat", Text: "lunch?"}); err != nil {
		t.Fatal(err)
	}

	got := waitForEvents(t, rec, EventAttnPopup, 1)
	if len(got) != 1 {
		t.Fatalf("%d popups reached the shell, want only the one that arrived in work", len(got))
	}
	if got[0].Notifications[0].Text != "lunch?" {
		t.Errorf("the popup was %q: quiet put a card on the screen", got[0].Notifications[0].Text)
	}
	if seen := s.history.Recent(); len(seen) != 2 {
		t.Errorf("history holds %d, want both: quiet stops the popup and never the record", len(seen))
	}
}

// Focus shows the emergency and keeps the rest. It is the mode somebody leaves
// on all afternoon, so a focus that showed every arrival would be work with a
// different word on the bar, and one that showed none would be quiet.
func TestFocusShowsOnlyTheUrgentAndKeepsBoth(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	defer s.Close()
	rec := &recorder{}
	s.listen(&sink{w: rec})

	if err := jrn.SetMode("focus"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Arrived(attn.Notification{From: "chat", Text: "lunch?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "production is down", Urgent: true}); err != nil {
		t.Fatal(err)
	}

	got := waitForEvents(t, rec, EventAttnPopup, 1)
	if len(got) != 1 {
		t.Fatalf("%d popups reached the shell in focus, want only the urgent one", len(got))
	}
	if got[0].Notifications[0].Text != "production is down" {
		t.Errorf("the popup was %q, want the one the sender called urgent", got[0].Notifications[0].Text)
	}
	if seen := s.history.Recent(); len(seen) != 2 {
		t.Errorf("history holds %d, want both", len(seen))
	}
}

// popupServer is a daemon standing on one desk of a directory of manifests,
// with a journal under it so that arrivals can be recorded and a listener to
// see what was drawn.
//
// Its own helper rather than queueTestServer, which is handed no manifests at
// all: a machine that declares no desks cannot tell a private one from an open
// one, and that is the whole question here.
func popupServer(t *testing.T, focused string, manifests map[string]string) (*Server, *recorder) {
	t.Helper()
	dir := t.TempDir()
	desks := filepath.Join(dir, "desks")
	if err := os.MkdirAll(desks, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range manifests {
		if err := os.WriteFile(filepath.Join(desks, name+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	jrn, err := journal.Open(filepath.Join(dir, "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	m := desk.Rebuild([]desk.Workspace{
		{Name: "work.DP-1.code", Output: "DP-1"},
		{Name: "clinic.DP-1.mail", Output: "DP-1"},
	}, []string{"DP-1"})
	s := New("test", jrn, &fakeCompositor{m: m, focused: focused, output: "DP-1"}, manifest.Dir(desks))
	t.Cleanup(func() { s.Close() })
	rec := &recorder{}
	s.listen(&sink{w: rec})
	return s, rec
}

// A desk that declares itself private is popups off, history only
// (docs/vision.md, section 3). The mode gate alone did not know that, so a
// notification arriving while somebody stood on their private desk put its
// sender, its summary and its body on the screen for five seconds - which is
// the exact thing that desk exists to prevent, and worse than the queue-only
// behaviour it replaced.
//
// Both halves in one test, because either alone would pass on a bug: drawing no
// card ever keeps every secret, and drawing every card keeps none. And the
// private one is still in the history, because this is display policy and the
// mode gate makes the same argument (docs/vision.md, principle 3).
func TestANotificationFromAPrivateDeskIsNeverPutOnTheScreen(t *testing.T) {
	s, rec := popupServer(t, "clinic.DP-1.mail", map[string]string{
		"work":   "name: work\nmonitors: { DP-1: { workspaces: [code] } }\n",
		"clinic": "name: clinic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n",
	})
	if _, err := s.Arrived(attn.Notification{From: "clinic", Text: "your results are in", Body: "the clinic wrote back"}); err != nil {
		t.Fatal(err)
	}
	// And one from the ordinary desk, which is what proves the first was
	// withheld rather than the popup path being broken. The pump writes in the
	// order things arrived, so a card for the second is a card the first never
	// got.
	s.niri.(*fakeCompositor).focused = "work.DP-1.code"
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}

	got := waitForEvents(t, rec, EventAttnPopup, 1)
	if len(got) != 1 {
		t.Fatalf("%d cards were drawn, want only the one from the open desk", len(got))
	}
	if drawn := got[0].Notifications[0]; drawn.Text != "the build failed" {
		t.Errorf("the card said %q, so a private desk's notification was on the screen", drawn.Text)
	}
	// And the private one is kept, whole, with the desk it arrived on.
	seen := s.history.Recent()
	if len(seen) != 2 {
		t.Fatalf("history holds %d, want both: a private desk stops the card and never the record", len(seen))
	}
	private := seen[1]
	if private.Text != "your results are in" || private.Body != "the clinic wrote back" || private.Desk != "clinic" {
		t.Errorf("record = %+v, want the private arrival kept whole with its desk", private)
	}
}

// A machine that declares a private desk and cannot say which desk something
// arrived on draws nothing, because the answer it cannot give is the one that
// matters. Fail closed is principle 9, and here the cost of guessing wrong is a
// private desk's notification on a screen somebody is recording.
//
// The ordinary machine declares no private desk at all, and there this refusal
// must not fire: a compositor that cannot be read would otherwise turn every
// popup off for the rest of the session.
func TestAnArrivalWithNoDeskDrawsNothingOnlyWhereADeskIsPrivate(t *testing.T) {
	guarded, rec := popupServer(t, "", map[string]string{
		"work":   "name: work\nmonitors: { DP-1: { workspaces: [code] } }\n",
		"clinic": "name: clinic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n",
	})
	// A compositor that cannot be read, so nothing can say which desk this
	// arrived on - a wedged niri, or a session before anything is adopted.
	guarded.niri.(*fakeCompositor).err = errors.New("no compositor")
	if _, err := guarded.Arrived(attn.Notification{From: "app", Text: "could be anything"}); err != nil {
		t.Fatal(err)
	}
	// And then one that is placeable, so the first can be shown to have been
	// withheld rather than merely still in flight: the pump writes in the order
	// things arrived, so a card for the second is a card the first never got.
	// Asserted by ordering and not by a sleep, because a sleep would pass on the
	// day the popup path got slower.
	guarded.niri.(*fakeCompositor).err = nil
	guarded.niri.(*fakeCompositor).focused = "work.DP-1.code"
	if _, err := guarded.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	got := waitForEvents(t, rec, EventAttnPopup, 1)
	if len(got) != 1 {
		t.Fatalf("%d cards were drawn, want only the one that could be placed", len(got))
	}
	if drawn := got[0].Notifications[0]; drawn.Text != "the build failed" {
		t.Errorf("the card said %q: a machine with a private desk drew one for an arrival it could not place", drawn.Text)
	}
	if seen := guarded.history.Recent(); len(seen) != 2 {
		t.Errorf("history holds %d, want both kept even though one drew no card", len(seen))
	}

	open, openRec := popupServer(t, "", map[string]string{
		"work": "name: work\nmonitors: { DP-1: { workspaces: [code] } }\n",
	})
	open.niri.(*fakeCompositor).err = errors.New("no compositor")
	if _, err := open.Arrived(attn.Notification{From: "app", Text: "could be anything"}); err != nil {
		t.Fatal(err)
	}
	if got := waitForEvents(t, openRec, EventAttnPopup, 1); len(got) != 1 {
		t.Errorf("a machine with nothing private drew %d cards: an unreadable compositor turned the popups off", len(got))
	}
}

// held is a listener that takes the first line and does not finish taking it
// until the test says so. A shell mid-frame, or one that has stopped drawing and
// not closed its socket.
type held struct {
	mu      sync.Mutex
	lines   int
	release chan struct{}
}

func (h *held) Write(p []byte) (int, error) {
	h.mu.Lock()
	first := h.lines == 0
	h.lines++
	h.mu.Unlock()
	if first {
		<-h.release
	}
	return len(p), nil
}

func (h *held) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lines
}

// A build bot can send a hundred in a minute, and the shell draws at human
// speed. What must not happen is either half of the obvious failure: the arrival
// path waiting on a surface, or the daemon growing a queue of undrawn cards.
//
// So the hand-off is bounded and non-blocking. This sends a flood at a listener
// that is stuck on its first line and asserts both ends of that: every one of
// them is in the history, and what the shell was ever offered stops at the
// backlog. A synchronous popup path fails this by hanging on the first arrival,
// which is the failure worth being unable to miss.
func TestAFloodOfArrivalsNeverWaitsForTheShellAndLosesNoRecord(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	defer s.Close()
	h := &held{release: make(chan struct{})}
	s.listen(&sink{w: h})

	const flood = popupBacklog * 4
	for i := 0; i < flood; i++ {
		if _, err := s.Arrived(attn.Notification{From: "ci", Text: "build " + strconv.Itoa(i)}); err != nil {
			t.Fatalf("arrival %d was lost: %v", i, err)
		}
	}
	if seen := s.history.Recent(); len(seen) != flood {
		t.Errorf("history holds %d of %d arrivals: the popup path cost a record", len(seen), flood)
	}

	// And now let it go, so what the backlog held can be counted.
	close(h.release)
	settled, last := 0, -1
	for i := 0; i < 200 && settled < 3; i++ {
		if n := h.count(); n == last {
			settled++
		} else {
			settled, last = 0, n
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := h.count(); n < 1 || n > popupBacklog+1 {
		t.Errorf("the shell was offered %d popups out of %d arrivals, want between 1 and %d: the backlog is the bound",
			n, flood, popupBacklog+1)
	}
}

// Reaching a popup is the deliberate key, and the answer says whether the
// keyboard actually went anywhere. With no shell, or with nothing on the screen,
// it did not - and the caller has a sentence for that, because a key that
// silently does nothing is the failure this whole surface is arranged around.
func TestReachingAPopupSaysWhetherAnythingTookTheKeyboard(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	defer s.Close()

	var r Reach
	call(t, s, Request{Method: "attn.reach"}, &r)
	if r.Reached {
		t.Error("nothing was listening and the key says the keyboard moved")
	}

	rec := &recorder{}
	s.listen(&sink{w: rec})
	// The shell's side: read the token out of the event and send it back, which
	// is what AttnPopup.qml does once it has the keyboard.
	go func() {
		for i := 0; i < 2000; i++ {
			if got := eventsOf(rec, EventAttnReach); len(got) > 0 && got[0].Token != "" {
				s.acknowledge(got[0].Token)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	call(t, s, Request{Method: "attn.reach"}, &r)
	if !r.Reached {
		t.Fatal("a surface took the keyboard and the key says nothing happened")
	}
	got := eventsOf(rec, EventAttnReach)
	if len(got) == 0 || got[0].Token == "" {
		t.Fatalf("reach = %+v, want an event carrying a token to acknowledge", got)
	}
	if got[0].Output != "DP-1" {
		t.Errorf("reach output = %q, want the screen being looked at", got[0].Output)
	}
}

// An empty mode is a mode to replay, not one to ask for. It reads as work when
// the journal has never been told, and the same reading was the CLI's check -
// so `zde attn "$MODE"` with the variable unset turned the notifications back
// on and printed "work" as though that had been asked for. Every other way to
// get this wrong is refused, and this is the one where the accident is loud.
func TestSettingTheEmptyModeIsRefused(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	if err := jrn.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}
	resp := s.Dispatch(Request{Method: "attn.mode", Args: []string{""}})
	if resp.Error == "" {
		t.Fatal("an empty mode was accepted")
	}
	if got := jrn.State().Mode; got != "quiet" {
		t.Errorf("the mode is %q: an empty argument changed it", got)
	}
}
