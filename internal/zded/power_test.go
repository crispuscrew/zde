package zded

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/power"
)

// A logind that says what a test wants it to say and never ends the machine the
// test is running on.
type fakeLogind struct {
	mu       sync.Mutex
	state    power.State
	stateErr error
	// did is every verb it was asked to perform, which is the assertion for
	// half of these tests: what must not happen is a row that reports success
	// having asked logind for nothing, or one that asks for it without being
	// told to.
	did   []power.What
	doErr error
}

func (f *fakeLogind) State() (power.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.stateErr
}

func (f *fakeLogind) Do(w power.What) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.did = append(f.did, w)
	return f.doErr
}

func (f *fakeLogind) done() []power.What {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]power.What(nil), f.did...)
}

// withLogind gives a server a power side, the way the daemon gives itself one.
func withLogind(s *Server, m power.Manager, err error) {
	s.openPower = func() (power.Manager, error) { return m, err }
}

// powerServer is a daemon with three windows open, a logind that answers, and
// spawns that are recorded rather than run - so the lock row can be watched
// without a locker taking the screen of whoever is running the tests.
func powerServer(t *testing.T) (*Server, *fakeLogind, *spy) {
	t.Helper()
	withZdeOnPath(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := &fakeCompositor{
		m:       twoDesks(),
		focused: "vshop.DP-1.code",
		output:  "DP-1",
		windows: []Window{{ID: 1}, {ID: 2}, {ID: 3}},
	}
	s := New("test", nil, f, nil)
	sp := &spy{}
	s.spawn = sp.run
	l := &fakeLogind{}
	withLogind(s, l, nil)
	return s, l, sp
}

func menu(t *testing.T, s *Server) Power {
	t.Helper()
	resp := s.Dispatch(Request{Method: "system.power"})
	if resp.Error != "" {
		t.Fatalf("system.power: %s", resp.Error)
	}
	var p Power
	if err := json.Unmarshal(resp.Ok, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func choice(t *testing.T, p Power, name string) PowerChoice {
	t.Helper()
	for _, c := range p.Choices {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s row in the power menu", name)
	return PowerChoice{}
}

// The lock on this menu and the lock on Mod+Ctrl+semicolon have to be one
// thing. Which program locks this screen is a machine's own answer, out of the
// apps table `zde system lock` already resolves it with, and a menu that grew a
// second idea of it - swaylock by name, loginctl lock-session, a niri action -
// would be the row that keeps working on the developer's machine and locks
// nothing on somebody else's.
func TestTheLockRowRunsTheCommandTheLockKeyRuns(t *testing.T) {
	s, l, sp := powerServer(t)

	resp := s.Dispatch(Request{Method: "system.power", Args: []string{"lock"}})
	if resp.Error != "" {
		t.Fatalf("locking from the power menu: %s", resp.Error)
	}
	got := sp.all()
	if len(got) != 1 || strings.Join(got[0], " ") != "zde system lock" {
		t.Fatalf("the lock row spawned %v, want what Mod+Ctrl+semicolon spawns", got)
	}
	if asked := l.done(); len(asked) != 0 {
		t.Errorf("locking asked logind for %v, and locking a screen is not logind's", asked)
	}
}

// The confirmation has to say what is about to be lost, or it is an "are you
// sure" - and people learn to press through those. Every line is something true
// about this machine at the moment the menu opened: the windows that close, the
// arrivals that exist nowhere but in the daemon, the other person whose
// afternoon this ends.
func TestPoweringOffNamesWhatIsAboutToBeLost(t *testing.T) {
	s, l, _ := powerServer(t)
	l.state = power.State{Sessions: []power.Session{
		{ID: "1", User: "me", Class: "user", Mine: true},
		{ID: "3", User: "ann", Class: "user"},
	}}
	// Two the queue never got, because a mode kept them out of it: they are in
	// the notification center and nowhere else.
	s.history.Add(attn.Record{ID: 1, Text: "the build finished"})
	s.history.Add(attn.Record{ID: 2, Text: "ilya replied"})
	// And three that must not be counted with them. Two are in the queue, which
	// is in the journal and survives a log out, so telling somebody they are
	// about to lose them is telling them the opposite of the truth; the third
	// they have already finished with.
	s.history.Add(attn.Record{ID: 3, Text: "standup in five", Queued: true})
	s.history.Add(attn.Record{ID: 4, Text: "the download finished", Queued: true})
	s.history.Dismiss(4)
	s.history.Add(attn.Record{ID: 5, Text: "taken back by its app"})
	s.history.Dismiss(5)

	off := choice(t, menu(t, s), "poweroff")
	if !off.Confirm {
		t.Error("powering off happens without asking")
	}
	said := strings.Join(off.Costs, "\n")
	for _, want := range []string{"3 windows close", "2 arrivals", "ann is logged in"} {
		if !strings.Contains(said, want) {
			t.Errorf("what powering off costs is %q, and it has to say %q", said, want)
		}
	}
	// The number is the whole of this line's honesty, in both directions.
	for _, wrong := range []string{"5 arrivals", "4 arrivals", "3 arrivals"} {
		if strings.Contains(said, wrong) {
			t.Errorf("what powering off costs is %q, and %q counts what the queue already has",
				said, wrong)
		}
	}
	// And the row that costs nothing says nothing: a lock that asked twice
	// would be the question that teaches people to answer without reading.
	if lock := choice(t, menu(t, s), "lock"); lock.Confirm || len(lock.Costs) != 0 {
		t.Errorf("the lock row = %+v, and locking loses nothing", lock)
	}
}

// A power menu that reports success while nothing happened is worse than one
// that says it was refused. logind refuses on the wire - an inhibitor holding
// sleep, another person logged in, polkit wanting a password nothing here can
// ask for - and that has to come back as a refusal with the words in it.
func TestARefusedSuspendIsSaidAsRefusedRatherThanAsDone(t *testing.T) {
	s, l, _ := powerServer(t)
	l.doErr = errors.New("chromium is holding this off: Playing audio, and logind will not " +
		"take a suspend past it without an administrator's password")

	resp := s.Dispatch(Request{Method: "system.power", Args: []string{"suspend"}})
	if resp.Error == "" {
		t.Fatalf("a refused suspend answered %s", resp.Ok)
	}
	if len(resp.Ok) != 0 {
		t.Errorf("a refused suspend also answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "chromium") {
		t.Errorf("the refusal is %q, and it has to carry what logind said", resp.Error)
	}
	if asked := l.done(); len(asked) != 1 || asked[0] != power.Suspend {
		t.Errorf("logind was asked for %v, want one suspend", asked)
	}
}

// Suspend is the one row here that loses nothing, so it does not ask - until
// something is holding sleep, when the question is the only place a person
// finds out what is about to refuse it. If this regresses, either every suspend
// costs a second keypress for nothing, or the one that is about to be refused
// looks exactly like the ones that work.
func TestSuspendAsksTwiceOnlyWhenSomethingIsHoldingSleep(t *testing.T) {
	s, l, _ := powerServer(t)

	quiet := choice(t, menu(t, s), "suspend")
	if quiet.Confirm || len(quiet.Costs) != 0 {
		t.Errorf("suspend = %+v on a machine where nothing is holding sleep", quiet)
	}

	l.state = power.State{Blocks: []power.Block{
		{What: "sleep", Who: "chromium", Why: "Playing audio"},
	}}
	held := menu(t, s)
	blocked := choice(t, held, "suspend")
	if !blocked.Confirm {
		t.Error("something is holding sleep and the suspend row still does not ask")
	}
	if said := strings.Join(blocked.Costs, "\n"); !strings.Contains(said, "chromium") ||
		!strings.Contains(said, "Playing audio") {
		t.Errorf("suspend says %q, and it has to name what is holding it", said)
	}
	// And it is not repeated onto rows it does not hold: a sleep inhibitor
	// stops a suspend and has nothing to say about a reboot.
	if said := strings.Join(choice(t, held, "reboot").Costs, "\n"); strings.Contains(said, "chromium") {
		t.Errorf("the reboot row says %q about something that only holds sleep", said)
	}
}

// A machine with nothing to ask still gets the menu, with the lock working and
// the reason on the four rows that need logind. The alternative is a key that
// draws nothing, which is the silent key this whole surface exists to be the
// opposite of.
func TestAMachineWithNoLogindStillLocksAndSaysWhyTheRestWillNot(t *testing.T) {
	s, _, sp := powerServer(t)
	withLogind(s, nil, power.ErrNoLogind)

	p := menu(t, s)
	if len(p.Choices) != 5 {
		t.Fatalf("the menu has %d rows on a machine with no logind, want all five", len(p.Choices))
	}
	if lock := choice(t, p, "lock"); lock.Why != "" {
		t.Errorf("the lock row says %q, and locking needs no logind", lock.Why)
	}
	for _, name := range []string{"logout", "suspend", "reboot", "poweroff"} {
		if why := choice(t, p, name).Why; !strings.Contains(why, "logind") {
			t.Errorf("%s says %q about a machine with no logind", name, why)
		}
	}

	if resp := s.Dispatch(Request{Method: "system.power", Args: []string{"poweroff"}}); resp.Error == "" {
		t.Error("powering off answered as done on a machine with no logind to ask")
	}
	if resp := s.Dispatch(Request{Method: "system.power", Args: []string{"lock"}}); resp.Error != "" {
		t.Errorf("locking is refused for want of a logind: %s", resp.Error)
	}
	if got := sp.all(); len(got) != 1 {
		t.Errorf("the lock spawned %v on a machine with no logind", got)
	}
}

// The event carries the rows and what each one costs, so the surface can draw
// the second question without asking anything - the same bargain the picker's
// event makes, and it matters more here: a confirmation that filled in after it
// appeared would do so under somebody's finger.
//
// And with nobody acknowledging it, the answer says so, which is what makes the
// key print the menu instead of doing nothing on a session whose shell has died.
func TestThePowerMenuTellsAListenerAndSaysWhenNobodyDrewIt(t *testing.T) {
	s, _, _ := powerServer(t)
	rec := &recorder{}
	s.listen(&sink{w: rec})

	p := menu(t, s)
	if p.Shown {
		t.Error("nobody acknowledged the event and the answer claims a menu was shown")
	}
	var got struct{ Event Event }
	line := strings.TrimSpace(rec.String())
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the event is not one line of json: %q", line)
	}
	if got.Event.Kind != EventPower {
		t.Errorf("kind = %q, want the power menu's own kind", got.Event.Kind)
	}
	if len(got.Event.Choices) != len(p.Choices) {
		t.Errorf("the event carries %d rows and the answer has %d", len(got.Event.Choices), len(p.Choices))
	}
	if got.Event.Token == "" {
		t.Error("the event carries no token, so nothing can say it drew the surface")
	}
	// The costs travel with it. Without them the surface would have to ask a
	// second question before it could say what a log out ends.
	if len(choiceIn(got.Event.Choices, "logout").Costs) == 0 {
		t.Error("the event's log out row carries nothing about what it costs")
	}
}

func choiceIn(choices []PowerChoice, name string) PowerChoice {
	for _, c := range choices {
		if c.Name == name {
			return c
		}
	}
	return PowerChoice{}
}

// A name nothing here knows is refused by name rather than quietly doing the
// nearest thing. The menu sends what it was handed and a person types what they
// read, so this is the answer to a typo - and it must not be the answer to
// "poweroff" one day because a row was renamed.
func TestAPowerActionNobodyHasIsRefusedByName(t *testing.T) {
	s, l, sp := powerServer(t)

	resp := s.Dispatch(Request{Method: "system.power", Args: []string{"hibernate"}})
	if resp.Error == "" {
		t.Fatal("system.power hibernate was accepted")
	}
	if !strings.Contains(resp.Error, "hibernate") {
		t.Errorf("the refusal is %q and does not say what was asked for", resp.Error)
	}
	if got, asked := sp.all(), l.done(); len(got) != 0 || len(asked) != 0 {
		t.Errorf("it refused and then spawned %v and asked logind for %v", got, asked)
	}
	// And every name the menu offers is one this answers, which is the half a
	// spelling mistake in either place would break.
	for _, c := range menu(t, s).Choices {
		if resp := s.Dispatch(Request{Method: "system.power", Args: []string{c.Name}}); resp.Error != "" {
			t.Errorf("the menu offers %q and running it says %q", c.Name, resp.Error)
		}
	}
}

// A logind that answers with an error still gets the whole menu drawn, with the
// reason on the rows that needed it. The alternative is a key that draws
// nothing on the machine where the answer is worth reading.
//
// What it is not is a test of the bound on the call. That is a bound on a real
// bus and it is tested against one, with a logind that accepts the call and
// then says nothing (internal/power, fakebus_test.go): a fake at this boundary
// answers instantly whatever it answers, so a deadline round it here could
// never fire and would pass with the bound deleted.
func TestALogindThatAnswersWithAnErrorStillDrawsTheMenu(t *testing.T) {
	s, l, _ := powerServer(t)
	l.stateErr = errors.New("logind did not finish saying who is logged in")

	p := menu(t, s)
	if len(p.Choices) != 5 {
		t.Errorf("the menu has %d rows when logind would not answer", len(p.Choices))
	}
	if why := choice(t, p, "poweroff").Why; why == "" {
		t.Error("logind would not answer and the poweroff row says nothing about it")
	}
}
