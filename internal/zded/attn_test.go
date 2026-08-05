package zded

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/crispuscrew/zde/internal/attn"
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
	for i := 0; i < attn.PerSenderMax+2; i++ {
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
	if seen := s.history.Recent(); len(seen) != attn.PerSenderMax {
		t.Errorf("history holds %d, want the bound", len(seen))
	}
}

// A notification arriving while the session is switching desks is filed against
// a desk, and the two of them writing at once is not a race.
//
// Filing one is a read that writes. Arrived asks where we are, and whereWeAre
// goes through deskOf, which records the desk when the journal is behind the
// compositor - so a notification is a journal writer, on whatever goroutine the
// bus handed it to, at the same time as a switch is writing where it went. That
// is worth a test rather than a claim: anything that posts a notification from a
// background goroutine is betting on this, and the bet is invisible from the
// code that makes it.
//
// The fake compositor stays focused where it was while the switch records the
// desk it went to, which is the interleaving on purpose: it is niri lagging a
// keypress, and it makes the two writers disagree on every pass rather than on
// the rare one. What must hold either way is that the journal is still readable
// afterwards, that every arrival is filed against a desk that exists, and that
// nothing was lost.
//
// Break the journal's lock - drop the mutex out of record or State - and
// `go test -race` says so from here.
func TestANotificationArrivingWhileTheDeskChangesIsNotARace(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")

	arriving := make(chan error, 1)
	start := make(chan struct{})
	go func() {
		<-start
		for i := 0; i < 50; i++ {
			if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
				arriving <- err
				return
			}
		}
		arriving <- nil
	}()
	close(start)
	for i := 0; i < 10; i++ {
		for _, target := range []string{"haven", "vshop"} {
			if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{target}}); resp.Error != "" {
				t.Fatalf("desk.switch %s: %s", target, resp.Error)
			}
		}
	}
	if err := <-arriving; err != nil {
		t.Fatalf("a notification was lost while the desk changed: %v", err)
	}

	state := jrn.State()
	// A desk that exists. The failure this rules out is a torn read handing the
	// arrival a name that was never a desk, which would file it somewhere
	// nothing can look for it.
	if state.OnDesk != "vshop" && state.OnDesk != "haven" {
		t.Errorf("the journal says we are on %q, and there are two desks", state.OnDesk)
	}
	if len(state.Queue) != 50 {
		t.Fatalf("%d notifications on the queue, and 50 arrived", len(state.Queue))
	}
	for _, it := range state.Queue {
		if it.Desk != "vshop" && it.Desk != "haven" {
			t.Errorf("a notification is filed against %q, which is not a desk", it.Desk)
// One download, ninety-nine progress updates, one row.
//
// replaces_id is the sender saying this is the same notification with something
// new to say, so the record is written over rather than added beside. Appending
// was what let one app answer "what did I miss" for the whole session: a
// hundred rows of the same download, ninety-nine of them marked done, and
// everything else pushed off the end behind them.
func TestOneDownloadIsOneRowOfHistoryHoweverOftenItMoves(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	if _, err := s.Arrived(attn.Notification{From: "mail", Text: "Ilya: about the invoice"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.Arrived(attn.Notification{From: "curl", Text: "downloading 1%"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 100; i++ {
		// Closed first and then Arrived, which is the order the bus side does
		// it in (internal/attn, Notify).
		if err := s.Closed(id); err != nil {
			t.Fatal(err)
		}
		id, err = s.Arrived(attn.Notification{
			From:     "curl",
			Text:     "downloading " + strconv.Itoa(i) + "%",
			Replaces: id,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	seen := s.history.Recent()
	if len(seen) != 2 {
		t.Fatalf("the history holds %d rows after one download and ninety-nine updates of it", len(seen))
	}
	if seen[0].ID != id || seen[0].Text != "downloading 100%" {
		t.Errorf("the newest row is %+v, want the download as it last said it was", seen[0])
	}
	if seen[0].Dismissed {
		t.Error("the row says the download is done, and the update that replaced it says it is not")
	}
	if seen[1].Text != "Ilya: about the invoice" {
		t.Errorf("the row behind it is %+v, want the one the download never touched", seen[1])
	}
	// And the queue is what it always was: one item for the download, because
	// each update finishes the last (internal/attn, Notify). The mail is the
	// other one.
	q := s.jrn.State().Queue
	downloads := 0
	for _, it := range q {
		if it.From == "curl" {
			downloads++
		}
	}
	if len(q) != 2 || downloads != 1 {
		t.Errorf("queue = %+v, want the mail and one download: a download is one thing waiting", q)
	}
}

// A sender leaves the history all at once - its ring goes whole when its name
// is one too many (internal/attn, SendersMax) - so one arrival can make a
// ring's worth of notifications unaddressable at a stroke.
//
// Every one of those ids has to reach the bus side. A daemon that forgot only
// the first would keep the rest of that sender's names in the one table with
// no bound of its own, for the life of the session, which is the leak the
// answer exists to stop.
func TestEveryRecordOfAnEvictedSenderIsForgotten(t *testing.T) {
	s, jrn, _ := queueTestServer(t, "vshop.DP-1.code")
	if err := jrn.SetMode("quiet"); err != nil {
		t.Fatal(err)
	}
	forgotten := []uint64{}
	s.Watching(tellTale{ids: &[]uint64{}, forgotten: &forgotten})

	// One sender with three records, and then every other name newer than it.
	mine := []uint64{}
	for i := 0; i < 3; i++ {
		id, err := s.Arrived(attn.Notification{From: "the quiet one", Text: "one of three"})
		if err != nil {
			t.Fatal(err)
		}
		mine = append(mine, id)
	}
	for n := 2; n <= attn.SendersMax; n++ {
		if _, err := s.Arrived(attn.Notification{From: "app " + strconv.Itoa(n), Text: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(forgotten) != 0 {
		t.Fatalf("forgot %v before anything was evicted", forgotten)
	}
	// The name too many.
	if _, err := s.Arrived(attn.Notification{From: "one name too many", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(forgotten) != len(mine) {
		t.Fatalf("forgot %v, want every id of the ring that went: %v", forgotten, mine)
	}
	for i, id := range mine {
		if forgotten[i] != id {
			t.Errorf("forgot %v, want %v - the whole ring, oldest first", forgotten, mine)
			break
		}
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
	for i := 0; i < attn.PerSenderMax; i++ {
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

// The desk's own attn policy (docs/model.md, section 5). Below here, a desk is
// something that can lend the session a mode.

// openJournal is a journal of its own, closed when the test ends. The desk
// policy needs one: the mode and the desk that lent it are both written down,
// and a server without a journal has nowhere to put either.
func openJournal(t *testing.T, path string) *journal.Journal {
	t.Helper()
	jrn, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	return jrn
}

// deskPolicyServer is a session whose desks are declared by manifests. The
// desks are the two the compositor fake has workspaces for, so a switch to
// either has somewhere to land, and the session starts standing on haven.
func deskPolicyServer(t *testing.T, jrn *journal.Journal, manifests map[string]string) (*Server, *fakeCompositor) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range manifests {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.db", output: "DP-1"}
	return New("test", jrn, f, manifest.Dir(dir)), f
}

// switchOnto enters a desk and keeps the fake compositor honest: it moves
// nothing by itself, and where the session is standing is read back off it on
// the next switch.
func switchOnto(t *testing.T, s *Server, f *fakeCompositor, name string) {
	t.Helper()
	resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{name}})
	if resp.Error != "" {
		t.Fatalf("desk.switch %s: %s", name, resp.Error)
	}
	var focused []string
	if err := json.Unmarshal(resp.Ok, &focused); err != nil {
		t.Fatal(err)
	}
	f.focused = focused[0]
}

// modeNow is the mode as the bar reads it: through the daemon, not out of the
// journal, because what the session is in is what zded answers.
func modeNow(t *testing.T, s *Server) string {
	t.Helper()
	resp := s.Dispatch(Request{Method: "attn.mode"})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var a Attn
	if err := json.Unmarshal(resp.Ok, &a); err != nil {
		t.Fatal(err)
	}
	return a.Mode
}

// chooseMode is a person setting the mode, through the verb `zde attn` calls -
// which is where Mod+q lands too, one toggle further along (see toggleQuiet).
func chooseMode(t *testing.T, s *Server, mode string) {
	t.Helper()
	if resp := s.Dispatch(Request{Method: "attn.mode", Args: []string{mode}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
}

const (
	vshopInFocus = "name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\npolicies: { attn: focus }\n"
	havenPlain   = "name: haven\nmonitors: { DP-1: { workspaces: [db] } }\n"
)

// A desk that declares a mode puts the session in it. This is the whole of
// `policies.attn`, and until now the field was read by nobody: a desk for
// concentrating declared focus and the session stayed in work.
func TestEnteringADeskAppliesTheModeItDeclares(t *testing.T) {
	jrn := openJournal(t, filepath.Join(t.TempDir(), "j.jsonl"))
	s, f := deskPolicyServer(t, jrn, map[string]string{"vshop": vshopInFocus, "haven": havenPlain})

	switchOnto(t, s, f, "vshop")
	if got := modeNow(t, s); got != "focus" {
		t.Errorf("mode on a desk declaring focus = %q, want focus", got)
	}
}

// And leaving gives back the mode that desk displaced, rather than the default:
// the desk borrowed it. A restore that always landed on work would be a desk
// policy that turns the notifications on for you, hours after you silenced them
// for a call, on the way to a terminal.
func TestLeavingADeskGivesBackTheModeItBorrowed(t *testing.T) {
	jrn := openJournal(t, filepath.Join(t.TempDir(), "j.jsonl"))
	s, f := deskPolicyServer(t, jrn, map[string]string{"vshop": vshopInFocus, "haven": havenPlain})

	chooseMode(t, s, "quiet") // silence, chosen on a desk that declares nothing
	switchOnto(t, s, f, "vshop")
	if got := modeNow(t, s); got != "focus" {
		t.Fatalf("mode on vshop = %q, want the focus it declares", got)
	}
	switchOnto(t, s, f, "haven")
	if got := modeNow(t, s); got != "quiet" {
		t.Errorf("mode after leaving vshop = %q, want the quiet that was in force before it", got)
	}
}

// The loop this feature exists for, in one test: a desk lends a mode, a person
// overrides it while standing there, and entering the desk again is what takes
// it back. The override outliving the switch away is the deliberate half - a
// mode chosen by hand is the session's, so it follows you off the desk, and the
// desk claims the mode at the one moment somebody can see it happen.
func TestAModeSetByHandStandsUntilYouEnterThatDeskAgain(t *testing.T) {
	jrn := openJournal(t, filepath.Join(t.TempDir(), "j.jsonl"))
	s, f := deskPolicyServer(t, jrn, map[string]string{"vshop": vshopInFocus, "haven": havenPlain})

	switchOnto(t, s, f, "vshop")
	if got := modeNow(t, s); got != "focus" {
		t.Fatalf("mode on vshop = %q, want the focus it declares", got)
	}
	chooseMode(t, s, "quiet")
	if got := modeNow(t, s); got != "quiet" {
		t.Fatalf("mode after asking for quiet = %q: the desk overrode a person", got)
	}
	switchOnto(t, s, f, "haven")
	if got := modeNow(t, s); got != "quiet" {
		t.Errorf("mode after leaving = %q, want the quiet the person chose to come with them", got)
	}
	switchOnto(t, s, f, "vshop")
	if got := modeNow(t, s); got != "focus" {
		t.Errorf("mode back on vshop = %q, want the focus it declares", got)
	}
}

// A desk that declares nothing leaves the mode where it is. Empty is not work:
// most desks say nothing about attn, and a switch that quietly reset the mode
// would make every one of them a way to lose a silence you asked for.
func TestEnteringADeskThatDeclaresNoModeLeavesTheModeAlone(t *testing.T) {
	jrn := openJournal(t, filepath.Join(t.TempDir(), "j.jsonl"))
	// vshop has no manifest at all, which is the other way to declare nothing:
	// a desk that only ever got named into existence by adoption.
	s, f := deskPolicyServer(t, jrn, map[string]string{"haven": havenPlain})

	chooseMode(t, s, "quiet")
	switchOnto(t, s, f, "vshop")
	if got := modeNow(t, s); got != "quiet" {
		t.Errorf("mode on a desk with no manifest = %q, want the quiet that was in force", got)
	}
	switchOnto(t, s, f, "haven")
	if got := modeNow(t, s); got != "quiet" {
		t.Errorf("mode on a desk whose manifest declares no policy = %q, want the quiet still", got)
	}
}

// Standing still is not entering. Half the nav keys re-enter the desk you are
// on, so a policy applied on every switch would undo a mode set by hand a
// keypress after it was set - and the person would watch the bar change back
// with nothing to blame it on.
func TestReEnteringTheDeskYouAreStandingOnKeepsTheModeYouChose(t *testing.T) {
	jrn := openJournal(t, filepath.Join(t.TempDir(), "j.jsonl"))
	s, f := deskPolicyServer(t, jrn, map[string]string{"vshop": vshopInFocus, "haven": havenPlain})

	switchOnto(t, s, f, "vshop")
	chooseMode(t, s, "work")
	switchOnto(t, s, f, "vshop")
	if got := modeNow(t, s); got != "work" {
		t.Errorf("mode after re-entering the desk under you = %q, want the work you asked for", got)
	}
}

// A switch that failed partway is not a desk you are on, so it is not a desk
// whose mode you are in either. The mode belongs to the desk still on the
// screens, and the retry that fixes them is what applies it.
func TestASwitchThatFailsHalfwayLeavesTheModeAlone(t *testing.T) {
	jrn := openJournal(t, filepath.Join(t.TempDir(), "j.jsonl"))
	s, f := deskPolicyServer(t, jrn, map[string]string{"vshop": vshopInFocus, "haven": havenPlain})

	f.failOn = "vshop.DP-1.code"
	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error == "" {
		t.Fatal("the compositor refused to focus the workspace and the switch reported success")
	}
	if got := modeNow(t, s); got != "work" {
		t.Errorf("mode after a switch that never landed = %q, want the one belonging to the desk you can see", got)
	}

	f.failOn = ""
	switchOnto(t, s, f, "vshop")
	if got := modeNow(t, s); got != "focus" {
		t.Errorf("mode after the retry = %q, want the focus vshop declares", got)
	}
}

// A desk you left without switching still holds the mode it borrowed, and
// coming back to it must not write down its own mode as the thing to give back.
// You get here without doing anything strange: niri's own keys and a window
// jump both land you on another desk's workspace, and the next thing that asks
// records it - so the switch back is an entry to a desk that never let go.
func TestComingBackToADeskThatStillHoldsTheModeKeepsWhatItDisplaced(t *testing.T) {
	jrn := openJournal(t, filepath.Join(t.TempDir(), "j.jsonl"))
	s, f := deskPolicyServer(t, jrn, map[string]string{"vshop": vshopInFocus, "haven": havenPlain})

	switchOnto(t, s, f, "vshop")
	// Off the desk without a desk action: the focused workspace is haven's, and
	// anything that asks where we are writes that down (see deskOf).
	f.focused = "haven.DP-1.db"
	if resp := s.Dispatch(Request{Method: "queue.add", Args: []string{"reply to ilya"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if got := jrn.State().OnDesk; got != "haven" {
		t.Fatalf("OnDesk = %q, want the haven the compositor is showing", got)
	}

	switchOnto(t, s, f, "vshop")
	if got := modeNow(t, s); got != "focus" {
		t.Fatalf("mode back on vshop = %q, want the focus it declares", got)
	}
	switchOnto(t, s, f, "haven")
	if got := modeNow(t, s); got != "work" {
		t.Errorf("mode after leaving = %q, want the work vshop displaced: it kept lending its own mode to itself", got)
	}
}

// The first desk after login is the one you logged out on, and coming back to
// it is not entering it. The session returns in the mode it ended in - which is
// what the journal keeps a mode for - and the record of which desk lent that
// mode returns with it, so walking off that desk still gives it back. Nothing
// special-cases login, and this is the test that says so.
func TestTheFirstDeskAfterLoginKeepsTheModeTheSessionEndedIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	last := openJournal(t, path)
	// Last night: on vshop, in the focus vshop lent, over the quiet chosen
	// before that.
	s, f := deskPolicyServer(t, last, map[string]string{"vshop": vshopInFocus, "haven": havenPlain})
	chooseMode(t, s, "quiet")
	switchOnto(t, s, f, "vshop")
	if err := last.Close(); err != nil {
		t.Fatal(err)
	}

	// This morning: a new daemon over the same journal, and niri with nothing
	// named yet, so the first switch is onto the desk the journal remembers.
	today := openJournal(t, path)
	s, f = deskPolicyServer(t, today, map[string]string{"vshop": vshopInFocus, "haven": havenPlain})
	f.focused = ""
	switchOnto(t, s, f, "vshop")
	if got := modeNow(t, s); got != "focus" {
		t.Errorf("mode on the first desk after login = %q, want the focus the session ended in", got)
	}
	switchOnto(t, s, f, "haven")
	if got := modeNow(t, s); got != "quiet" {
		t.Errorf("mode after leaving that desk = %q, want the quiet from before it, remembered across the login", got)
	}
}
