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
// somebody else's afternoon.
//
// Class is logind's own word for what kind of session it is - "user",
// "greeter", "background", "manager" and the rest of session_class_table
// (src/login/logind-session.c) - and it is the field that decides whether this
// one counts as another person being here at all (see Others). It is why the
// sessions are read with ListSessionsEx: the older ListSessions does not carry
// it (logind.go, State).
type Session struct {
	ID    string
	User  string
	Seat  string
	Class string
	Mine  bool
}

// counts is whether logind would treat this session as another person when it
// decides what a reboot needs.
//
// logind's own rule, copied rather than invented: have_multiple_sessions
// (src/login/logind-dbus.c, systemd v258) walks the sessions and takes only the
// ones SESSION_CLASS_IS_INHIBITOR_LIKE names - user, user-early, user-light,
// user-early-light (src/login/logind-session.h). A display manager's greeter
// waiting on another vt, the manager session systemd starts for a lingering
// user, and every background session are not people sitting at this machine,
// and logind does not authorise as though they were. Counting them would put
// "gdm is logged in here as well" under the reboot row of every machine with a
// display manager on it, which is the warning-that-is-a-lie Others exists to
// avoid.
//
// An empty class is a session from something that did not say, and it counts.
// That is the safe direction, the same one Others takes when nothing can say
// which session is this one.
func (s Session) counts() bool {
	switch s.Class {
	case "", "user", "user-early", "user-light", "user-early-light":
		return true
	}
	return false
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

// Others is the people who are logged in here besides you.
//
// Somebody else, and not merely somebody: logind counts a second session of
// your own as one of yours, and only another user's makes a reboot need the
// multiple-sessions authorisation. Getting that wrong in either direction
// produces a warning that is a lie - either it never appears when it matters,
// or it appears every time you open a second tty on your own machine.
//
// And a session and not merely a session: what logind counts is filtered by
// class (see Session.counts), so a greeter or a background job of another uid
// is not a person here.
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
		if s.Mine || !s.counts() || (mine != "" && s.User == mine) {
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
// machine that is the reason for it.
//
// Two of logind's refusals are translated and everything else is handed back
// exactly as logind said it: logind's own words about a verb it could not run
// are better than a summary of them, and a translation that swallowed one would
// be this surface lying about why nothing happened.
//
// The two are the ones that arrive naming nothing.
//
// A block inhibitor is the first, and it is not polkit's doing at all.
// verify_shutdown_creds (src/login/logind-dbus.c, systemd v258) reads the
// inhibitors itself, and a lock that is not block-weak ends the call there,
// before polkit is asked about the ignore-inhibit action, with
// BUS_ERROR_BLOCKED_BY_INHIBITOR_LOCK and the sentence "Operation denied due to
// active block inhibitor". That sentence names neither the program holding the
// lock nor the reason it gave, and both of those are on this side already from
// ListInhibitors - so this is the one place they can be put in front of the
// person about to wonder why nothing happened.
//
// v257 short-circuits in the same place and says it differently: plain
// SD_BUS_ERROR_ACCESS_DENIED, "Access denied due to active block inhibitor".
// That is why an access-denied with something holding this verb is read as the
// inhibitor rather than as polkit - on v257 it is the only thing it can mean,
// and where both are true at once the inhibitor is the half somebody can go and
// close. v256 and earlier did ask polkit, which is where the older belief that
// this arrives as an authorisation problem comes from.
//
// polkit is the second. zde asks non-interactively (see logind.go,
// shutdownOrSleep) and a zde session has no polkit agent, so a refusal comes
// back as a bare "interactive authorization required" with nothing in it about
// which of the machine's reasons it was.
//
// It is worth knowing how rare that second one is on an ordinary desktop, since
// it is easy to write a check for it that ends somebody's afternoon instead
// (docs/verify.md, section 6). systemd's own policy gives allow_active "yes" to
// reboot, power-off and suspend and to each of their -multiple-sessions
// variants (/usr/share/polkit-1/actions/org.freedesktop.login1.policy, systemd
// 258), so the session in front of the screen is allowed all of those outright,
// other people logged in or not. What is refused is a session polkit does not
// call active - a second one on another vt while somebody else's is in front,
// one with no seat - where allow_inactive and allow_any are auth_admin_keep; a
// halt, which is auth_admin_keep even for the active session; and any machine
// whose administrator has written rules of their own.
func Because(w What, st State, err error) error {
	switch {
	case err == nil:
		return nil
	case blockedByInhibitor(err), accessDenied(err) && len(st.Blocking(w)) > 0:
		return heldOff(w, st)
	case needsAdmin(err):
		return noAuthority(w, st)
	}
	return err
}

// heldOff is logind refusing on account of an inhibitor, with the inhibitor
// named.
func heldOff(w What, st State) error {
	held := st.Blocking(w)
	if len(held) == 0 {
		// logind read its inhibitors and this side could not, or the lock was
		// taken in the moment between the two reads. Still said as the refusal
		// it is rather than as the D-Bus name it arrived as.
		return fmt.Errorf("something on this machine is holding a %s off, and logind "+
			"will not take one while it does", w)
	}
	b := held[0]
	// The first one, and however many others there are: naming one thing that
	// can be closed beats a list, and a count says the list is not finished.
	// Its own words for the reason, because "Playing audio" is what makes the
	// browser worth going back to rather than killing.
	return fmt.Errorf("%s is holding this off%s%s, and logind will not take a %s while it does",
		blockWho(b), because(b.Why), andMore(len(held)-1), w)
}

// noAuthority is polkit refusing, with whatever on this machine is the reason
// polkit was asked the harder question.
func noAuthority(w What, st State) error {
	if others := st.Others(); len(others) > 0 {
		return fmt.Errorf("%s is logged in here as well, and logind wants an administrator's "+
			"password before it will take a %s from this session - which nothing here can ask for",
			userNames(others), w)
	}
	// Refused, with nothing on this machine to point at.
	return fmt.Errorf("logind would not take a %s from this session: it wants an administrator's "+
		"password, and nothing here can ask for one", w)
}

// needsAdmin is polkit saying no, in the two shapes it says it.
//
// Told apart from every other error because they mean opposite things: these
// are logind answering, and anything else is a bus that is unwell or a verb
// this machine cannot do - which needs its own words and not these.
func needsAdmin(err error) bool {
	return errorName(err) == "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired" ||
		accessDenied(err)
}

// accessDenied is polkit's outright no, and on systemd v257 it is also how a
// held block inhibitor came back (see Because).
func accessDenied(err error) bool {
	return errorName(err) == "org.freedesktop.DBus.Error.AccessDenied"
}

// blockedByInhibitorLock is systemd's BUS_ERROR_BLOCKED_BY_INHIBITOR_LOCK,
// which is what a held block lock produces on v258 instead of anything from
// polkit. It is in the systemd-logind binary on this machine, which is how it
// was checked rather than remembered.
const blockedByInhibitorLock = "org.freedesktop.login1.BlockedByInhibitorLock"

func blockedByInhibitor(err error) bool {
	return errorName(err) == blockedByInhibitorLock
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
