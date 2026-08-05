// Package power is what ends a session or a machine (docs/model.md, section 6:
// system.power).
//
// logind over the system bus, and not `systemctl suspend` or `loginctl` behind
// an exec. Three reasons, in the order they matter.
//
// The refusal is the point of the surface. A power menu that reports success
// while nothing happened is worse than one that says it was refused, and logind
// refuses on the wire with a name that can be read - polkit would not authorise
// it, something is holding sleep - where a spawned tool answers with an exit
// status and a line of English on a stderr no keypress has.
//
// The same bus answers the two questions a confirmation has to ask before
// anything happens: who else is logged in, and what is holding this off. A tool
// that only performs cannot be asked either of them.
//
// And the bus library is already vendored and already in this daemon
// (internal/link speaks NetworkManager on it, internal/bt BlueZ), so none of
// this costs a dependency or a program on the host.
//
// What is not here: the screen lock. Locking is running the program this
// machine calls its locker, which is a launch and not a logind verb, and it
// goes through the one table `zde system lock` already resolves it with
// (internal/zded, powerRun).
package power

import (
	"errors"
	"fmt"
	"strings"
)

// What is one thing logind can be asked for. Named as the surface names them,
// not as logind's methods are spelled, because these travel to a shell and back
// as strings and the vocabulary a person reads should be one vocabulary.
type What string

const (
	// Logout ends this session and nothing else on the machine.
	Logout What = "logout"
	// Suspend is sleep, which is the one here that keeps the session.
	Suspend What = "suspend"
	Reboot  What = "reboot"
	// PowerOff is off. logind spells the method PowerOff; the row says it in
	// two words and the wire says it in one, so that a name can be typed at
	// `zde system power` without quoting.
	PowerOff What = "poweroff"
)

// Session is one login session on this machine.
//
// Mine is this one, which is the difference between a log out and ending
// somebody else's afternoon - and the count of the ones that are not mine is
// what decides whether logind will take a reboot from an ordinary user at all
// (see Because).
type Session struct {
	ID   string
	User string
	Seat string
	Mine bool
}

// Block is one block inhibitor: something that told logind not to let a power
// action happen while it is running. What is logind's own colon-separated list
// ("sleep:shutdown"), Who the program, Why the sentence it gave.
//
// Delay inhibitors are deliberately not here. One of those postpones a suspend
// by a few seconds so something can save its work, which is not a thing worth
// putting in front of a person about to press a key; a block is, because it is
// the reason the key is about to be refused.
type Block struct {
	What string
	Who  string
	Why  string
}

// Stands reports whether this inhibitor stands in the way of that verb.
//
// The reading of logind's field belongs here because the vocabulary is
// logind's: "sleep" covers suspend and hibernate, "shutdown" covers a reboot
// and a power off, and both can be in the one field at once. A log out is
// inhibited by neither - logind has no inhibitor for ending a session - so
// nothing ever stands in its way, which is worth knowing before somebody adds
// a warning that could never fire.
func (b Block) Stands(w What) bool {
	want := ""
	switch w {
	case Suspend:
		want = "sleep"
	case Reboot, PowerOff:
		want = "shutdown"
	default:
		return false
	}
	for _, part := range strings.Split(b.What, ":") {
		if part == want {
			return true
		}
	}
	return false
}

// State is what logind says about the machine before anything is asked of it.
type State struct {
	Sessions []Session
	Blocks   []Block
}

// Others is the sessions that belong to somebody else.
//
// Somebody else, and not merely somebody: logind counts a second session of
// your own as one of yours, and only another user's makes a reboot need the
// multiple-sessions authorisation. Getting that wrong in either direction
// produces a warning that is a lie - either it never appears when it matters,
// or it appears every time you open a second tty on your own machine.
//
// When nothing says which session is this one - logind can be unable to say,
// and myID answers empty rather than guessing - every session counts as
// somebody's. That is the safe direction: it can name you to yourself before a
// reboot, where the other way round it would say a machine is empty while
// somebody is working on it.
func (st State) Others() []Session {
	var out []Session
	mine := ""
	for _, s := range st.Sessions {
		if s.Mine {
			mine = s.User
			break
		}
	}
	for _, s := range st.Sessions {
		if s.Mine || (mine != "" && s.User == mine) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Blocking is what stands in the way of one verb right now.
func (st State) Blocking(w What) []Block {
	var out []Block
	for _, b := range st.Blocks {
		if b.Stands(w) {
			out = append(out, b)
		}
	}
	return out
}

// Manager is the little of logind that zde needs. An interface for the reason
// link.Manager is one: the daemon has to be testable without a system bus, and
// on a machine where the thing behind it is allowed to be missing.
type Manager interface {
	// State is who is logged in and what is holding a power action off.
	State() (State, error)
	// Do performs one. It answers when logind has taken the request, which for
	// a reboot or a power off is before the machine is gone - so a refusal is
	// something the caller can still show somebody.
	Do(w What) error
}

// ErrNoLogind is a machine with no logind on its system bus: no bus at all, or
// nobody owning the name. A state and not a fault, the way a machine with no
// NetworkManager is (internal/link, ErrNoManager) - the menu still opens, the
// lock still locks, and the rows that need logind say why they will not work.
var ErrNoLogind = errors.New("no logind on this machine")

// Because is a refusal in words somebody can act on, with the thing on this
// machine that is the likely reason for it.
//
// Only polkit's two refusals are translated, and everything else is handed
// back exactly as logind said it: logind's own words about a verb it could not
// run are better than a summary of them, and a translation that swallowed one
// would be this surface lying about why nothing happened.
//
// Those two are worth it because they are how an ordinary machine doing
// nothing wrong refuses. polkit's defaults ask for an administrator's password
// when something holds a block inhibitor, and again when another user is logged
// in; zde asks non-interactively (see logind.go, do) and a zde session has no
// polkit agent, so what comes back is a bare "interactive authorization
// required" - which tells the person nothing about the browser holding sleep or
// the second person on the machine. This says which it was.
func Because(w What, st State, err error) error {
	if err == nil || !needsAdmin(err) {
		return err
	}
	if held := st.Blocking(w); len(held) > 0 {
		b := held[0]
		// The first one, and however many others there are: naming one thing
		// that can be closed beats a list, and a count says the list is not
		// finished. Its own words for the reason, because "Playing audio" is
		// what makes the browser worth going back to rather than killing.
		return fmt.Errorf("%s is holding this off%s%s, and logind will not take a %s past it "+
			"without an administrator's password that nothing here can ask for",
			blockWho(b), because(b.Why), andMore(len(held)-1), w)
	}
	if others := st.Others(); len(others) > 0 {
		return fmt.Errorf("%s is logged in here as well, and logind will not take a %s from "+
			"a second person's session without an administrator's password that nothing here can ask for",
			userNames(others), w)
	}
	// Refused, with nothing on this machine to point at. Said as the refusal it
	// is rather than as the D-Bus name it arrived as.
	return fmt.Errorf("logind would not take a %s from this session: it wants an administrator's "+
		"password, and nothing here can ask for one", w)
}

// needsAdmin is polkit saying no, in the two shapes it says it.
//
// Told apart from every other error because they mean opposite things: these
// are logind answering, and anything else is a bus that is unwell or a verb
// this machine cannot do - which needs its own words and not these.
func needsAdmin(err error) bool {
	name := errorName(err)
	return name == "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired" ||
		name == "org.freedesktop.DBus.Error.AccessDenied"
}

func blockWho(b Block) string {
	if b.Who == "" {
		return "something on this machine"
	}
	return b.Who
}

func because(why string) string {
	if why == "" {
		return ""
	}
	return ": " + why
}

func andMore(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return " (and one other)"
	}
	return fmt.Sprintf(" (and %d others)", n)
}

// userNames is who else is here, each named once: two ttys belonging to one
// person are one person, and saying their name twice reads as two people.
func userNames(sessions []Session) string {
	seen := map[string]bool{}
	var names []string
	for _, s := range sessions {
		name := s.User
		if name == "" {
			name = "session " + s.ID
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
