package zded

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/zinc"
)

// deskThatDeclares is a server on a desk nobody is standing on, whose manifest
// names these apps. Entering it is what these tests do.
func deskThatDeclares(t *testing.T, apps ...string) (*Server, *fakeCompositor) {
	t.Helper()
	dir := t.TempDir()
	yaml := "name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n"
	for _, app := range apps {
		yaml += "  - { app: " + app + ", instance: vshop }\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "vshop.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.db", output: "DP-1"}
	// With a journal: an arrival is recorded against it, and a notification
	// nothing can keep is not a notification (see Arrived).
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	return New("test", jrn, f, manifest.Dir(dir)), f
}

// arrivals waits for what the switch had to say, since the launches happen on
// the goroutine behind it. Everything the history has, newest first.
func arrivals(t *testing.T, s *Server, want int) []attn.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := s.history.Recent()
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d notifications and %d arrived: %+v", want, len(got), got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The failure this exists to stop being silent: a person switches to a desk,
// gets fewer windows than the manifest declares, and the only record of which
// one and why was a line in a daemon's log.
func TestADeskThatCannotStartAnAppSaysWhichOneAndWhy(t *testing.T) {
	s, _ := deskThatDeclares(t, "nvim")
	s.launch = func(address string) error {
		return fmt.Errorf("zcr is not on PATH, so %q cannot be started (programs.zinc.enable)", address)
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	got := arrivals(t, s, 1)[0]
	if !strings.Contains(got.Text, "vshop") || !strings.Contains(got.Text, "nvim@vshop") {
		t.Errorf("notification says %q, want the desk and the app that did not start", got.Text)
	}
	if !strings.Contains(got.Body, "zcr is not on PATH") {
		t.Errorf("notification body is %q, want what the runner said about it", got.Body)
	}
	// Not urgent: urgency is what crosses focus and quiet modes, and a missing
	// window is not worth breaking somebody's concentration for.
	if got.Urgent {
		t.Error("a desk that came up short interrupted a focus mode")
	}
	// And it is the desktop that said it. Two things say so and they are not the
	// same thing: the name, which is the word the reservation keeps an app off
	// (internal/attn, SelfFrom and claim), and the mark, which is what every
	// surface draws its badge from and what no arrival off the bus can set. The
	// whole message is "the thing that tried to start your apps is telling you it
	// could not", and a name that only reads like that word is a message anybody
	// can send (internal/attn, Notification.Self).
	if got.From != attn.SelfFrom {
		t.Errorf("sender is %q, want the reserved name nothing on the bus can take", got.From)
	}
	if !got.Self {
		t.Error("the desktop's own notification is unbadged, so every surface draws it as a claim - " +
			"and a claim is what a lookalike name sends")
	}
}

// The noise bound. A desk that declares eight apps and can run none of them is
// every desk on a machine with no layer 2, and eight arrivals for one keypress
// would make the whole mechanism something to turn off.
func TestADeskThatCanStartNoneOfItsAppsIsOneNotification(t *testing.T) {
	var apps []string
	for i := range 8 {
		apps = append(apps, fmt.Sprintf("app-%d", i))
	}
	s, _ := deskThatDeclares(t, apps...)
	s.launch = func(address string) error { return errors.New("no such app") }

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	got := arrivals(t, s, 1)
	// Long enough for a second one to have shown up if the count were per app.
	time.Sleep(200 * time.Millisecond)
	if got = s.history.Recent(); len(got) != 1 {
		t.Fatalf("eight apps that would not start produced %d notifications: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Text, "8 apps") {
		t.Errorf("notification says %q, want the count", got[0].Text)
	}
	// And the body names the first few and counts the rest, rather than being a
	// wall of eight complaints or a number with nothing to act on.
	lines := strings.Split(got[0].Body, "\n")
	if len(lines) != launchesNamed+1 {
		t.Fatalf("body is %d lines, want %d named and one counting the rest:\n%s", len(lines), launchesNamed, got[0].Body)
	}
	if !strings.Contains(lines[0], "app-0@vshop") || !strings.Contains(lines[len(lines)-1], "4 more") {
		t.Errorf("body is\n%s\nwant the first few named and the rest counted", got[0].Body)
	}
}

// One failure in four is the case somebody actually meets: three windows
// arrived and one did not, and the notification is about the one.
func TestADeskSaysNothingAboutTheAppsThatStarted(t *testing.T) {
	s, _ := deskThatDeclares(t, "browser", "nvim", "term")
	s.launch = func(address string) error {
		if address == "nvim@vshop" {
			return errors.New("no app \"nvim\" defined")
		}
		return nil
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	got := arrivals(t, s, 1)[0]
	if strings.Contains(got.Body, "browser") || strings.Contains(got.Body, "term") {
		t.Errorf("notification mentions an app that started: %q", got.Body)
	}
	if !strings.Contains(got.Text, "nvim@vshop") {
		t.Errorf("notification says %q, want the one that did not start", got.Text)
	}
}

// The switch back, which is the case this meets on a healthy machine all day:
// zinc refuses a second launch of an app that is already up (internal/zinc,
// ErrAlreadyRunning), and counting that refusal as a failure made pressing the
// same key twice say "3 apps did not start" on a machine where everything
// started. A notification that cries wolf is one people turn off, and it takes
// the true ones with it.
func TestADeskThatIsAlreadyUpSaysNothingOnTheWayBackToIt(t *testing.T) {
	s, _ := deskThatDeclares(t, "browser", "nvim", "term")
	tried := make(chan string, 8)
	s.launch = func(address string) error {
		tried <- address
		// What zinc hands back, wrapped as Run wraps it - the caller's half of
		// the contract is errors.Is and nothing else.
		return fmt.Errorf("%s run %s: %w", zinc.Runner, address, zinc.ErrAlreadyRunning)
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	for range 3 {
		select {
		case <-tried:
		case <-time.After(2 * time.Second):
			t.Fatal("the desk declares three apps and did not try to start them")
		}
	}
	// Long enough for the notification to have been posted if the refusals had
	// been counted: it is one call after the last launch returns.
	time.Sleep(200 * time.Millisecond)
	if got := s.history.Recent(); len(got) != 0 {
		t.Errorf("a desk whose apps were already running said %+v", got)
	}
	if got := s.jrn.Waiting(); len(got) != 0 {
		t.Errorf("a desk whose apps were already running left %+v on the queue", got)
	}
}

// And the refusal is told from a failure one app at a time, not one switch at a
// time: a desk that has one app up and one that will not start is a desk with
// one thing to say.
func TestOnlyTheAppsThatReallyDidNotStartAreNamed(t *testing.T) {
	s, _ := deskThatDeclares(t, "browser", "nvim")
	s.launch = func(address string) error {
		if address == "browser@vshop" {
			return fmt.Errorf("%s run %s: %w", zinc.Runner, address, zinc.ErrAlreadyRunning)
		}
		return errors.New("no app \"nvim\" defined")
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	got := arrivals(t, s, 1)[0]
	// One failure names itself, so a count here would be the refusal counted.
	if !strings.Contains(got.Text, "nvim@vshop") || strings.Contains(got.Text, "2 apps") {
		t.Errorf("notification says %q, want the one app that did not start", got.Text)
	}
	if strings.Contains(got.Body, "browser") {
		t.Errorf("notification body is %q, and the browser was running fine", got.Body)
	}
}

// A desk that came up whole has nothing to say, and saying it anyway would make
// every switch cost a queue item.
func TestADeskWhoseAppsAllStartNotifiesNobody(t *testing.T) {
	s, _ := deskThatDeclares(t, "browser", "nvim")
	started := make(chan string, 4)
	s.launch = func(address string) error {
		started <- address
		return nil
	}

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("the desk declares two apps and did not start them")
		}
	}
	if got := s.history.Recent(); len(got) != 0 {
		t.Errorf("a desk that started everything it declares said %+v", got)
	}
}

// What a desk could not start waits in the queue, which is the half of "loud"
// that is for the person who was not looking at the screen when the desk came
// up short. `zde queue` is where they find it (docs/verify.md, section 5).
func TestWhatADeskCouldNotStartWaitsOnTheQueue(t *testing.T) {
	s, _ := deskThatDeclares(t, "nvim")
	s.launch = func(string) error { return errors.New("no app \"nvim\" defined") }

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	rec := arrivals(t, s, 1)[0]
	if !rec.Queued {
		t.Error("the record says the mode kept it off the queue, in the mode that keeps nothing off it")
	}
	waiting := s.jrn.Waiting()
	if len(waiting) != 1 {
		t.Fatalf("the queue has %d items for one desk that came up short: %+v", len(waiting), waiting)
	}
	if waiting[0].Text != rec.Text {
		t.Errorf("the queue says %q and the centre says %q", waiting[0].Text, rec.Text)
	}
	if waiting[0].From != attn.SelfFrom {
		t.Errorf("queued from %q, want the desktop saying it is the sender", waiting[0].From)
	}
	// And the badge goes on the queue with it, or `zde queue` is the one surface
	// where the desktop's row and an app drawing itself like the desktop read
	// the same (internal/journal, Item.Self).
	if !waiting[0].Self {
		t.Error("the queued item is unbadged, so `zde queue` cannot say who wrote it")
	}
	// The reason is asked of the record and not of the queue item, because the
	// journal stopped carrying bodies (internal/journal, Item): a queued line is
	// fsynced and kept for ever, and what the runner said is thousands of
	// characters nothing ever read back out of it. So the queue holds the
	// summary and the sender, and the message under Mod+n is the record's.
	if !strings.Contains(rec.Body, "no app") {
		t.Errorf("the record carries %q, want what the runner said about it", rec.Body)
	}
}

// And a quiet session keeps it out of the queue, which is the whole of what a
// mode does (internal/attn, Mode: display policy, never data policy).
//
// Deliberately, and it is worth writing down which way this was decided.
// A desk can put somebody in quiet without their pressing anything, and on a
// machine with no layer 2 every desk fails to start every app it declares
// (docs/delivery.md) - so a launch failure that queued itself regardless of the
// mode would be the one notification quiet cannot silence, on exactly the
// machine this feature is for, and quiet would become a mode nobody can use.
// Urgency would not have answered it either: quiet queues nothing, urgent
// included, so marking this urgent would buy focus mode's interruption and not
// this. What the mode must not do is lose it, and it does not: the centre has
// it, marked as one that was kept out of the queue rather than one that never
// happened.
func TestAQuietSessionKeepsWhatADeskCouldNotStartOutOfTheQueueAndNowhereElse(t *testing.T) {
	s, _ := deskThatDeclares(t, "nvim")
	if resp := s.setMode(string(attn.Quiet)); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	s.launch = func(string) error { return errors.New("no app \"nvim\" defined") }

	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	rec := arrivals(t, s, 1)[0]
	if rec.Queued {
		t.Error("quiet mode put a launch failure on the queue")
	}
	if got := s.jrn.Waiting(); len(got) != 0 {
		t.Errorf("quiet mode left %+v waiting", got)
	}
	// And the centre has all of it, which is what makes the mode display
	// policy rather than a decision about what happened.
	if !strings.Contains(rec.Text, "nvim@vshop") || !strings.Contains(rec.Body, "no app") {
		t.Errorf("the centre kept %q / %q, want the app that did not start and why", rec.Text, rec.Body)
	}
}

// The notification path must never be able to cost somebody the switch. Without
// a journal nothing can be kept, which is the way this fails that is not made
// up: the daemon still switches the desk and still starts what it can.
//
// The notification failing is the half this could not prove before. Dispatch
// answers the switch and the launches run on a goroutine behind it, so
// resp.Error is structurally incapable of carrying a notification failure -
// which made the assertion on it true of a daemon that never tried to notify
// anybody at all. The daemon's log is where that failure is written down, so
// that is where the test reads it.
func TestASwitchSurvivesANotificationThatCannotBeKept(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "vshop.yaml"), []byte(
		"name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n"+
			"  - { app: nvim, instance: vshop }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeCompositor{m: twoDesks(), focused: "haven.DP-1.db", output: "DP-1"}
	s := New("test", nil, f, manifest.Dir(dir)) // no journal: Arrived refuses
	logged := watchTheLog(t)
	tried := make(chan string, 1)
	s.launch = func(address string) error {
		tried <- address
		return errors.New("no such app")
	}

	resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}})
	if resp.Error != "" {
		t.Fatalf("a launch that could not be reported took the switch with it: %s", resp.Error)
	}
	// The switch happened, rather than merely not failing: the workspace it
	// moved to is in the answer, and the compositor was told to go there.
	var focused []string
	if err := json.Unmarshal(resp.Ok, &focused); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(focused, []string{"vshop.DP-1.code"}) {
		t.Errorf("the switch answered %v, want the workspace it moved to", focused)
	}
	if got := f.focusCalls(); !slices.Equal(got, []string{"vshop.DP-1.code"}) {
		t.Errorf("the compositor was asked for %v", got)
	}
	select {
	case <-tried:
	case <-time.After(2 * time.Second):
		t.Fatal("the switch never tried to start what the desk declares")
	}
	// And the notification really could not be kept, which is the thing this
	// test is named after.
	waitForLog(t, logged, "nor did the notification about it")
}

// zde's own notifications are bounded like anything that arrives on the bus,
// and this is the path that has to use the door: attn.Local clamps what a
// caller hands it, and a Notification filled in by hand beside it is the one
// way into the history with nothing clamping it at all (internal/attn, Local).
//
// The bounds are not hypothetical on this path even though a manifest cannot
// reach them today: an address is bounded at 64 bytes a part by the manifest
// and a reason at reasonMax here, so the fixtures are longer than a manifest
// can currently produce on purpose. A test that fed it only what a manifest
// allows would pass on a launchesFailed that clamped nothing at all.
func TestWhatASwitchSaysAboutItsLaunchesIsBoundedLikeAnythingElse(t *testing.T) {
	posted := func(t *testing.T, failed []launchFailure) attn.Record {
		t.Helper()
		s, _ := deskThatDeclares(t, "nvim")
		s.launchesFailed("vshop", failed) // the call the goroutine behind a switch makes
		got := s.history.Recent()
		if len(got) != 1 {
			t.Fatalf("%d notifications for one switch: %+v", len(got), got)
		}
		return got[0]
	}
	// One failure names itself in the summary, so that is where an address
	// nothing clamped would arrive whole.
	t.Run("the summary", func(t *testing.T) {
		failed := []launchFailure{{Address: strings.Repeat("a", 900), Err: errors.New("no app defined")}}
		raw := launchSummary("vshop", failed)
		want := attn.Local(raw, launchBody(failed))
		if want.Text == raw {
			t.Fatal("the fixture never reaches the summary's bound, so this would prove nothing")
		}
		if got := posted(t, failed); got.Text != want.Text {
			t.Errorf("the history kept %d characters of summary, want the %d a notification is clamped to",
				len([]rune(got.Text)), len([]rune(want.Text)))
		}
	})
	// Past launchesNamed, so the body is four lines like that and a count.
	t.Run("the body", func(t *testing.T) {
		var failed []launchFailure
		for i := range 6 {
			failed = append(failed, launchFailure{
				Address: fmt.Sprintf("%d%s", i, strings.Repeat("a", 900)),
				Err:     errors.New(strings.Repeat("e", 9000)),
			})
		}
		raw := launchBody(failed)
		want := attn.Local(launchSummary("vshop", failed), raw)
		if want.Body == raw {
			t.Fatal("the fixture never reaches the body's bound, so this would prove nothing")
		}
		got := posted(t, failed)
		if got.Body != want.Body {
			t.Errorf("the history kept %d characters of body, want the %d a notification is clamped to",
				len([]rune(got.Body)), len([]rune(want.Body)))
		}
		if got.From != attn.SelfFrom {
			t.Errorf("from = %q, want the desktop saying it is the sender", got.From)
		}
	})
}

// logSpy collects what the daemon writes to its log.
//
// Locked, because the writer is the goroutine behind a switch and the reader is
// the test: a plain buffer here is a data race that only shows up under -race
// on somebody else's machine.
type logSpy struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logSpy) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logSpy) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// watchTheLog points the daemon's log at one for the length of a test, and puts
// it back afterwards.
func watchTheLog(t *testing.T) *logSpy {
	t.Helper()
	spy := &logSpy{}
	log.SetOutput(spy)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return spy
}

// waitForLog waits for a line the daemon writes from the goroutine behind a
// switch, which is after the answer the test already has.
func waitForLog(t *testing.T, spy *logSpy, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := spy.text(); strings.Contains(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log never said %q, and it is where this failure is written down:\n%s", want, spy.text())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// zcr hands back everything the runner printed, and a failed image build is a
// screen of it. One line per failure, bounded, because the alternative is a
// notification body made of one app's stack trace.
func TestALaunchFailureIsCutToALineInTheNotification(t *testing.T) {
	long := strings.Repeat("x", reasonMax*2)
	body := launchBody([]launchFailure{
		{Address: "nvim@vshop", Err: errors.New("Error: short line\nError: preparing container\nmore\nand more")},
		{Address: "browser@vshop", Err: errors.New(long)},
	})
	lines := strings.Split(body, "\n")
	if len(lines) != 2 {
		t.Fatalf("body is %d lines for two failures:\n%s", len(lines), body)
	}
	if !strings.HasPrefix(lines[0], "nvim@vshop: Error: short line") || !strings.HasSuffix(lines[0], "...") {
		t.Errorf("line = %q, want the first line of the complaint and a mark that there was more", lines[0])
	}
	if n := len([]rune(lines[1])); n > len("browser@vshop: ")+reasonMax+len(" ...") {
		t.Errorf("one failure took %d characters of the body", n)
	}
}
