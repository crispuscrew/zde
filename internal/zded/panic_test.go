package zded

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/manifest"
)

// fakeSound is the machine's output without a machine: what the sound was when
// panic found it, what it is now, and what it was asked for in order.
//
// knows is whether the mute state can be read at all, which is a state a real
// machine has - a VM with no sound card, a session with no PipeWire - and one
// panic has to answer for rather than guess at.
type fakeSound struct {
	mu    sync.Mutex
	muted bool
	knows bool
	err   error
	asked []bool
	// onMute runs inside the call, so a test can see what the rest of the
	// daemon had already done at the instant the sound was taken.
	onMute func()
}

func (f *fakeSound) Muted() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.knows {
		return false, errors.New("no sound card here")
	}
	return f.muted, nil
}

func (f *fakeSound) Mute(on bool) error {
	f.mu.Lock()
	hook := f.onMute
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.asked = append(f.asked, on)
	f.muted = on
	return nil
}

func (f *fakeSound) is() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.muted
}

func (f *fakeSound) calls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.asked...)
}

// panicServer is a daemon with three desks in niri, a journal to keep a mode
// in, and a sound that is recorded rather than made.
//
// decoy is what goes in panic.json, and "" writes no file at all: a machine
// that has never set one is the first case this action has to survive. vshop is
// where the person is standing, haven is the harmless desk, and clinic is up in
// niri like the other two so that the only thing between panic and a private
// desk is the check for one - a private desk with no workspaces would fail the
// switch anyway, and the test would pass on a daemon with no check in it.
func panicServer(t *testing.T, decoy string, desks map[string]string) (*Server, *fakeCompositor, *fakeSound) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	if decoy != "" {
		writeJSON(t, filepath.Join(cfg, "zde", "panic.json"), map[string]string{"decoy": decoy})
	}
	dir := t.TempDir()
	for name, body := range desks {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	jrn, err := journal.Open(filepath.Join(t.TempDir(), "j.jsonl"))
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
	s := New("test", jrn, f, manifest.Dir(dir))
	t.Cleanup(func() { s.Close() })
	snd := &fakeSound{knows: true}
	s.UseSound(snd)
	return s, f, snd
}

// standOn is niri doing what the fake compositor does not: the focus follows the
// switch. Called by the tests that press the key twice, because which way panic
// goes is read off where the person is standing (see deskPanic).
func standOn(f *fakeCompositor, workspace string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.focused = workspace
}

// The whole action in one press: the decoy on the screen, the output off, and
// nothing allowed to interrupt.
//
// The mutation: drop any one of the three. Each leaves the other two passing,
// which is the reason all three are asserted here rather than in three tests -
// a panic that switches and does not mute is a video still playing to a room,
// and one that mutes and does not switch is the work still on the screen.
func TestPanicSwitchesToTheDecoyMutesAndSilences(t *testing.T) {
	s, f, snd := panicServer(t, "haven", nil)

	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	if resp.Note != "" {
		t.Errorf("everything worked and the answer still had something to say: %q", resp.Note)
	}
	if got := f.focusCalls(); len(got) != 1 || got[0] != "haven.DP-1.db" {
		t.Errorf("niri was asked for %v, want the decoy's workspace", got)
	}
	if !snd.is() {
		t.Error("the sound is still on")
	}
	if s.mode() != attn.Quiet {
		t.Errorf("the mode is %s, and panic is supposed to be silence", s.mode())
	}
}

// The order, which is what keeps a failure from leaving the machine in a state
// that is neither the work nor the decoy. The assertion is made from inside the
// mute: at the instant the sound is taken, niri must already have been told to
// focus the decoy and the silence must already be in force.
//
// A test that looked afterwards would pass on every ordering. This one fails on
// the one that matters: the sound going off while the work is still on the
// screen is what a person reads as "it worked".
//
// The mutation: mute before the switch, or before the silence.
func TestNothingIsTakenBeforeTheDecoyIsUp(t *testing.T) {
	s, f, snd := panicServer(t, "haven", nil)

	var mu sync.Mutex
	var focusedWhenMuted []string
	var modeWhenMuted attn.Mode
	snd.onMute = func() {
		mu.Lock()
		defer mu.Unlock()
		focusedWhenMuted = f.focusCalls()
		modeWhenMuted = s.mode()
	}

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(focusedWhenMuted) == 0 {
		t.Fatal("the sound went off before niri had been asked for anything, so the work was still on the screen")
	}
	if focusedWhenMuted[len(focusedWhenMuted)-1] != "haven.DP-1.db" {
		t.Errorf("when the sound went off niri had been asked for %v, want the decoy", focusedWhenMuted)
	}
	if modeWhenMuted != attn.Quiet {
		t.Errorf("the mode was %s when the sound went off, and a notification landing in that gap pops onto the decoy", modeWhenMuted)
	}
}

// A machine that has never set a decoy has nothing to put on the screen, so
// nothing at all happens and the answer names the option to set. lock-preset
// locks anyway in the same position, because a lock with no preset is still a
// lock; a panic that muted the sound and silenced the notifications over the
// work it was asked to hide is a person believing they are covered.
//
// The mutation: mute and silence anyway, and answer with a note. The sound goes
// off, which is what somebody reads as the key having worked, and the screen
// still has their work on it.
func TestWithNoDecoyNothingHappensAndTheRefusalNamesTheOption(t *testing.T) {
	s, f, snd := panicServer(t, "", nil)

	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error == "" {
		t.Fatalf("with no decoy set, desk.panic answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "zde.panic.decoy") {
		t.Errorf("the refusal reads %q, and it should name the option to set", resp.Error)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v with no decoy set", got)
	}
	if snd.is() || len(snd.calls()) != 0 {
		t.Errorf("the sound was touched by a panic that could not happen: %v", snd.calls())
	}
	if s.mode() != attn.Work {
		t.Errorf("the mode is %s after a panic that did not happen", s.mode())
	}
}

// A private desk is out of the picker, popups off, capture-blocked
// (docs/vision.md, section 3). It is the one desk with the most to hide, so it
// is the one desk that must never be what panic puts on the screen for somebody
// else to look at.
//
// The mutation: drop the canShow call. panic then walks the person who just
// wanted their work hidden onto their most private desk.
func TestAPrivateDeskIsNotADecoy(t *testing.T) {
	s, f, snd := panicServer(t, "clinic", map[string]string{
		"clinic": "name: clinic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n",
	})

	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error == "" {
		t.Fatalf("with a private decoy, desk.panic answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "private") {
		t.Errorf("the refusal reads %q, and it should say why", resp.Error)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v, and a private desk is not a decoy", got)
	}
	if snd.is() {
		t.Error("the sound went off for a panic that did not happen")
	}
}

// And the check fails closed. A manifest that will not parse is the ordinary
// state of a directory somebody edits by hand, and the switch does not need a
// manifest to happen - so a check reading "cannot tell" as "not private" would
// send panic to a private desk on exactly the machine where nobody can see that
// it did (docs/vision.md, principle 9).
//
// The mutation: read the decoy's manifest through manifestFor, which drops the
// error, so an unreadable directory and a desk nothing declares answer the
// same. The switch goes through.
func TestAManifestThatWillNotParseStopsThePanic(t *testing.T) {
	s, f, snd := panicServer(t, "haven", map[string]string{
		"broken": "name: haven\nmonitorz: nope\n",
	})

	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error == "" {
		t.Fatalf("beside a broken manifest, desk.panic answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "broken.yaml") {
		t.Errorf("the refusal reads %q, and it should name the file to fix", resp.Error)
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri was asked for %v while nothing could say whether that desk is private", got)
	}
	if snd.is() {
		t.Error("the sound went off while nothing could say whether the decoy is private")
	}
}

// A decoy naming a desk that is gone - renamed, deleted, never made - is the
// failure that happens after every check has passed, and it is the one this
// action cannot paper over: there is no other way to take the work off the
// screen. So it stops there, and nothing else is started.
//
// The mutation: carry on and mute anyway. The person then has a quiet machine
// showing exactly what they pressed the key to hide, with the failure on a
// stderr a keybind does not have.
func TestADecoyThatIsGoneStopsBeforeAnythingIsTaken(t *testing.T) {
	s, f, snd := panicServer(t, "ghost", nil)

	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error == "" {
		t.Fatalf("with a decoy that is not there, desk.panic answered %s", resp.Ok)
	}
	if !strings.Contains(resp.Error, "ghost") {
		t.Errorf("the refusal reads %q, and it should name the desk that is not there", resp.Error)
	}
	if snd.is() || len(snd.calls()) != 0 {
		t.Errorf("the sound was taken by a panic with nowhere to switch to: %v", snd.calls())
	}
	if s.mode() != attn.Work {
		t.Errorf("the notifications were silenced by a panic with nowhere to switch to: %s", s.mode())
	}
	if got := f.focusCalls(); len(got) != 0 {
		t.Errorf("niri focused %v, and there is no such desk", got)
	}
}

// The same key both ways. A key that hides a session and cannot give it back
// is a way to lose the session, and the way back has to be the key already
// under the finger rather than a second one to remember at the worst moment.
//
// The mutation: make it one-way. The second press hides again, and the sound
// and the desk never come back.
func TestTheSameKeyComesBack(t *testing.T) {
	s, f, snd := panicServer(t, "haven", nil)

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	// niri is what moves the focus, and this fake does not, so this is the
	// arrival on the decoy that the second press is read against.
	standOn(f, "haven.DP-1.db")

	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error != "" {
		t.Fatalf("the second press: %s", resp.Error)
	}
	if resp.Note != "" {
		t.Errorf("coming back had something to say: %q", resp.Note)
	}
	got := f.focusCalls()
	if len(got) != 2 || got[1] != "vshop.DP-1.code" {
		t.Errorf("niri was asked for %v, want the desk the person was on", got)
	}
	if snd.is() {
		t.Error("the sound is still off after coming back")
	}
	if s.mode() != attn.Work {
		t.Errorf("the mode is %s after coming back, and it was work before", s.mode())
	}
}

// And the mode that comes back is the one that was in force, not work. Mod+q
// coming out of quiet always goes to work (attn.go, toggleQuiet) because a
// toggle that remembers answers one key with two modes; this is the opposite
// case, and somebody who was already in quiet - in a call, in a screencast -
// must not have the notifications turned on for them by the key that gives
// their desk back.
//
// The mutation: come back into work. The call somebody panicked during ends
// with everything that queued up during it arriving at once.
func TestComingBackGivesTheModeThatWasInForce(t *testing.T) {
	s, f, _ := panicServer(t, "haven", nil)
	if resp := s.Dispatch(Request{Method: "attn.mode", Args: []string{"quiet"}}); resp.Error != "" {
		t.Fatalf("attn.mode quiet: %s", resp.Error)
	}

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	standOn(f, "haven.DP-1.db")
	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("the second press: %s", resp.Error)
	}
	if s.mode() != attn.Quiet {
		t.Errorf("the mode is %s, and it was quiet before panic touched it", s.mode())
	}
}

// Panic gives back what it took and nothing else. Somebody who had muted for a
// meeting and then panicked must not have the sound put back on for them by the
// key that gives their desk back.
//
// The mutation: unmute unconditionally on the way out. The room hears whatever
// was playing.
func TestComingBackDoesNotUnmuteWhatPanicDidNotMute(t *testing.T) {
	s, f, snd := panicServer(t, "haven", nil)
	snd.muted = true

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	standOn(f, "haven.DP-1.db")
	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("the second press: %s", resp.Error)
	}
	if !snd.is() {
		t.Error("the sound was turned on by a key that never turned it off")
	}
}

// A machine that cannot say whether it was muted stays quiet, and says so. The
// two ways this could go wrong are opposite and only one of them is loud: sound
// somebody did not ask for, in a room they are in with somebody else.
//
// The mutation: treat "cannot tell" as "it was on". The unmute then happens on
// a machine nothing here knows anything about.
func TestASoundThatCannotBeReadIsLeftAlone(t *testing.T) {
	s, f, snd := panicServer(t, "haven", nil)
	snd.knows = false

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	standOn(f, "haven.DP-1.db")
	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error != "" {
		t.Fatalf("the second press: %s", resp.Error)
	}
	if !snd.is() {
		t.Error("the sound was turned on by a daemon that could not say whether it had turned it off")
	}
	if !strings.Contains(resp.Note, "sound") {
		t.Errorf("the note reads %q, and somebody is looking at a machine that makes no noise", resp.Note)
	}
}

// The half that cannot be done is said in words rather than done silently. A
// machine with no sound card is every VM and some desktops, and the desk and
// the silence are still worth having there - but a panic that answered as
// though it had muted a machine it never reached would be this action lying
// about the one thing it is for.
//
// The mutation: swallow the error. The answer then says panic happened, and
// what is playing goes on playing.
func TestAMuteThatCouldNotHappenIsSaidAndTheRestStillHappens(t *testing.T) {
	s, f, snd := panicServer(t, "haven", nil)
	snd.err = errors.New("wpctl set-mute: no such sink")

	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error != "" {
		t.Fatalf("desk.panic on a machine with no sound: %s", resp.Error)
	}
	if !strings.Contains(resp.Note, "no such sink") {
		t.Errorf("the note reads %q, and it should carry what the machine said", resp.Note)
	}
	if got := f.focusCalls(); len(got) != 1 || got[0] != "haven.DP-1.db" {
		t.Errorf("niri was asked for %v: the decoy is worth having on a machine with no sound", got)
	}
	if s.mode() != attn.Quiet {
		t.Errorf("the mode is %s: the silence is worth having on a machine with no sound", s.mode())
	}
}

// A daemon with no way to reach the sound at all says that too, in the same
// place. This is the state a zded wired without UseSound is in, and the answer
// has to be a sentence rather than a mute that quietly never happened.
func TestNoSoundAtAllIsAnAnswerAndNotASilence(t *testing.T) {
	s, f, _ := panicServer(t, "haven", nil)
	s.UseSound(nil)

	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	if !strings.Contains(resp.Note, "sound") {
		t.Errorf("the note reads %q, and nothing muted this machine", resp.Note)
	}
	// And coming back says nothing about a sound it never took. The mutation:
	// answer for the sound off what was read rather than off what was done. The
	// key then reports leaving the machine muted on one that never went quiet.
	standOn(f, "haven.DP-1.db")
	if back := s.Dispatch(Request{Method: "desk.panic"}); strings.Contains(back.Note, "sound") {
		t.Errorf("coming back said %q about a sound panic never touched", back.Note)
	}
}

// Pressed anywhere but on the decoy, panic hides - even while it is holding.
// The case is a person who came off the decoy by hand and then had somebody
// walk in: a key that undid the panic because one was held would put the work
// back on the screen at exactly that moment, which is the one thing this key
// must never do.
//
// The mutation: toggle on the held state alone. The desk comes back, in front
// of whoever just walked in.
func TestPanicPressedOffTheDecoyHidesAgain(t *testing.T) {
	s, f, snd := panicServer(t, "haven", nil)

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	// Off the decoy by hand, and the sound and the mode with it.
	standOn(f, "vshop.DP-1.code")
	if resp := s.Dispatch(Request{Method: "attn.mode", Args: []string{"work"}}); resp.Error != "" {
		t.Fatalf("attn.mode work: %s", resp.Error)
	}
	if err := s.setMuted(false); err != nil {
		t.Fatal(err)
	}

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("the second press: %s", resp.Error)
	}
	got := f.focusCalls()
	if len(got) != 2 || got[1] != "haven.DP-1.db" {
		t.Errorf("niri was asked for %v, and the second press should have hidden again", got)
	}
	if !snd.is() {
		t.Error("the sound is on after a press that was supposed to hide")
	}
	if s.mode() != attn.Quiet {
		t.Errorf("the mode is %s after a press that was supposed to hide", s.mode())
	}
}

// The cards already on the screen go with the desk they belong to, and the
// silence is in force before they do. A popup is the sender's name and the
// summary of something that arrived on the desk panic has just hidden, drawn
// over the decoy for the rest of its five seconds.
//
// The assertion is made from inside the listener, for the reason the mute test
// makes its assertion inside the mute: in the other order, a notification
// arriving in the gap pops straight back onto the decoy and this test is the
// only thing that would notice.
//
// The mutation: drop the broadcast, or move it before the silence.
func TestPanicTakesTheCardsOffTheScreenAfterTheSilence(t *testing.T) {
	s, _, _ := panicServer(t, "haven", nil)
	seen := &modeWatcher{s: s}
	s.listen(&sink{w: seen})

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	lines, modes := seen.all()
	// The first of them, deliberately: what is being asserted is that no sweep
	// happens before the silence does, and looking at the last one would pass on
	// a daemon that swept early and then swept again.
	i := -1
	for n, line := range lines {
		if strings.Contains(line, `"kind":"`+EventAttnHide+`"`) {
			i = n
			break
		}
	}
	if i < 0 {
		t.Fatalf("nothing told the shell to take the popups off the screen: %v", lines)
	}
	if modes[i] != attn.Quiet {
		t.Errorf("the mode was %s when the cards were swept, so the next arrival pops onto the decoy", modes[i])
	}
}

// modeWatcher is a listener that writes down the mode at the instant each event
// reached it.
type modeWatcher struct {
	s     *Server
	mu    sync.Mutex
	lines []string
	modes []attn.Mode
}

func (w *modeWatcher) Write(p []byte) (int, error) {
	// The mode read before the line is recorded, because the read is what this
	// is here for and the append is not.
	m := w.s.mode()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, string(p))
	w.modes = append(w.modes, m)
	return len(p), nil
}

func (w *modeWatcher) all() ([]string, []attn.Mode) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...), append([]attn.Mode(nil), w.modes...)
}

// panic hides a screen and forgets nothing. What was waiting is still waiting
// when it comes back, and what arrived while it held is in the notification
// history: display policy, never data policy (docs/vision.md, principle 3). A
// panic that emptied the queue would be a way to lose what somebody was in the
// middle of, and it would not hide anything - the queue is a number on the bar
// and a surface behind a key.
//
// The mutation: clear the queue on the way in. Every approval somebody was
// working through is gone, permanently, for a person who walked past.
func TestPanicKeepsWhatIsWaiting(t *testing.T) {
	s, _, _ := panicServer(t, "haven", nil)
	if resp := s.Dispatch(Request{Method: "queue.add", Args: []string{"review the deploy"}}); resp.Error != "" {
		t.Fatalf("queue.add: %s", resp.Error)
	}

	if resp := s.Dispatch(Request{Method: "desk.panic"}); resp.Error != "" {
		t.Fatalf("desk.panic: %s", resp.Error)
	}
	if waiting := s.jrn.Waiting(); len(waiting) != 1 {
		t.Errorf("the queue holds %d things after a panic, and it held one before", len(waiting))
	}
}

// The decoy is a line in a config and never an argument. A desk name typed
// after this verb is a name to get wrong with somebody already in the room.
func TestPanicTakesNoArguments(t *testing.T) {
	s, _, _ := panicServer(t, "haven", nil)

	if resp := s.Dispatch(Request{Method: "desk.panic", Args: []string{"haven"}}); resp.Error == "" {
		t.Errorf("desk.panic haven answered %s, and there is no such spelling", resp.Ok)
	}
}

// The refusal goes to a terminal, and the decoy's name came out of a file
// somebody edited by hand. So it goes through the filter every other foreign
// line in zde does, or a config could clear the screen the refusal was printed
// on (internal/attn, Line).
//
// The mutation: drop attn.Line from canShow. The escape arrives whole.
func TestThePanicRefusalCannotCarryAnEscapeOutOfAConfig(t *testing.T) {
	s, _, _ := panicServer(t, "cli\x1b[2Jnic", map[string]string{
		"clinic": "name: cli\x1b[2Jnic\nprivate: true\nmonitors: { DP-1: { workspaces: [mail] } }\n",
	})

	resp := s.Dispatch(Request{Method: "desk.panic"})
	if resp.Error == "" {
		t.Fatalf("a private decoy answered %s", resp.Ok)
	}
	if strings.ContainsRune(resp.Error, '\x1b') {
		t.Errorf("the refusal carries an escape out of a config: %q", resp.Error)
	}
}
