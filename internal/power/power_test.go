package power

import (
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// polkitRefusal is polkit saying no: logind asked it, it wants an
// administrator's password, and the call was made non-interactively because
// nothing in a zde session can put a password prompt on the screen.
func polkitRefusal() error {
	return dbus.Error{
		Name: "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired",
		Body: []any{"Interactive authentication required."},
	}
}

// inhibitorRefusal is logind saying no on its own account, which is what a held
// block inhibitor produces and polkit has nothing to do with: since v257
// verify_shutdown_creds ends the call here, before polkit is asked anything.
// The sentence is systemd's own, and it names nothing.
func inhibitorRefusal() error {
	return dbus.Error{
		Name: blockedByInhibitorLock,
		Body: []any{"Operation denied due to active block inhibitor"},
	}
}

// logind puts everything an inhibitor covers in one colon-separated field, so
// the reading of that field is what decides which key a browser holding sleep
// is allowed to stop.
//
// Wrong in one direction, a suspend is refused with nothing on the screen about
// the thing that refused it; wrong in the other, a reboot carries a warning
// about something that was never going to stop it - and a warning that is not
// true is one people learn to press through, which is the habit the whole
// confirmation exists not to build.
func TestAnInhibitorOnSleepStopsASuspendAndNotAReboot(t *testing.T) {
	audio := Block{What: "sleep", Who: "chromium", Why: "Playing audio"}
	if !audio.Stands(Suspend) {
		t.Error("something holding sleep does not stand in the way of a suspend")
	}
	for _, w := range []What{Reboot, PowerOff, Logout} {
		if audio.Stands(w) {
			t.Errorf("something holding sleep reads as standing in the way of a %s", w)
		}
	}

	// One inhibitor can hold both, which is what the colon is for.
	both := Block{What: "sleep:shutdown", Who: "packagekit"}
	for _, w := range []What{Suspend, Reboot, PowerOff} {
		if !both.Stands(w) {
			t.Errorf("sleep:shutdown does not stand in the way of a %s", w)
		}
	}
	// And logind's other two, which are about lids and power buttons and have
	// nothing to say about a key somebody pressed.
	if idle := (Block{What: "idle:handle-lid-switch"}); idle.Stands(Suspend) || idle.Stands(PowerOff) {
		t.Error("an idle inhibitor reads as standing in the way of a power action")
	}
	// A log out is inhibited by nothing: logind has no inhibitor for ending a
	// session, so a warning here could never come true.
	if both.Stands(Logout) {
		t.Error("a log out reads as inhibitable, and logind has no such inhibitor")
	}
}

// The refusal is the whole point of this surface, and there are two of them.
//
// logind's own, for a block inhibitor, arrives as "Operation denied due to
// active block inhibitor" and names neither the program nor the reason it gave.
// If this regresses, a suspend that never happens is explained by a sentence
// with nothing in it, and the thing to close is not on the screen.
func TestAnInhibitorRefusalNamesTheProgramHoldingIt(t *testing.T) {
	held := State{Blocks: []Block{{What: "sleep", Who: "chromium", Why: "Playing audio"}}}
	err := Because(Suspend, held, inhibitorRefusal())
	if err == nil {
		t.Fatal("a refused suspend came back as no error at all")
	}
	for _, want := range []string{"chromium", "Playing audio"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is %q, and it has to name %q", err, want)
		}
	}

	// The same refusal with nothing on this side to point at - the lock was
	// taken between the two reads, or the inhibitors could not be read. Still
	// said as the refusal it is rather than as the D-Bus name it arrived as.
	err = Because(Suspend, State{}, inhibitorRefusal())
	if err == nil || strings.Contains(err.Error(), "BlockedByInhibitorLock") {
		t.Errorf("refusal is %v, want it said as a refusal and not as a bus name", err)
	}
	if !strings.Contains(err.Error(), "holding") {
		t.Errorf("refusal is %q, and it has to say something is holding the suspend off", err)
	}

	// And systemd v257's spelling of the same short-circuit, which is a plain
	// access-denied: "Access denied due to active block inhibitor". A machine
	// that says it that way must not have its inhibitor explained as a password
	// nothing can ask for.
	old := dbus.Error{
		Name: "org.freedesktop.DBus.Error.AccessDenied",
		Body: []any{"Access denied due to active block inhibitor"},
	}
	err = Because(Suspend, held, old)
	if !strings.Contains(err.Error(), "chromium") {
		t.Errorf("refusal is %q, and on v257 that access-denied is the inhibitor", err)
	}
	if strings.Contains(err.Error(), "password") {
		t.Errorf("refusal is %q, and no password gets a browser to stop playing audio", err)
	}
	// With nothing holding this verb, the same name is polkit's own no and is
	// said as one - a sleep inhibitor is not why a reboot was denied, and there
	// is no inhibitor at all to tell somebody about.
	err = Because(Reboot, held, old)
	if !strings.Contains(err.Error(), "administrator") {
		t.Errorf("refusal is %q, and with nothing holding a reboot that access-denied is polkit's", err)
	}
	if strings.Contains(err.Error(), "chromium") || strings.Contains(err.Error(), "holding") {
		t.Errorf("refusal is %q, and something holding sleep is not in the way of a reboot", err)
	}
}

// polkit's, which on an ordinary desktop is the rarer one: systemd's policy
// gives allow_active "yes" to reboot, power-off, suspend and their
// -multiple-sessions variants, so what produces this is a session polkit does
// not call active. It arrives as a bare "interactive authentication required",
// which tells a person nothing about the second person on the machine.
//
// And an inhibitor is never the reason for it: logind ends the call on a block
// inhibitor before polkit is asked at all, so naming the browser here would
// send somebody to close a tab that was never in the way.
func TestAPolkitRefusalNamesWhoElseIsHereAndNotAnInhibitor(t *testing.T) {
	crowded := State{
		Sessions: []Session{{ID: "1", User: "me", Mine: true}, {ID: "3", User: "ann"}},
		Blocks:   []Block{{What: "sleep", Who: "chromium", Why: "Playing audio"}},
	}
	err := Because(Reboot, crowded, polkitRefusal())
	if err == nil {
		t.Fatal("a refused reboot came back as no error at all")
	}
	if !strings.Contains(err.Error(), "ann") {
		t.Errorf("refusal is %q, and it has to say who else is logged in", err)
	}
	if strings.Contains(err.Error(), "chromium") {
		t.Errorf("refusal is %q, and a sleep inhibitor is not why polkit refused a reboot", err)
	}

	// And with nothing on this machine to point at, it is still said as a
	// refusal rather than as a bus error.
	err = Because(PowerOff, State{}, polkitRefusal())
	if !strings.Contains(err.Error(), "administrator") {
		t.Errorf("refusal is %q, want the reason it cannot be answered from here", err)
	}
}

// Everything but those two is logind's own account of what went wrong, and it
// is better than any summary of it: a machine with no swap refuses a hibernate
// in its own words, and a bus that has gone says so. If this regresses, a real
// fault is reported as an authorisation problem and somebody goes looking for a
// password.
//
// The case worth having here is a D-Bus error with a different name, because
// that is the one a translation reaches for by accident: errorName answers ""
// for anything that never came off a bus, so a test built only out of
// errors.New would pass whatever the two names were widened to.
func TestAnythingThatIsNotOneOfThoseTwoKeepsLogindsOwnWords(t *testing.T) {
	// logind's own name for a verb this machine cannot do, off the same bus and
	// out of the same namespace as the inhibitor refusal.
	noSwap := dbus.Error{
		Name: "org.freedesktop.login1.SleepVerbNotSupported",
		Body: []any{"Sleep verb 'suspend' not supported"},
	}
	// Compared by what it says rather than with errors.Is: a dbus.Error holds a
	// slice, so it is not comparable, and errors.Is against one is a check that
	// can only ever answer false.
	got := Because(Suspend, State{Blocks: []Block{{What: "sleep", Who: "chromium"}}}, noSwap)
	if got.Error() != noSwap.Error() {
		t.Errorf("Because turned %q into %q", noSwap, got)
	}
	if strings.Contains(got.Error(), "chromium") {
		t.Errorf("a fault that is not a refusal was explained as %q", got)
	}

	// And polkit's other name for no, which zde does translate, so that the
	// line above is about the name and not about the type.
	denied := dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied", Body: []any{"Access denied"}}
	if got := Because(Suspend, State{}, denied); got.Error() == denied.Error() {
		t.Errorf("polkit's AccessDenied came back untranslated as %q", got)
	}

	plain := errors.New("connection closed by user")
	if got := Because(Suspend, State{}, plain); !errors.Is(got, plain) {
		t.Errorf("Because turned %v into %v", plain, got)
	}
	if got := Because(Suspend, State{}, nil); got != nil {
		t.Errorf("Because made an error out of nothing: %v", got)
	}
}

// logind only wants the second authorisation when the other session belongs to
// somebody else, so that is what the warning has to count. Counting sessions
// instead puts "somebody else is logged in" in front of anybody who has a tty
// open on their own machine, which is a warning about themselves.
func TestASecondSessionOfYourOwnIsNotSomebodyElse(t *testing.T) {
	mine := State{Sessions: []Session{
		{ID: "1", User: "me", Seat: "seat0", Mine: true},
		{ID: "3", User: "me"},
	}}
	if got := mine.Others(); len(got) != 0 {
		t.Errorf("Others() = %v on a machine where every session is mine", got)
	}

	shared := State{Sessions: []Session{
		{ID: "1", User: "me", Mine: true},
		{ID: "3", User: "ann"},
		{ID: "5", User: "ann"},
	}}
	got := shared.Others()
	if len(got) != 2 {
		t.Fatalf("Others() = %v, want ann's two sessions", got)
	}
	// Said once, though: two ttys belonging to one person are one person.
	if names := userNames(got); names != "ann" {
		t.Errorf("userNames = %q, want one person named once", names)
	}
}

// And a session and not merely a session. logind walks the sessions and takes
// only the classes SESSION_CLASS_IS_INHIBITOR_LIKE names - user, user-early,
// user-light, user-early-light (src/login/logind-session.h, systemd v258) -
// when it decides whether a reboot needs the second authorisation, so a display
// manager's greeter and a lingering user's manager session are not people at
// this machine.
//
// If this regresses, every machine with a display manager on it carries
// "gdm is logged in here as well" under the reboot row, every time - which is
// a warning that is a lie, and those are the ones people learn to press
// through.
func TestAGreeterOrABackgroundJobIsNotSomebodyLoggedIn(t *testing.T) {
	st := State{Sessions: []Session{
		{ID: "2", User: "me", Seat: "seat0", Class: "user", Mine: true},
		{ID: "c1", User: "gdm", Seat: "seat0", Class: "greeter"},
		{ID: "c2", User: "builder", Class: "background"},
		{ID: "c3", User: "builder", Class: "manager"},
		{ID: "c4", User: "me", Class: "lock-screen"},
	}}
	if got := st.Others(); len(got) != 0 {
		t.Errorf("Others() = %+v, and none of those is a person sitting at this machine", got)
	}

	// The four classes logind does count, and a row from something that did not
	// say - which counts too, because the safe direction here is to warn.
	for _, class := range []string{"user", "user-early", "user-light", "user-early-light", ""} {
		here := State{Sessions: []Session{
			{ID: "2", User: "me", Class: "user", Mine: true},
			{ID: "9", User: "ann", Class: class},
		}}
		if got := here.Others(); len(got) != 1 {
			t.Errorf("Others() = %+v with ann in a %q session, and logind counts that one", got, class)
		}
	}
}
