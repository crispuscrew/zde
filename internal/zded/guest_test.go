package zded

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

// fakeLocker is the machine's screen locker without a screen: what it was asked
// to run, whether it would start at all, and - the half that matters - a way for
// the test to say when it exited and whether it let anybody in.
//
// It stands in for lockUntilUnlocked, which is the only thing in zde that waits
// for another program to finish. TestARealLockerEndsTheSessionWhenItExits
// drives that one for real, so the two halves of this are both covered: the
// wiring there, and every branch that hangs off it here.
type fakeLocker struct {
	mu       sync.Mutex
	s        *Server
	argv     [][]string
	startErr error
}

func (l *fakeLocker) run(argv []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.startErr != nil {
		return l.startErr
	}
	l.argv = append(l.argv, append([]string(nil), argv...))
	return nil
}

func (l *fakeLocker) started() [][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]string(nil), l.argv...)
}

// unlock is the locker exiting after somebody typed the password.
func (l *fakeLocker) unlock() {
	l.s.lockingOut(false)
	l.s.releaseGuest()
}

// died is the locker exiting without ever having asked anybody for one.
func (l *fakeLocker) died() { l.s.lockingOut(false) }

// guestServer is a daemon with three desks in niri, a journal to remember a
// guest session in, a locker it knows how to run, and a clipboard that records
// rather than reaches the session's.
//
// handed is what goes in guest.json, and "" writes no file at all: a machine
// that has never set one is the first case this action has to survive. vshop is
// where the person is standing, haven is what gets handed over, and clinic is up
// in niri like the other two so that the only thing between guest and a private
// desk is the check for one - a private desk with no workspaces would fail the
// switch anyway, and the test would pass on a daemon with no check in it.
func guestServer(t *testing.T, handed string, desks map[string]string) (*Server, *fakeCompositor, *fakeLocker) {
	t.Helper()
	// The palette resolves `system.lock` against what is on the PATH, which is
	// how lock-preset reaches a locker at all (palette.go, whyNot).
	withZdeOnPath(t)
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	// What `zde system lock` resolves against, and the reason a guest session
	// can know on the way in that it has a way out.
	writeJSON(t, filepath.Join(cfg, "zde", "apps.json"), map[string][]string{"lock": {"true"}})
	if handed != "" {
		writeJSON(t, filepath.Join(cfg, "zde", "guest.json"), map[string]string{"desk": handed})
	}
	dir := t.TempDir()
	for name, body := range desks {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, f := guestServerOn(t, filepath.Join(t.TempDir(), "j.jsonl"), dir)
	l := &fakeLocker{s: s}
	s.lockAndWait = l.run
	return s, f, l
}

// guestServerOn is the daemon alone, over a journal path the caller names - so
// that a test can build a second one over the same file, which is what a zded
// restart is.
func guestServerOn(t *testing.T, jpath, desksDir string) (*Server, *fakeCompositor) {
	t.Helper()
	jrn, err := journal.Open(jpath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jrn.Close() })
	f := &fakeCompositor{
		m: desk.Rebuild([]desk.Workspace{
			{Name: "vshop.DP-1.code", Output: "DP-1"},
			{Name: "haven.DP-1.db", Output: "DP-1"},
			{Name: "clinic.DP-1.mail", Output: "DP-1"},
		}, []string{"DP-1"}),
		focused: "vshop.DP-1.code",
		output:  "DP-1",
	}
	s := New("test", jrn, f, manifest.Dir(desksDir))
	t.Cleanup(func() { s.Close() })
	return s, f
}

// answer decodes what a verb answered with, so a test can read a list rather
// than a slice of bytes.
func answer(t *testing.T, resp Response, out any) {
	t.Helper()
	if resp.Error != "" {
		t.Fatalf("%s", resp.Error)
	}
	if err := json.Unmarshal(resp.Ok, out); err != nil {
		t.Fatalf("the answer is not what it says it is: %v", err)
	}
}

// guestOn hands the machine over, and then does what niri does and the fake
// compositor does not: the focus follows the switch. Every verb that reads
// where you are standing reads it off that, so a test that skipped it would be
// asking these questions from the desk the guest is not on.
func guestOn(t *testing.T, s *Server) {
	t.Helper()
	if resp := s.Dispatch(Request{Method: "desk.guest"}); resp.Error != "" {
		t.Fatalf("desk.guest: %s", resp.Error)
	}
	if f, ok := s.niri.(*fakeCompositor); ok {
		standOn(f, "haven.DP-1.db")
	}
}

// The whole action in one press: the desk handed over is on the screen, the
// session is silenced, and the guest session is written down where a restart
// will find it.
//
// The three are asserted together for the reason panic's three are: each one
// passes without the others, and each one alone is the feature not happening. A
// guest session that switched and did not silence is your notifications popping
// over somebody else's shoulder; one that silenced and did not switch is your
// work on the screen; one that did both and wrote nothing down is a machine
// that gives every desk back at the next rebuild.
//
// The mutation: drop the switch, the setMode, or the SetGuest. Each leaves the
// other two passing.
func TestGuestHandsOverOneDeskAndSilencesTheSession(t *testing.T) {
	s, f, _ := guestServer(t, "haven", nil)

	resp := s.Dispatch(Request{Method: "desk.guest"})
	if resp.Error != "" {
		t.Fatalf("desk.guest: %s", resp.Error)
	}
	if resp.Note != "" {
		t.Errorf("everything worked and the answer still had something to say: %q", resp.Note)
	}
	if got := f.focusCalls(); len(got) != 1 || got[0] != "haven.DP-1.db" {
		t.Errorf("niri was asked for %v, want the guest desk's workspace", got)
	}
	if s.mode() != attn.Quiet {
		t.Errorf("the mode is %s, and a guest session is supposed to be silence", s.mode())
	}
	if got := s.guestDesk(); got != "haven" {
		t.Errorf("the guest session says %q, want haven", got)
	}
}

// The order, and it is panic's order for panic's reason. The record is what
// every other part of a guest session is read off, so it must not be written
// before the desk it names is on the screen: a switch that failed with the
// record already down is a session silenced and restricted around the desk your
// work is on, which is the worst outcome this verb has.
//
// The mutation: write the record before switchDesk. This test fails and the
// rest pass.
func TestNothingIsRestrictedBeforeTheGuestDeskIsUp(t *testing.T) {
	s, f, _ := guestServer(t, "haven", nil)
	f.failOn = "haven.DP-1.db"

	resp := s.Dispatch(Request{Method: "desk.guest"})
	if resp.Error == "" {
		t.Fatalf("the switch failed and desk.guest answered %s", resp.Ok)
	}
	if got := s.guestDesk(); got != "" {
		t.Errorf("the session is restricted to %q over a desk that never came up", got)
	}
	if s.mode() != attn.Work {
		t.Errorf("the mode is %s over a desk that never came up", s.mode())
	}
}

// One desk unlocked and the rest refused: every verb that reaches a desk switch
// answers with the same refusal, and the desk handed over is still reachable.
//
// The mutation: take the gate out of switchFrom and put it in the desk.switch
// case instead. desk.switch still refuses; next, last and the regulars all walk
// straight off the guest desk, and this test says which.
func TestAGuestSessionRefusesEveryDeskButTheOneHandedOver(t *testing.T) {
	s, f, _ := guestServer(t, "haven", nil)
	guestOn(t, s)
	before := len(f.focusCalls())

	for _, req := range []Request{
		{Method: "desk.switch", Args: []string{"vshop"}},
		{Method: "desk.next"},
		{Method: "desk.prev"},
		{Method: "desk.regulars"},
		{Method: "nav.down"},
	} {
		resp := s.Dispatch(req)
		if resp.Error == "" {
			t.Errorf("%s answered %s during a guest session", req.Method, resp.Ok)
			continue
		}
		if !strings.Contains(resp.Error, "guest mode is on") {
			t.Errorf("%s refused with %q, which does not say why", req.Method, resp.Error)
		}
	}
	if got := f.focusCalls(); len(got) != before {
		t.Errorf("niri was asked for %v during a guest session", got[before:])
	}
	// And the desk that was handed over is not refused, or the guest could not
	// come back to it from a window jump or a workspace scroll.
	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"haven"}}); resp.Error != "" {
		t.Errorf("the desk that was handed over was refused too: %s", resp.Error)
	}
}

// The gate is on the mechanism and not on a list of verbs, so the two actions
// that switch desks for their own reasons meet it too - and each keeps its own
// character when it does. panic changes nothing at all, because its decoy is a
// desk it may not reach. lock-preset locks anyway, because a lock that refused
// over a desk is a screen left open.
//
// The mutation: gate the desk verbs in Dispatch instead of switchFrom. Both of
// these then walk off the guest desk, and neither of the tests above notices.
func TestPanicAndLockPresetMeetTheGuestGateToo(t *testing.T) {
	s, f, _ := guestServer(t, "haven", nil)
	writeJSON(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "zde", "panic.json"), map[string]string{"decoy": "clinic"})
	writeJSON(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "zde", "lock.json"), map[string]string{"preset": "clinic"})
	snd := &fakeSound{knows: true}
	s.UseSound(snd)
	sp := &spy{}
	s.spawn = sp.run
	guestOn(t, s)
	before := len(f.focusCalls())

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error == "" {
		t.Errorf("panic answered %s during a guest session", resp.Ok)
	}
	if snd.is() {
		t.Error("panic muted the sound over a decoy it could not reach")
	}
	resp := s.Dispatch(Request{Method: "system.lock-preset"})
	if resp.Error != "" {
		t.Fatalf("lock-preset refused during a guest session: %s", resp.Error)
	}
	if !strings.Contains(resp.Note, "guest mode is on") {
		t.Errorf("lock-preset locked and its note reads %q, which does not say why it did not switch", resp.Note)
	}
	if len(sp.all()) != 1 {
		t.Errorf("the locker was started %d times, want once", len(sp.all()))
	}
	if got := f.focusCalls(); len(got) != before {
		t.Errorf("niri was asked for %v during a guest session", got[before:])
	}
}

// Suspended, not cleared (docs/vision.md, section 3), which is two promises and
// this checks both. What was copied before the guest arrived is still in the
// ring and comes back whole; what they copy while they are there is never read
// at all, so there is nothing of theirs on the machine afterwards.
//
// The reads count is the load-bearing assertion and not the list length: the
// promise is that the bytes were never asked for, and only the clipboard being
// asked can say whether they were.
//
// The mutation: record during a guest session and drop the entries at the end
// instead. The list is the same size afterwards, the reads count is not, and
// this test says so.
func TestTheClipboardHistoryIsSuspendedAndComesBackWhole(t *testing.T) {
	s, _, l := guestServer(t, "haven", nil)
	board := newFakeClipboard()
	s.UseClipboard(board)

	board.offer("the invoice number", "text/plain")
	s.take()
	if got := len(s.clips.Rows(time.Now())); got != 1 {
		t.Fatalf("%d entries before the guest arrived, want the one that was copied", got)
	}
	guestOn(t, s)

	board.offer("their own password", "text/plain")
	reads, _ := board.counts()
	s.take()
	if got, _ := board.counts(); got != reads {
		t.Error("the clipboard was read during a guest session, so what they copied is in this daemon's memory")
	}
	if got := len(s.clips.Rows(time.Now())); got != 1 {
		t.Errorf("%d entries during a guest session, want only the one from before it", got)
	}
	if resp := s.Dispatch(Request{Method: MethodClip}); resp.Error == "" {
		t.Errorf("clip.history answered %s during a guest session", resp.Ok)
	}
	if resp := s.Dispatch(Request{Method: "clip.clear"}); resp.Error == "" {
		t.Errorf("clip.clear answered %s during a guest session", resp.Ok)
	}

	l.unlock()
	if got := len(s.clips.Rows(time.Now())); got != 1 {
		t.Errorf("%d entries after the guest session, want the one that was there before it", got)
	}
	if resp := s.Dispatch(Request{Method: MethodClip}); resp.Error != "" {
		t.Errorf("clip.history is still refused after the guest session: %s", resp.Error)
	}
}

// Putting an entry back is the same leak with an extra step, and it is the one
// arity of the one verb that never reaches Dispatch: the connection loop
// answers it itself, so a gate only in Dispatch would leave `Mod+v` and Enter
// working on somebody else's clipboard history.
//
// Asked over a real connection, because that is the only way this code path
// exists at all.
//
// The mutation: take the gate out of handle and leave it in Dispatch. Every
// other test here passes.
func TestPuttingAnEntryBackIsRefusedOverTheSocketToo(t *testing.T) {
	s, _, _ := guestServer(t, "haven", nil)
	board := newFakeClipboard()
	s.UseClipboard(board)
	board.offer("the invoice number", "text/plain")
	s.take()
	rows := s.clips.Rows(time.Now())
	if len(rows) != 1 {
		t.Fatalf("%d entries to put back, want one", len(rows))
	}
	guestOn(t, s)

	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var out []string
	if err := c.Call(MethodClip, &out, itoa(rows[0].ID)); err == nil {
		t.Fatalf("clip.history %d answered %v over the socket during a guest session", rows[0].ID, out)
	} else if !strings.Contains(err.Error(), "guest session") {
		t.Errorf("the refusal reads %q, which does not say why", err)
	}
	if _, writes := board.counts(); writes != 0 {
		t.Errorf("the clipboard was written %d times during a guest session", writes)
	}
}

// The queue is hidden and not spent, which is principle 3 in one test: the bar
// asks `queue.list` every two seconds and gets nothing, the verbs that would
// finish or empty it are refused, and every item is there when the session ends.
//
// The mutation: refuse queue.list instead of emptying it. The bar shows an error
// for as long as somebody borrows the machine, and the last assertion here still
// passes - which is why the emptied answer is asserted rather than any answer.
func TestTheQueueIsHiddenFromAGuestAndNotSpent(t *testing.T) {
	s, _, l := guestServer(t, "haven", nil)
	if resp := s.Dispatch(Request{Method: "queue.add", Args: []string{"reply to the invoice"}}); resp.Error != "" {
		t.Fatalf("queue.add: %s", resp.Error)
	}
	guestOn(t, s)

	var during []journal.Item
	answer(t, s.Dispatch(Request{Method: "queue.list"}), &during)
	if len(during) != 0 {
		t.Errorf("the bar was given %d queued items during a guest session", len(during))
	}
	for _, req := range []Request{
		{Method: "queue.clear"},
		{Method: "queue.done", Args: []string{"1"}},
	} {
		if resp := s.Dispatch(req); resp.Error == "" {
			t.Errorf("%s answered %s during a guest session", req.Method, resp.Ok)
		}
	}

	l.unlock()
	var after []journal.Item
	answer(t, s.Dispatch(Request{Method: "queue.list"}), &after)
	if len(after) != 1 {
		t.Errorf("%d queued items after the guest session, want the one that was there before it", len(after))
	}
}

// The other two surfaces that draw a person's correspondence, and the verb that
// would turn the popups back on. Each is refused by name, so a row added to
// guestBarred without a reason is a row somebody has to delete a test for.
//
// The mutation: drop attn.mode from the gate. Nothing else here notices, and a
// guest can put the notifications back on with one command.
func TestAGuestSessionRefusesTheSurfacesThatDrawYourRecords(t *testing.T) {
	s, _, _ := guestServer(t, "haven", nil)
	guestOn(t, s)

	for _, req := range []Request{
		{Method: "attn.center"},
		{Method: "attn.reach"},
		{Method: "attn.quiet"},
		{Method: "attn.mode", Args: []string{"work"}},
		{Method: "desk.snapshot"},
		{Method: "desk.move-window-to", Args: []string{"vshop"}},
		{Method: "desk.move-workspace-to", Args: []string{"vshop"}},
	} {
		if resp := s.Dispatch(req); resp.Error == "" {
			t.Errorf("%s answered %s during a guest session", req.Method, resp.Ok)
		}
	}
	// Reading the mode is the bar's question, asked on a clock, and refusing it
	// would put an error on the strip rather than the word quiet.
	if resp := s.Dispatch(Request{Method: "attn.mode"}); resp.Error != "" {
		t.Errorf("reading the mode was refused during a guest session: %s", resp.Error)
	}
	if s.mode() != attn.Quiet {
		t.Errorf("the mode is %s, so something got through the gate", s.mode())
	}
}

// A desk name is a project name, so the one desk this session will switch to is
// also the only one it will say out loud - in the picker, which is the surface
// a guest presses, and in the list the CLI prints when there is no shell.
//
// The mutation: reduce the picker and leave desk.list alone. `zde desk list` in
// the terminal on the guest desk reads out every project on the machine.
func TestAGuestSessionNamesOneDeskAndNoOthers(t *testing.T) {
	s, _, _ := guestServer(t, "haven", nil)
	guestOn(t, s)

	var listed []string
	answer(t, s.Dispatch(Request{Method: "desk.list"}), &listed)
	if len(listed) != 1 || listed[0] != "haven" {
		t.Errorf("desk.list said %v during a guest session, want the guest desk alone", listed)
	}
	var picked Switcher
	answer(t, s.Dispatch(Request{Method: "desk.switcher"}), &picked)
	if len(picked.Desks) != 1 || picked.Desks[0] != "haven" {
		t.Errorf("the picker offered %v during a guest session, want the guest desk alone", picked.Desks)
	}
}

// A guest session outlives the daemon, which is the one place this differs from
// panic on purpose: panic's hold is memory-only so that nothing on disk records
// that you pressed it, and a guest session forgotten by a restart is a machine
// that hands every desk back to whoever is sitting at it. zded restarts on every
// rebuild that touches it.
//
// The mutation: keep the guest session in a Server field instead of the journal.
// Every other test here passes.
func TestAGuestSessionSurvivesAZdedRestart(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(cfg, "zde", "apps.json"), map[string][]string{"lock": {"true"}})
	writeJSON(t, filepath.Join(cfg, "zde", "guest.json"), map[string]string{"desk": "haven"})
	jpath := filepath.Join(t.TempDir(), "j.jsonl")
	desksDir := t.TempDir()

	first, _ := guestServerOn(t, jpath, desksDir)
	first.lockAndWait = (&fakeLocker{s: first}).run
	guestOn(t, first)
	first.Close()

	// The same journal, a new daemon: this is `systemctl --user restart zded`.
	second, f := guestServerOn(t, jpath, desksDir)
	if got := second.guestDesk(); got != "haven" {
		t.Fatalf("after a restart the guest session says %q, want haven", got)
	}
	if resp := second.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error == "" {
		t.Errorf("after a restart the desks are back: desk.switch answered %s", resp.Ok)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v after a restart during a guest session", got)
	}
	if second.mode() != attn.Quiet {
		t.Errorf("the mode after a restart is %s, so the silence did not survive either", second.mode())
	}
}

// A machine that has never set a guest desk has nothing to hand over, so nothing
// at all happens and the answer names the option to set. panic's refusal rather
// than lock-preset's note, and for panic's reason: a session that silenced
// itself and restricted itself around the desk your work is on is worse than
// one that says it did nothing.
//
// The mutation: hand over the desk you are standing on and answer with a note.
func TestWithNoGuestDeskNothingIsHandedOver(t *testing.T) {
	s, f, _ := guestServer(t, "", nil)

	resp := s.Dispatch(Request{Method: "desk.guest"})
	if resp.Error == "" {
		t.Fatalf("with no guest desk set, desk.guest answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "zde.guest.desk") {
		t.Errorf("the refusal reads %q, and it should name the option to set", resp.Error)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v with no guest desk set", got)
	}
	if s.guestDesk() != "" || s.mode() != attn.Work {
		t.Errorf("something was restricted or silenced with no guest desk set: guest %q, mode %s", s.guestDesk(), s.mode())
	}
}

// The three refusals canShow decides, each in its own words and each changing
// nothing: a desk that is gone, one declared private, and a desks directory
// nothing can read.
//
// The private one is the case this action needs most. Handing somebody the one
// desk built to be unseen is the exact opposite of what the verb is for, and
// clinic is up in niri here, so the only thing stopping it is the check.
//
// The unreadable one is canShow's fail-closed branch with guest as the third
// caller (config.go): a directory that will not open says nothing about whether
// a desk is private, and reading that as "not private" would hand a desk over on
// exactly the machine where nobody can see that it did.
//
// The mutation: skip canShow. The private case hands over clinic and this is the
// only test that says so.
func TestAGuestDeskThatCannotBeShownIsNotHandedOver(t *testing.T) {
	for _, tc := range []struct {
		name, handed string
		desks        map[string]string
		unreadable   bool
		says         string
	}{
		{
			name: "a desk that is gone", handed: "atelier",
			says: "no workspaces",
		},
		{
			name: "a desk declared private", handed: "clinic",
			desks: map[string]string{"clinic": "name: clinic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n"},
			says:  "declares private",
		},
		{
			name: "a manifest that will not parse", handed: "haven",
			desks: map[string]string{"haven": "name: haven\n", "broken": "name: [\n"},
			says:  "will not parse",
		},
		{
			name: "manifests nothing can read", handed: "haven",
			desks: map[string]string{"haven": "name: haven\n"}, unreadable: true,
			says: "could not be read",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := guestServer(t, tc.handed, tc.desks)
			if tc.unreadable {
				s.desks = unreadableDesks{}
			}
			resp := s.Dispatch(Request{Method: "desk.guest"})
			if resp.Error == "" {
				t.Fatalf("desk.guest answered %s", resp.Ok)
			}
			if !strings.Contains(resp.Error, tc.says) {
				t.Errorf("the refusal reads %q, want it to say %q", resp.Error, tc.says)
			}
			if got := f.focusCalls(); len(got) != 0 {
				t.Errorf("niri was asked for %v", got)
			}
			if s.guestDesk() != "" || s.mode() != attn.Work {
				t.Errorf("something was restricted or silenced: guest %q, mode %s", s.guestDesk(), s.mode())
			}
		})
	}
}

// The way out of a guest session is this machine asking for a password, so a
// machine with no locker is one where the session could not be left - and that
// is checked on the way in rather than discovered on the way out. It is the one
// question guest asks that panic does not: panic's way back is a place, and a
// place cannot be missing.
//
// The mutation: check for a locker only in endGuest. Handing the machine over
// still works, and the person gets it back by editing a config file from the
// guest desk.
func TestWithNoLockerThereIsNoGuestSessionToEnter(t *testing.T) {
	s, f, _ := guestServer(t, "haven", nil)
	if err := os.Remove(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "zde", "apps.json")); err != nil {
		t.Fatal(err)
	}

	resp := s.Dispatch(Request{Method: "desk.guest"})
	if resp.Error == "" {
		t.Fatalf("with no locker, desk.guest answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "password") {
		t.Errorf("the refusal reads %q, and it should say why a locker is what a guest session needs", resp.Error)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v with no locker on the machine", got)
	}
	if s.guestDesk() != "" {
		t.Error("a guest session was opened on a machine with no way out of one")
	}
}

// The session ends when the locker lets somebody in, and not when the key is
// pressed. Pressing it puts the lock screen up and changes nothing else: the
// desks are still refused and the mode is still quiet while the lock screen is
// there, because whoever is looking at it has not typed anything yet.
//
// The mutation: release the session in endGuest instead of waiting for the
// locker. The first half of this test passes and the middle fails - the desks
// are back with the lock screen still up, so a locker that would not draw is a
// guest with your whole session.
func TestTheGuestSessionEndsOnlyWhenTheLockerLetsSomebodyIn(t *testing.T) {
	s, _, l := guestServer(t, "haven", nil)
	guestOn(t, s)

	resp := s.Dispatch(Request{Method: "desk.guest"})
	if resp.Error != "" {
		t.Fatalf("ending the guest session: %s", resp.Error)
	}
	if got := l.started(); len(got) != 1 || got[0][0] != "true" {
		t.Fatalf("the locker started as %v, want the one apps.json names", got)
	}
	if s.guestDesk() != "haven" {
		t.Error("the desks came back while the lock screen was still up")
	}
	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error == "" {
		t.Error("a desk switch went through while the lock screen was still up")
	}

	l.unlock()
	if got := s.guestDesk(); got != "" {
		t.Errorf("the guest session says %q after somebody unlocked", got)
	}
	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error != "" {
		t.Errorf("the desks did not come back after an unlock: %s", resp.Error)
	}
	if s.mode() != attn.Work {
		t.Errorf("the mode after an unlock is %s, want the one from before the guest arrived", s.mode())
	}
}

// The mode that comes back is the one from before the guest arrived, set rather
// than toggled, and this is the case that separates the two: somebody who was
// already in quiet when they handed the machine over must not have the
// notifications turned on for them by the unlock.
//
// The mutation: put work back instead of the remembered mode. The test above
// still passes, because work is what it was in.
func TestTheModeAGuestSessionGivesBackIsTheOneItTook(t *testing.T) {
	s, _, l := guestServer(t, "haven", nil)
	if resp := s.Dispatch(Request{Method: "attn.mode", Args: []string{"focus"}}); resp.Error != "" {
		t.Fatalf("attn.mode focus: %s", resp.Error)
	}
	guestOn(t, s)
	if s.mode() != attn.Quiet {
		t.Fatalf("the mode during the guest session is %s, want quiet", s.mode())
	}

	if resp := s.Dispatch(Request{Method: "desk.guest"}); resp.Error != "" {
		t.Fatalf("ending the guest session: %s", resp.Error)
	}
	l.unlock()
	if s.mode() != attn.Focus {
		t.Errorf("the mode after the guest session is %s, want the focus it was in before", s.mode())
	}
}

// A locker that exits without letting anybody in - it could not open the screen,
// or it crashed - is not a password, so the session stays exactly as it was and
// the next press locks again.
//
// The mutation: release on any exit rather than a clean one. A locker that dies
// on start hands the machine back to whoever is sitting at it.
func TestALockerThatNeverAskedForAPasswordLeavesTheSessionOn(t *testing.T) {
	s, _, l := guestServer(t, "haven", nil)
	guestOn(t, s)
	if resp := s.Dispatch(Request{Method: "desk.guest"}); resp.Error != "" {
		t.Fatalf("ending the guest session: %s", resp.Error)
	}

	l.died()
	if got := s.guestDesk(); got != "haven" {
		t.Fatalf("the guest session says %q after a locker that let nobody in", got)
	}
	if resp := s.Dispatch(Request{Method: "desk.switch", Args: []string{"vshop"}}); resp.Error == "" {
		t.Error("the desks came back after a locker that let nobody in")
	}
	// And the next press locks again, which is the whole of being able to try.
	if resp := s.Dispatch(Request{Method: "desk.guest"}); resp.Error != "" {
		t.Fatalf("the second press: %s", resp.Error)
	}
	if got := l.started(); len(got) != 2 {
		t.Errorf("the locker was started %d times, want one per press", len(got))
	}
}

// A locker that will not start at all leaves the session on and says so, which
// is the fail-closed direction: giving the desks back with nothing able to ask
// for a password is the one thing this must not do.
//
// The mutation: release the session before starting the locker.
func TestALockerThatWillNotStartLeavesTheSessionOn(t *testing.T) {
	s, _, l := guestServer(t, "haven", nil)
	guestOn(t, s)
	l.startErr = errors.New("swaylock: no wayland display")

	resp := s.Dispatch(Request{Method: "desk.guest"})
	if resp.Error == "" {
		t.Fatalf("with a locker that will not start, ending the session answered %s", resp.Ok)
	}
	if got := s.guestDesk(); got != "haven" {
		t.Errorf("the guest session says %q after a locker that would not start", got)
	}
	// And the claim was given back, or the next press would say the screen is
	// already locked on a machine with no lock screen on it.
	l.startErr = nil
	if resp := s.Dispatch(Request{Method: "desk.guest"}); resp.Error != "" {
		t.Errorf("the next press after a locker that would not start: %s", resp.Error)
	}
}

// Two presses while the lock screen is up must not be two lock screens: niri
// hands the keyboard grab to the oldest surface holding one, so the second
// locker would be a lock screen nobody can type into over one that has already
// been satisfied.
//
// The mutation: drop the lockingOut claim. The count below is 2.
func TestASecondPressWhileTheScreenIsLockedStartsNoSecondLocker(t *testing.T) {
	s, _, l := guestServer(t, "haven", nil)
	guestOn(t, s)
	if resp := s.Dispatch(Request{Method: "desk.guest"}); resp.Error != "" {
		t.Fatalf("ending the guest session: %s", resp.Error)
	}

	resp := s.Dispatch(Request{Method: "desk.guest"})
	if resp.Error == "" {
		t.Errorf("the second press answered %s rather than saying the screen is already locked", resp.Ok)
	}
	if got := l.started(); len(got) != 1 {
		t.Errorf("the locker was started %d times while one lock screen was up, want once", len(got))
	}
}

// lockUntilUnlocked is the only thing in zde that waits for another program to
// finish, and the fake above cannot prove it is wired up: a lockAndWait that
// never called releaseGuest would pass every test here. So this one runs a real
// locker - a script that exits, which is what a locker does when it has let
// somebody in - and waits for the session to end on its own.
//
// Both exits, because they are what the whole design turns on: a locker that
// exits cleanly has accepted a password and the desks come back; one that exits
// badly never asked for one and they do not.
//
// The mutation: ignore cmd.Wait's error in lockUntilUnlocked. The second half
// of this hangs until the deadline and then says the desks came back on a locker
// that crashed.
func TestARealLockerEndsTheSessionWhenItExits(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		ends         bool
	}{
		{name: "somebody typed the password", script: "#!/bin/sh\nexit 0\n", ends: true},
		{name: "the locker crashed", script: "#!/bin/sh\nexit 3\n", ends: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := guestServer(t, "haven", nil)
			// The real one, not the fake: this test is about the wiring.
			s.lockAndWait = nil
			locker := filepath.Join(t.TempDir(), "locker")
			if err := os.WriteFile(locker, []byte(tc.script), 0o700); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "zde", "apps.json"),
				map[string][]string{"lock": {locker}})
			guestOn(t, s)

			if resp := s.Dispatch(Request{Method: "desk.guest"}); resp.Error != "" {
				t.Fatalf("ending the guest session: %s", resp.Error)
			}
			ended := false
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				if s.guestDesk() == "" {
					ended = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if ended != tc.ends {
				t.Errorf("the guest session ended: %v, want %v", ended, tc.ends)
			}
		})
	}
}

// The desk is not an argument, and the refusal says where it is instead. A
// second spelling that took one would be a desk name typed while somebody is
// standing there, which is the whole reason it is a config line.
func TestGuestTakesNoArgument(t *testing.T) {
	s, _, _ := guestServer(t, "haven", nil)

	resp := s.Dispatch(Request{Method: "desk.guest", Args: []string{"clinic"}})
	if resp.Error == "" {
		t.Fatalf("desk.guest clinic answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "zde.guest.desk") {
		t.Errorf("the refusal reads %q, and it should name where the desk is set", resp.Error)
	}
	if s.guestDesk() != "" {
		t.Error("a guest session was opened by a request that was refused")
	}
}

// A daemon with nowhere to write it down refuses rather than opening a session
// it will forget. It is the same reasoning zen has for the same answer, one
// notch harder: a bar that comes back at the next login is untidy, and a guest
// session that does is a machine handing every desk back.
func TestWithNoJournalNoGuestSessionIsOpened(t *testing.T) {
	s, f, _ := guestServer(t, "haven", nil)
	s.jrn = nil

	resp := s.Dispatch(Request{Method: "desk.guest"})
	if resp.Error == "" {
		t.Fatalf("with no journal, desk.guest answered %s", resp.Ok)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v with nowhere to record the session", got)
	}
}

// The popups already on the screen go with the desk that is being handed over.
// Quiet stops the next one; a card that is up is a summary of something that
// arrived on your desk, drawn over the guest's screen for the rest of its five
// seconds (events.go, EventAttnHide).
//
// The mutation: drop the broadcast. Nothing else here notices.
func TestHandingTheMachineOverSweepsTheCardsAlreadyDrawn(t *testing.T) {
	s, _, _ := guestServer(t, "haven", nil)
	c, err := DialPath(serve(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var said string
	if err := c.Call(MethodEvents, &said); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the client subscribing", func() bool { return s.listeners() == 1 })
	guestOn(t, s)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ev, err := c.NextEventBefore(deadline)
		if err != nil {
			break
		}
		if ev.Kind == EventAttnHide {
			return
		}
	}
	t.Error("nothing swept the popups when the machine was handed over")
}

// "Guest mode restricts launching to the guest desk" (docs/model.md, section 6),
// and the mechanism is niri's rather than a refusal - because launching never
// reaches this daemon at all (guest.go, guestPins).
//
// A pin is a niri window rule naming a workspace by name, so an app pinned to
// another desk opens there and takes the session with it, past every gate zded
// has. So those rules leave the file while a guest is at the keyboard, and
// adoption puts the window on the desk the session is standing on.
//
// The mutation: leave SyncRules reading the whole map. The rule for vshop's
// browser is still in niri's config, and one launch from the guest desk opens a
// window on the desk that was handed over rather than out.
func TestAPinnedAppCannotOpenOffTheGuestDesk(t *testing.T) {
	s, f, l := guestServer(t, "haven", map[string]string{
		"haven": "name: haven\nmonitors: { DP-1: { workspaces: [db] } }\n" +
			"apps:\n  - { app: browser, app_id: org.mozilla.firefox, monitor: DP-1, workspace: db }\n",
		"vshop": "name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\n" +
			"apps:\n  - { app: nvim, app_id: nvim, monitor: DP-1, workspace: code }\n",
	})
	s.SyncRules()
	if !strings.Contains(readRules(t), "vshop.DP-1.code") {
		t.Fatal("vshop's pin is not in niri's config to begin with, so this test proves nothing")
	}
	reloads := f.reloadCount()

	guestOn(t, s)
	rules := readRules(t)
	if strings.Contains(rules, "vshop.DP-1.code") {
		t.Errorf("vshop's pin is still in niri's config during a guest session:\n%s", rules)
	}
	if !strings.Contains(rules, "haven.DP-1.db") {
		t.Errorf("the guest desk's own pin went with it:\n%s", rules)
	}
	if f.reloadCount() == reloads {
		t.Error("niri was not asked to read the rules again, so they arrive at its next poll rather than before the next launch")
	}

	l.unlock()
	if !strings.Contains(readRules(t), "vshop.DP-1.code") {
		t.Error("vshop's pin did not come back when the guest session ended")
	}
}

func readRules(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(dynamicPath())
	if err != nil {
		t.Fatalf("reading niri's dynamic config: %v", err)
	}
	return string(data)
}
