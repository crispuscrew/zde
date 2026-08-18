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
			// A sender each, so that all fifty reach the queue and the count
			// below still means what it meant. One name fifty times is refused
			// after thirty now, on purpose and by a different mechanism
			// (internal/journal, PerSenderMax) - and a test about a torn read
			// should not be the one that trips over it.
			from := "ci-" + strconv.Itoa(i)
			if _, err := s.Arrived(attn.Notification{From: from, Text: "the build failed"}); err != nil {
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
		}
	}
}

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

	// One sender with three records, and every other name holding more, so the
	// ring of three is the cheapest one to lose.
	mine := []uint64{}
	for i := 0; i < 3; i++ {
		id, err := s.Arrived(attn.Notification{From: "the cheapest", Text: "one of three"})
		if err != nil {
			t.Fatal(err)
		}
		mine = append(mine, id)
	}
	for n := 2; n <= attn.SendersMax; n++ {
		for k := 0; k < 5; k++ {
			if _, err := s.Arrived(attn.Notification{From: "app " + strconv.Itoa(n), Text: "hello"}); err != nil {
				t.Fatal(err)
			}
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

// A second manifest naming a desk that is already declared is not how the
// first one's `private: true` disappears.
//
// The manifests are keyed on the name inside the file and the first in
// directory order wins, with the loser reported as a problem
// (internal/manifest, LoadDir). So a copy of a private desk's manifest that
// leaves the flag out decides the question by alphabetical order. Both files
// are refused instead: the one that lost could have been the private
// declaration, and nothing here can tell which.
func TestADuplicateManifestDoesNotPutAPrivateDesksCardOnTheScreen(t *testing.T) {
	s, rec := popupServer(t, "clinic.DP-1.mail", map[string]string{
		// Sorts first, so it is the one LoadDir keeps: this is the copy
		// winning, which is the case that has to be refused.
		"aa-clinic": "name: clinic\nmonitors: { DP-1: { workspaces: [mail] } }\n",
		"clinic":    "name: clinic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n",
		"work":      "name: work\nmonitors: { DP-1: { workspaces: [code] } }\n",
	})
	if _, err := s.Arrived(attn.Notification{From: "clinic", Text: "your results are in"}); err != nil {
		t.Fatal(err)
	}
	// Then take the copy away and send one that must draw. It proves the first
	// was withheld rather than the popup path being broken - the pump writes in
	// the order things arrived, so a card for the second is a card the first
	// never got - and taking the copy away is what makes the machine ordinary
	// again, since while it is there nothing on it can be placed with
	// confidence and nothing is drawn at all.
	if err := os.Remove(filepath.Join(string(s.desks.(manifest.Dir)), "aa-clinic.yaml")); err != nil {
		t.Fatal(err)
	}
	s.niri.(*fakeCompositor).focused = "work.DP-1.code"
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	got := waitForEvents(t, rec, EventAttnPopup, 1)
	if len(got) != 1 {
		t.Fatalf("%d cards were drawn, want only the one from the open desk", len(got))
	}
	if drawn := got[0].Notifications[0]; drawn.Text != "the build failed" {
		t.Errorf("the card said %q: a duplicate manifest that says nothing about privacy was enough to put a private desk's notification on the screen", drawn.Text)
	}
}

// The regulars are reachable from every desk and cannot be declared by any
// manifest (internal/manifest, check), so they are the one name that is
// certainly not the private desk. Answering them with the doubt owed to an
// undeclared desk cost every card on them - most of a day, for somebody who
// works out of the regulars - on any machine that declares one private desk
// anywhere.
func TestTheRegularsStillDrawCardsWhereADeskIsPrivate(t *testing.T) {
	s, rec := popupServer(t, "regulars.DP-1.2", map[string]string{
		"work":   "name: work\nmonitors: { DP-1: { workspaces: [code] } }\n",
		"clinic": "name: clinic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n",
	})
	if _, err := s.Arrived(attn.Notification{From: "ci", Text: "the build failed"}); err != nil {
		t.Fatal(err)
	}
	if got := waitForEvents(t, rec, EventAttnPopup, 1); len(got) != 1 {
		t.Errorf("%d cards were drawn for an arrival on the regulars, and no manifest can declare the regulars private", len(got))
	}
	if st := s.status(); st.Unplaced != 0 {
		t.Errorf("status counts %d unplaced arrivals, and the regulars are a desk zde named itself", st.Unplaced)
	}
}

// What the fail-closed answer costs is counted and said. Otherwise it is a
// session that stops drawing cards on a machine where nothing is wrong with the
// manifests and nothing appears in any log: the daemon refusing to show things
// looks exactly like the popup not working.
func TestACardNobodyCanPlaceIsCountedWhereADeskIsPrivate(t *testing.T) {
	guarded, _ := popupServer(t, "", map[string]string{
		"work":   "name: work\nmonitors: { DP-1: { workspaces: [code] } }\n",
		"clinic": "name: clinic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n",
	})
	guarded.niri.(*fakeCompositor).err = errors.New("no compositor")
	if _, err := guarded.Arrived(attn.Notification{From: "app", Text: "could be anything"}); err != nil {
		t.Fatal(err)
	}
	if st := guarded.status(); st.Unplaced != 1 {
		t.Errorf("status counts %d unplaced arrivals, want the one it drew no card for", st.Unplaced)
	}

	// And on the ordinary machine there is nothing to count: the same arrival
	// draws its card, so nothing was refused and saying otherwise would send
	// somebody looking for a problem they do not have.
	open, _ := popupServer(t, "", map[string]string{
		"work": "name: work\nmonitors: { DP-1: { workspaces: [code] } }\n",
	})
	open.niri.(*fakeCompositor).err = errors.New("no compositor")
	if _, err := open.Arrived(attn.Notification{From: "app", Text: "could be anything"}); err != nil {
		t.Fatal(err)
	}
	if st := open.status(); st.Unplaced != 0 {
		t.Errorf("status counts %d unplaced arrivals on a machine where no desk is private", st.Unplaced)
	}
}

// stuckListener takes the first line and does not finish taking it until the
// test says so. A shell mid-frame, or one that has stopped drawing and not
// closed its socket.
type stuckListener struct {
	mu      sync.Mutex
	lines   int
	release chan struct{}
}

func (h *stuckListener) Write(p []byte) (int, error) {
	h.mu.Lock()
	first := h.lines == 0
	h.lines++
	h.mu.Unlock()
	if first {
		<-h.release
	}
	return len(p), nil
}

func (h *stuckListener) count() int {
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
//
// The flood is spread over several senders on purpose. What this test is about
// is the popup path costing a record, and a history that bounds what any one
// name may hold would otherwise answer for it: 64 arrivals under one name is a
// measurement of that bound and not of this one, and the test would fail the
// day the bound changed while nothing was wrong with the path it names.
func TestAFloodOfArrivalsNeverWaitsForTheShellAndLosesNoRecord(t *testing.T) {
	s, _, _ := queueTestServer(t, "vshop.DP-1.code")
	defer s.Close()
	h := &stuckListener{release: make(chan struct{})}
	s.listen(&sink{w: h})

	// Eight each from eight senders: enough of both to be a flood, and few
	// enough of either that nothing the history bounds is being tested here.
	const senders = 8
	const flood = popupBacklog * 4
	for i := 0; i < flood; i++ {
		from := "bot-" + strconv.Itoa(i%senders)
		if _, err := s.Arrived(attn.Notification{From: from, Text: "build " + strconv.Itoa(i)}); err != nil {
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
