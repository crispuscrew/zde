package power

import (
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// polkitRefusal is how an ordinary machine says no: logind asks polkit, polkit
// wants an administrator's password, and the call was made non-interactively
// because nothing in a zde session can put a password prompt on the screen.
func polkitRefusal() error {
	return dbus.Error{
		Name: "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired",
		Body: []any{"Interactive authentication required."},
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

// The refusal is the whole point of this surface. polkit answers "interactive
// authentication required", which tells a person nothing about the browser
// holding sleep or the second person logged in - so the words have to name what
// on this machine is the reason.
//
// If this regresses, a suspend that never happens is explained by a D-Bus error
// name, and the thing to close is not on the screen.
func TestARefusalNamesWhatIsHoldingItRatherThanTheDBusError(t *testing.T) {
	held := State{Blocks: []Block{{What: "sleep", Who: "chromium", Why: "Playing audio"}}}
	err := Because(Suspend, held, polkitRefusal())
	if err == nil {
		t.Fatal("a refused suspend came back as no error at all")
	}
	for _, want := range []string{"chromium", "Playing audio"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is %q, and it has to name %q", err, want)
		}
	}

	// The same machine, a reboot: nothing is holding shutdown, so the reason is
	// the other person - and naming the browser here would send somebody to
	// close a tab that was never in the way.
	crowded := State{
		Sessions: []Session{{ID: "1", User: "me", Mine: true}, {ID: "3", User: "ann"}},
		Blocks:   held.Blocks,
	}
	err = Because(Reboot, crowded, polkitRefusal())
	if !strings.Contains(err.Error(), "ann") {
		t.Errorf("refusal is %q, and it has to say who else is logged in", err)
	}
	if strings.Contains(err.Error(), "chromium") {
		t.Errorf("refusal is %q, and a sleep inhibitor is not why a reboot was refused", err)
	}

	// And with nothing on this machine to point at, it is still said as a
	// refusal rather than as a bus error.
	err = Because(PowerOff, State{}, polkitRefusal())
	if !strings.Contains(err.Error(), "administrator") {
		t.Errorf("refusal is %q, want the reason it cannot be answered from here", err)
	}
}

// Everything that is not polkit saying no is logind's own account of what went
// wrong, and it is better than any summary of it: a machine with no swap
// refuses a hibernate in its own words, and a bus that has gone says so. If
// this regresses, a real fault is reported as an authorisation problem and
// somebody goes looking for a password.
func TestAnythingThatIsNotPolkitKeepsLogindsOwnWords(t *testing.T) {
	boom := errors.New("Sleep verb \"suspend\" not supported")
	if got := Because(Suspend, State{}, boom); !errors.Is(got, boom) {
		t.Errorf("Because turned %v into %v", boom, got)
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
