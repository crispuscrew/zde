package zded

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/crispuscrew/zde/internal/attn"
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
