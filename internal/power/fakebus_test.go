package power

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// A logind that is not logind, on a bus that is a real bus.
//
// The same arrangement internal/link uses for NetworkManager, and for the same
// reason: everything behind the Manager interface is a conversation with
// another process, so a fake at the Go boundary would be a fake of the
// conversation zde believes it is having - which is exactly the belief three of
// the defects in this package were about. What the tests change about the code
// under test is DBUS_SYSTEM_BUS_ADDRESS and XDG_SESSION_ID, both of which are
// environment the real thing reads, so the real Open() runs, the real reply
// shapes are decoded, and the two-second bound is a bound on a real call.
//
// It is deliberately not a copy of NetworkManager's fake. logind is a much
// smaller conversation - four methods and one property - so this is four
// methods and one property.

const dbusDaemon = "dbus-daemon"

// startBus brings up a private dbus-daemon and points the code under test at
// it. It dies with the test.
func startBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath(dbusDaemon); err != nil {
		t.Skipf("no %s on PATH, so there is no bus to fake logind on: "+
			"add pkgs.dbus to the devshell (flake.nix) and this runs", dbusDaemon)
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "bus.conf")
	// The socket goes wherever the daemon puts it rather than in the test's own
	// directory: a unix socket path is capped at about 108 bytes, and a Go temp
	// directory under a long TMPDIR has spent most of that before the file name.
	if err := os.WriteFile(cfg, []byte(`<!DOCTYPE busconfig PUBLIC
 "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:tmpdir=/tmp</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow own="*"/>
    <allow send_destination="*"/>
    <allow receive_sender="*"/>
  </policy>
</busconfig>
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(dbusDaemon, "--config-file="+cfg, "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		cmd.Process.Kill() //nolint:errcheck // already failing
		t.Fatalf("%s never printed an address: %v", dbusDaemon, err)
	}
	t.Cleanup(func() {
		// Interrupt rather than kill, so the daemon unlinks its socket on the
		// way out instead of leaving one in /tmp per test run.
		cmd.Process.Signal(os.Interrupt) //nolint:errcheck // it is going away either way
		cmd.Wait()                       //nolint:errcheck // its exit status is not this test's business
	})
	return strings.TrimSpace(line)
}

// exSession is one row of ListSessionsEx, in its own order on the wire:
// a(sussussbto). Written out rather than reused from logind.go, because a test
// that decoded with the code's own struct would agree with it about a
// reordering and prove nothing.
type exSession struct {
	ID     string
	UID    uint32
	User   string
	Seat   string
	Leader uint32
	Class  string
	TTY    string
	Idle   bool
	IdleAt uint64
	Path   dbus.ObjectPath
}

type exInhibitor struct {
	What string
	Who  string
	Why  string
	Mode string
	UID  uint32
	PID  uint32
}

// logindFake answers as systemd-logind does. Its state is public to the test on
// purpose, and behind a lock, because a test changes it while a call is in
// flight.
type logindFake struct {
	mu sync.Mutex
	// sessions and inhibitors are what the two list calls answer with.
	sessions   []exSession
	inhibitors []exInhibitor
	// slow is how long every call takes before it answers, which is how a test
	// says "logind stopped answering".
	slow time.Duration
	// fail is the D-Bus error a member answers with, by name and message. A bus
	// that will not answer is not the same fact as one that answers "no", and
	// telling those apart is the whole of Because.
	fail map[string][2]string
	// calls is every member that arrived, in order.
	calls []string
}

func (f *logindFake) record(member string) *dbus.Error {
	f.mu.Lock()
	f.calls = append(f.calls, member)
	slow := f.slow
	bad, failing := f.fail[member]
	f.mu.Unlock()
	if slow > 0 {
		time.Sleep(slow)
	}
	if failing {
		return dbus.NewError(bad[0], []any{bad[1]})
	}
	return nil
}

func (f *logindFake) asked(member string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == member {
			return true
		}
	}
	return false
}

func (f *logindFake) setFail(member, name, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string][2]string{}
	}
	f.fail[member] = [2]string{name, message}
}

// ListSessionsEx is the call zde makes, because it is the one that carries the
// session class.
func (f *logindFake) ListSessionsEx() ([]exSession, *dbus.Error) {
	if err := f.record("ListSessionsEx"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]exSession(nil), f.sessions...), nil
}

// ListSessions is here so that a test can find out it was called. Real logind
// still has it; zde must not use it, because its a(susso) has no class in it.
func (f *logindFake) ListSessions() ([]struct {
	ID   string
	UID  uint32
	User string
	Seat string
	Path dbus.ObjectPath
}, *dbus.Error) {
	f.record("ListSessions") //nolint:errcheck // the assertion is that this is never reached
	return nil, nil
}

func (f *logindFake) ListInhibitors() ([]exInhibitor, *dbus.Error) {
	if err := f.record("ListInhibitors"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]exInhibitor(nil), f.inhibitors...), nil
}

func (f *logindFake) Suspend(interactive bool) *dbus.Error  { return f.record("Suspend") }
func (f *logindFake) Reboot(interactive bool) *dbus.Error   { return f.record("Reboot") }
func (f *logindFake) PowerOff(interactive bool) *dbus.Error { return f.record("PowerOff") }
func (f *logindFake) TerminateSession(id string) *dbus.Error {
	return f.record("TerminateSession " + id)
}

// serveLogind puts the fake on a private bus and hands back the real Logind,
// opened the way zded opens it.
func serveLogind(t *testing.T, f *logindFake) *Logind {
	t.Helper()
	addr := startBus(t)
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck // the test is over
	if err := conn.Export(f, mgrPath, mgrIface); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName(service, dbus.NameFlagDoNotQueue)
	if err != nil {
		t.Fatal(err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("the fake could not take %s on its own bus", service)
	}

	// The whole seam: no production code knows it is being tested.
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)
	// What pam_systemd puts in a session's environment, and the first of the
	// three ways myID answers. Set so that these tests are about the sessions
	// and not about which of the three fallbacks a fake happens to reach.
	t.Setenv("XDG_SESSION_ID", "2")

	m, err := Open()
	if err != nil {
		t.Fatalf("Open against the fake bus: %v", err)
	}
	l, ok := m.(*Logind)
	if !ok {
		t.Fatalf("Open answered %T, and these tests drive the real one", m)
	}
	t.Cleanup(func() { l.Close() }) //nolint:errcheck // the test is over
	return l
}

// mine is the ordinary machine: one person, at the screen.
func mine() exSession {
	return exSession{
		ID: "2", UID: 1000, User: "me", Seat: "seat0", Leader: 1346,
		Class: "user", TTY: "tty1", Path: "/org/freedesktop/login1/session/_32",
	}
}

// A bus question with no deadline on it is a keypress that never comes back.
// zded answers every keybind on one socket, and a client gives a call five
// seconds (internal/zded, Client.Call), so a logind that accepts the call and
// then says nothing must cost this two seconds and not the whole session.
//
// The fake here does not answer for far longer than the bound, which is the
// only way to tell a bound from the absence of one: with a fake that replies
// at once, deleting askFor changes nothing anybody can see.
func TestALogindThatStopsAnsweringCostsTwoSecondsAndNotTheKeypress(t *testing.T) {
	f := &logindFake{sessions: []exSession{mine()}, slow: 20 * time.Second}
	l := serveLogind(t, f)

	type answer struct {
		st  State
		err error
	}
	done := make(chan answer, 1)
	started := time.Now()
	go func() {
		st, err := l.State()
		done <- answer{st, err}
	}()
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("a logind that never answered came back with %+v and no error", got.st)
		}
		// Comfortably inside the five seconds a client waits, and comfortably
		// short of the twenty the fake sits on.
		if waited := time.Since(started); waited > askFor+2*time.Second {
			t.Errorf("it gave up after %v, and the bound is %v", waited, askFor)
		}
	case <-time.After(askFor + 3*time.Second):
		t.Fatalf("a logind that stopped answering held the call for more than %v: "+
			"the bound on the call is gone, and this is a keybind that never returns", askFor+3*time.Second)
	}
}

// The refusal a held inhibitor produces, end to end, off a real bus.
//
// systemd answers this one itself and before polkit is asked anything:
// verify_shutdown_creds ends the call with
// org.freedesktop.login1.BlockedByInhibitorLock and a sentence that names
// neither the program holding the lock nor the reason it gave. Both are on
// zde's side from ListInhibitors, so if this regresses a suspend that will
// never happen is explained by "Operation denied due to active block
// inhibitor", and the thing to close is not on the screen.
func TestABlockInhibitorComesBackNamingWhatIsHoldingIt(t *testing.T) {
	f := &logindFake{
		sessions: []exSession{mine()},
		inhibitors: []exInhibitor{
			{What: "sleep", Who: "chromium", Why: "Playing audio", Mode: "block", UID: 1000, PID: 4321},
		},
	}
	l := serveLogind(t, f)
	f.setFail("Suspend", blockedByInhibitorLock, "Operation denied due to active block inhibitor")

	err := l.Do(Suspend)
	if err == nil {
		t.Fatal("a suspend logind refused came back as done")
	}
	for _, want := range []string{"chromium", "Playing audio"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal is %q, and it has to name %q: logind's own words name neither", err, want)
		}
	}
	if !f.asked("Suspend") {
		t.Error("nothing was asked of logind at all")
	}
}

// logind decides whether a power action needs the multiple-sessions
// authorisation by walking the sessions and taking only the ones
// SESSION_CLASS_IS_INHIBITOR_LIKE names (have_multiple_sessions,
// src/login/logind-dbus.c). A greeter waiting on another vt and the manager
// session systemd starts for a lingering user are not people at this machine.
//
// Reading them off ListSessions is what makes that impossible to get right,
// because a(susso) has no class in it - so this also checks that the older call
// is not the one being made. If this regresses, every machine with a display
// manager on it says gdm is logged in here as well, under the reboot row,
// every time.
func TestOnlyAPersonsSessionCountsAsSomebodyElseBeingHere(t *testing.T) {
	f := &logindFake{sessions: []exSession{
		mine(),
		{ID: "c1", UID: 970, User: "gdm", Seat: "seat0", Class: "greeter", Path: "/org/freedesktop/login1/session/c1"},
		{ID: "c2", UID: 998, User: "builder", Class: "background", Path: "/org/freedesktop/login1/session/c2"},
		{ID: "c3", UID: 998, User: "builder", Class: "manager", Path: "/org/freedesktop/login1/session/c3"},
	}}
	l := serveLogind(t, f)

	st, err := l.State()
	if err != nil {
		t.Fatal(err)
	}
	if f.asked("ListSessions") {
		t.Error("the sessions were read with ListSessions, which carries no class: " +
			"a greeter cannot be told from a person on that call")
	}
	if others := st.Others(); len(others) != 0 {
		t.Errorf("Others() = %+v on a machine where the only person is me", others)
	}
	// And when somebody really is here, they are counted.
	f.mu.Lock()
	f.sessions = append(f.sessions, exSession{
		ID: "5", UID: 1001, User: "ann", Seat: "seat1", Class: "user", TTY: "tty3",
		Path: "/org/freedesktop/login1/session/_35",
	})
	f.mu.Unlock()

	st, err = l.State()
	if err != nil {
		t.Fatal(err)
	}
	if others := st.Others(); len(others) != 1 || others[0].User != "ann" {
		t.Errorf("Others() = %+v, want the one person who is actually logged in", others)
	}
}

// A delay inhibitor postpones a suspend by seconds so something can save its
// work. It is not why anything is refused, and a menu that put one in front of
// somebody would carry a warning that never comes true.
func TestOnlyABlockInhibitorReachesTheMenu(t *testing.T) {
	f := &logindFake{
		sessions: []exSession{mine()},
		inhibitors: []exInhibitor{
			{What: "sleep", Who: "systemd-logind", Why: "Handling lid switch", Mode: "delay", UID: 0, PID: 1},
			{What: "shutdown", Who: "packagekit", Why: "Updating", Mode: "block", UID: 0, PID: 900},
		},
	}
	l := serveLogind(t, f)

	st, err := l.State()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Blocks) != 1 || st.Blocks[0].Who != "packagekit" {
		t.Fatalf("the blocks are %+v, want only the one that is a block", st.Blocks)
	}
	if len(st.Blocking(Suspend)) != 0 {
		t.Error("a delay inhibitor on sleep reads as standing in the way of a suspend")
	}
	if len(st.Blocking(Reboot)) != 1 {
		t.Error("a block inhibitor on shutdown does not stand in the way of a reboot")
	}
}
