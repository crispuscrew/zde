package zded

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// An inhibitor's two strings are written by whoever ran systemd-inhibit, which
// is any account on this machine, and the line built out of them is drawn on
// the power menu and printed to a terminal when no shell is up. So they are
// somebody else's text on its way to a screen, and they are filtered like it.
//
// The cost line is the worst place in zde to lose this: it is read at the
// moment somebody is deciding whether to end their session, which is exactly
// when a cleared screen or a forged extra row is worth something to somebody.
func TestAnInhibitorCannotWriteToTheScreenItIsExplainedOn(t *testing.T) {
	s, l, _ := powerServer(t)
	l.state = power.State{Blocks: []power.Block{{
		What: "sleep",
		Who:  "chromium\x1b[2J",
		Why:  "Playing audio\n.   suspend  nothing is holding this off",
	}}}

	said := strings.Join(choice(t, menu(t, s), "suspend").Costs, "\n")
	for _, bad := range []string{"\x1b", "\n."} {
		if strings.Contains(said, bad) {
			t.Errorf("the cost line is %q, and it still carries %q", said, bad)
		}
	}
	// Still says what is holding sleep, because a line that dropped the name
	// would trade one silence for another.
	if !strings.Contains(said, "chromium") || !strings.Contains(said, "Playing audio") {
		t.Errorf("the cost line is %q, want what is holding it and why", said)
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

// The power key against a system bus that accepts and then says nothing, which
// is a wedged bus or one whose daemon is stuck on a disk.
//
// Two things must not happen, and both were happening. The dial under each
// press left the socket it opened parked on a read nothing would ever satisfy,
// so a descriptor and three goroutines went with every press and never came
// back - forty presses took a session from seven descriptors to forty-seven,
// and a session out of descriptors has no power menu, no wifi list and no bar.
// And every press paid the whole two-second bound again, on a key whose entire
// job is to still work when the session has gone wrong.
//
// Driven through the real power.Open rather than a fake, because everything
// this is about is underneath that seam.
func TestThePowerKeyAgainstAWedgedBusCostsNothingPermanentAndAsksOnce(t *testing.T) {
	s, _, _ := powerServer(t)
	s.openPower = nil // the real one, at the bus the line below names
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", blackhole(t))

	// Before anything has dialled, so that the one dial this makes is inside
	// what is being counted rather than outside it.
	files := settledFiles(t)

	// The first press pays the bound and is what the rest are measured against.
	first := time.Now()
	menu(t, s)
	dial := time.Since(first)

	rest := time.Now()
	for i := 0; i < 20; i++ {
		p := menu(t, s)
		if why := choice(t, p, "poweroff").Why; why == "" {
			t.Fatalf("press %d drew a poweroff row with nothing on it about a bus that never answered", i)
		}
	}
	took := time.Since(rest)

	if after := settledFiles(t); after > files {
		t.Errorf("the presses left %d descriptors open, from %d: the socket under an "+
			"abandoned dial is never closed, and a session runs out of them", after-files, files)
	}
	// Twenty more dials would be twenty more of whatever the first one cost.
	// Half of one is far below that and far above what an answer already in
	// hand takes, which is microseconds.
	if took > dial/2 {
		t.Errorf("twenty presses took %s against one dial's %s: every press is dialling a bus "+
			"that already said it was not there", took, dial)
	}
}

// Only an absence is remembered, and not any other failure.
//
// A machine with no logind is a fact about the machine, and worth not asking
// about again for a minute. A bus that answered badly once is not: it is a
// moment, and a power menu that had written it down would go on saying so for a
// minute after the thing that caused it had gone - on the surface whose whole
// job is to be right about why a key did nothing.
func TestOnlyAnAbsenceIsRememberedAndNotAnyOtherFailure(t *testing.T) {
	s, l, _ := powerServer(t)
	tries := 0
	// One press at a time below, which is what makes counting it here safe.
	// Not the lock: logins() gave up holding powerMu across the dial, so a
	// counter here is only safe because these two presses are sequential (see
	// TestABurstOfPressesStillCostsOneDial for the concurrent one).
	s.openPower = func() (power.Manager, error) {
		tries++
		if tries == 1 {
			return nil, errors.New("the bus said something nobody here expected")
		}
		return l, nil
	}

	if why := choice(t, menu(t, s), "poweroff").Why; why == "" {
		t.Fatal("a press that could not reach logind drew a poweroff row saying nothing about it")
	}
	if why := choice(t, menu(t, s), "poweroff").Why; why != "" {
		t.Errorf("the next press says %q, and what failed once is worth asking again", why)
	}
	if tries != 2 {
		t.Errorf("logind was opened %d times, want the second press to have asked again", tries)
	}
}

// settledFiles is how many descriptors this process holds once the last press
// has finished letting go. A loop rather than one reading, because the
// goroutines a close unblocks are scheduled whenever the runtime feels like it.
func settledFiles(t *testing.T) int {
	t.Helper()
	open := func() int {
		names, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skipf("no /proc/self/fd to count descriptors in: %v", err)
		}
		return len(names)
	}
	n := open()
	for i := 0; i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
		if next := open(); next >= n {
			return n
		} else {
			n = next
		}
	}
	return n
}

// blackhole is a bus that takes a connection and then says nothing. It listens
// and never accepts: a unix connect completes as soon as the kernel has queued
// it, so the client is connected with nothing on the other end, and the queued
// half costs this process no descriptor of its own to confuse the count with.
func blackhole(t *testing.T) string {
	t.Helper()
	// Not t.TempDir: a unix socket path is capped at about 108 bytes, and a
	// directory named after a test this long has spent most of that already.
	dir, err := os.MkdirTemp("", "zde-power-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ln.Close()
		os.RemoveAll(dir)
	})
	return "unix:path=" + path
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

// The power chord does not wait behind the bar's poll.
//
// `system.idle` went onto the bar's five-second clock (shell/shell.qml), and it
// reaches logind through the same logins() the power menu does. With powerMu
// held across the dial, a poll that found the remembered absence stale held that
// lock for as long as a system bus takes to give up - bus.Within, two seconds -
// and a chord pressed inside that window waited for it, on the one surface whose
// whole job is to work when the session has gone wrong. On a machine with no
// logind that is 1,440 dials a day to land in.
//
// What is asserted is not how long the chord took, which is a number this
// machine decides. It is that the chord was answered while the poll's dial was
// still out: the dial is held open by this test until the menu has been drawn,
// so an answer at all is an answer that did not wait for it.
func TestThePowerChordIsNotHeldUpByTheBarsDialForLogind(t *testing.T) {
	s, _, _ := powerServer(t)
	dialing := make(chan struct{})
	release := make(chan struct{})
	var out sync.Once
	s.openPower = func() (power.Manager, error) {
		out.Do(func() { close(dialing) })
		<-release
		return nil, power.ErrNoLogind
	}
	// A machine that had no logind the last time anything looked, long enough
	// ago that the next caller goes and looks again. Written down rather than
	// waited for: noLogindFor is a minute and a test is not.
	s.noLogind = power.ErrNoLogind
	s.noLogindAt = time.Now().Add(-2 * noLogindFor)

	// The bar's poll, which is what finds the answer stale and dials.
	polled := make(chan Response, 1)
	go func() { polled <- s.idleHold() }()
	<-dialing

	// And the chord, while that dial is still out. Dispatched on a goroutine and
	// read back here, because a t.Fatal on a goroutine of its own is not one.
	pressed := make(chan Response, 1)
	go func() { pressed <- s.Dispatch(Request{Method: "system.power"}) }()
	select {
	case resp := <-pressed:
		close(release)
		if resp.Error != "" {
			t.Fatalf("system.power: %s", resp.Error)
		}
		var p Power
		if err := json.Unmarshal(resp.Ok, &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Choices) != 5 {
			t.Errorf("the menu drew %d rows on a machine with no logind, want all five", len(p.Choices))
		}
		if why := choiceIn(p.Choices, "poweroff").Why; why == "" {
			t.Error("the poweroff row says nothing about a logind that is not there")
		}
	case <-time.After(20 * time.Second):
		close(release)
		t.Fatal("the power chord was still waiting for the poll's dial to come back, which is what " +
			"holding powerMu across the dial costs the one surface that has to work when the session has not")
	}
	<-polled
}

// And a burst still costs one dial between them, which is what holding the lock
// across it used to buy: presses two to forty of a burst joined the first one's
// two seconds rather than starting two seconds of their own. Nothing is bought
// by not holding the lock if the answer is forty dials.
func TestABurstOfPressesStillCostsOneDial(t *testing.T) {
	s, l, _ := powerServer(t)
	var dials atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	var out sync.Once
	s.openPower = func() (power.Manager, error) {
		dials.Add(1)
		out.Do(func() { close(started) })
		<-release
		return l, nil
	}

	const presses = 40
	press := func(w *sync.WaitGroup) {
		defer w.Done()
		s.Dispatch(Request{Method: "system.power"})
	}
	var pressed sync.WaitGroup
	pressed.Add(1)
	go press(&pressed)
	// The first one is out before the rest arrive, which is what a burst is:
	// somebody leaning on the key, not forty presses in the same instant.
	<-started
	for i := 1; i < presses; i++ {
		pressed.Add(1)
		go press(&pressed)
	}
	// Long enough that a press which was going to dial for itself has, and
	// bounded so that a daemon which never answers fails here rather than hangs.
	time.Sleep(200 * time.Millisecond)
	close(release)
	pressed.Wait()

	if n := dials.Load(); n != 1 {
		t.Errorf("%d presses cost %d dials, want the one they wait on between them", presses, n)
	}
}
