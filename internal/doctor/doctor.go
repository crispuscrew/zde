// Package doctor is the one screen that says why a session is unwell, on the
// machine it is unwell on and with nothing else to look anything up on
// (docs/install.md, when it breaks).
//
// It is a package rather than a lump in the CLI because of where the mistakes
// live: not in dialling a socket or stat-ing a file, but in what a reading
// means - which readings are a broken machine, which are a machine that is
// merely young, and which are nothing at all. So Gather does the asking and
// Judge does the deciding, and Judge is a pure function of a struct: it can be
// run against sessions nobody has ever had, which is the only way the answers
// for a locker that cannot authenticate or a bus somebody else took are ever
// exercised.
package doctor

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/crispuscrew/zde/internal/attn"
)

// Level is how much of a person's evening one line costs. Three rather than
// two, because the question is not "is this true" but "do I have to do
// something about it": a warning is a session somebody can still work in, a
// failure is one that is missing something it was promised.
type Level string

const (
	OK   Level = "ok"
	Warn Level = "warn"
	Fail Level = "fail"
)

// Check is one line. Detail says the consequence and not only the reading
// wherever there is one to say: somebody running this is trying to find out
// what is broken, not to collect facts.
type Check struct {
	Level  Level
	Name   string
	Detail string
}

// String is the line as it is printed. Fixed columns rather than the tabs the
// queue uses: this goes into a bug report to be read by people, and the level
// is the column an eye runs down. Nothing generated reads it, so nothing is
// owed a separator.
func (c Check) String() string {
	return fmt.Sprintf("%-5s %-13s %s", c.Level, c.Name, c.Detail)
}

// Report is the whole screen, in the order the checks are printed.
type Report []Check

// Failed is how many checks failed, which is what the exit status is made of.
// Warnings deliberately do not count: every machine has some today (there is
// no layer 2 yet), and a command that always exits non-zero is one nobody
// reads the output of.
func (r Report) Failed() int {
	n := 0
	for _, c := range r {
		if c.Level == Fail {
			n++
		}
	}
	return n
}

func (r Report) String() string {
	var b strings.Builder
	for _, c := range r {
		b.WriteString(c.String())
		b.WriteByte('\n')
	}
	return b.String()
}

// noDaemon is what every check only zded can answer says when it is not
// answering. Said rather than left out: a line that vanishes is one somebody
// has to notice is missing, and the first line of the report has already given
// the reason.
const noDaemon = "not known: zded is not answering"

// Run is Gather then Judge: the whole command, for a caller with no reason to
// hold the facts in between.
func Run() Report { return Judge(Gather()) }

// Judge turns what was gathered into the lines a person reads. The order is
// the order of the report, and it is roughly the order the things depend on
// each other: the daemon, what it can see, what it owns, what will run apps,
// what starts it, and what it reads off the disk.
func Judge(s Session) Report {
	r := Report{
		daemon(s),
		compositor(s),
		shell(s),
		notify(s),
		layer2(s),
		podman(s),
	}
	r = append(r, units(s)...)
	r = append(r, manifests(s)...)
	r = append(r, deskApps(s)...)
	r = append(r, locker(s))
	r = append(r, logind(s)...)
	r = append(r, journal(s))
	return r
}

// daemon is the first line because everything under it is zded's answer: a
// report whose first line is a failure explains most of the blanks below it.
func daemon(s Session) Check {
	switch {
	case s.DialErr != nil:
		return Check{Fail, "zded", s.DialErr.Error()}
	case s.Status == nil:
		// Gather never produces this, and a panic here would be the one thing
		// worse than a session nobody can explain: it would take away the
		// command that was going to explain it.
		return Check{Fail, "zded", "no answer, and no reason given"}
	}
	return Check{OK, "zded", s.Status.Version + ", socket at " + s.Socket}
}

// compositor is a failure and not a warning: zded answering without niri is a
// session where every desk key is silent, which is the state this whole
// command exists to name.
func compositor(s Session) Check {
	switch {
	case s.Status == nil:
		return Check{Warn, "compositor", noDaemon}
	case s.Status.Compositor == "connected":
		return Check{OK, "compositor", "connected"}
	}
	return Check{Fail, "compositor", s.Status.Compositor}
}

// shell is a warning, because a session with no shell still works: the keys
// that would draw a surface print their answer instead (cmd/zde, switcher).
// What it costs is worth spelling out, since the symptom people arrive with is
// "Mod+Tab did something odd" rather than "no shell".
func shell(s Session) Check {
	switch {
	case s.Status == nil:
		return Check{Warn, "shell", noDaemon}
	case s.Status.Shell:
		return Check{OK, "shell", "listening"}
	}
	return Check{Warn, "shell", "not listening: Mod+Tab prints a list instead of a picker"}
}

// notify is about one name on the session bus. There is exactly one holder of
// it (internal/attn, Serve), and whoever holds it gets every notification this
// session receives - so a session where it is not zded's is one where things
// arrive somewhere nobody is looking, which is a failure however healthy the
// rest of the machine is.
func notify(s Session) Check {
	level := Fail
	switch {
	case s.Status == nil:
		// Without zded's own answer, who owns the name is a reading rather
		// than a verdict: it may well be a zded this doctor cannot reach.
		level = Warn
	case s.Status.Notifications:
		return Check{OK, "notify", attn.BusName + " is zded's"}
	}
	switch {
	case s.NotifyErr != nil:
		return Check{level, "notify", "could not ask who owns " + attn.BusName + ": " + s.NotifyErr.Error()}
	case s.Notify == "":
		return Check{level, "notify", "nothing owns " + attn.BusName + ", so notifications go nowhere"}
	}
	return Check{level, "notify", attn.BusName + " is owned by " + s.Notify}
}

// layer2 is whether zcr is there at all (docs/delivery.md). It is here because
// that is precisely the thing somebody will not believe when they read that
// their editor is unsandboxed - and it is what the desk apps check below turns
// on, since without zcr the only resolver left answers about a different
// namespace.
//
// Named for the layer rather than for the line it prints, because this package
// asks zinc things now and a function called zinc would shadow the package that
// answers them.
func layer2(s Session) Check {
	switch {
	case s.Status == nil:
		return Check{Warn, "zinc", noDaemon}
	case s.Status.Zinc:
		return Check{OK, "zinc", "zcr is on the session's PATH"}
	}
	return Check{Warn, "zinc", "no zcr on the session's PATH: layer 2 is not here, so apps run on the host"}
}

// podman is what zinc will run every app inside, and rootless is the whole
// point of it (docs/delivery.md, layer 0). Broken subuid ranges are the
// ordinary way this fails and they fail nothing else on the machine, so
// without a check like this one it is discovered by the first app that will
// not start.
func podman(s Session) Check {
	switch {
	case errors.Is(s.Podman.Err, exec.ErrNotFound):
		return Check{Warn, "podman", "not installed, so layer 2 has nothing to run an app in"}
	case s.Podman.Err != nil:
		return Check{Warn, "podman", s.Podman.Err.Error()}
	case !s.Podman.Rootless:
		return Check{Warn, "podman", "answering as root rather than rootless: an app would run with this machine's root"}
	}
	return Check{OK, "podman", "rootless"}
}

// units are what a login starts. A unit that is not active is a warning and
// never a failure: a daemon started by hand answers just as well, which is
// what the tests and anybody debugging this do, so the state is context for
// the lines above rather than a verdict of its own.
func units(s Session) []Check {
	out := make([]Check, 0, len(s.Units))
	for _, u := range s.Units {
		switch {
		case u.Err != nil:
			out = append(out, Check{Warn, "unit", u.Name + ": " + u.Err.Error()})
		case u.State == "active":
			out = append(out, Check{OK, "unit", u.Name + " active"})
		default:
			out = append(out, Check{Warn, "unit", u.Name + " " + u.State})
		}
	}
	return out
}

// manifests is one line per desk that could not be read, because that is the
// answer to "where did my desk go" and a count would send somebody through the
// directory looking for which one.
func manifests(s Session) []Check {
	if s.Status == nil {
		return []Check{{Warn, "manifests", noDaemon}}
	}
	if len(s.Status.BadManifests) == 0 {
		return []Check{{OK, "manifests", "every manifest zded has read parsed"}}
	}
	out := make([]Check, 0, len(s.Status.BadManifests))
	for _, bad := range s.Status.BadManifests {
		out = append(out, Check{Fail, "manifests", bad})
	}
	return out
}

// deskApps is one line per app a desk declares that this machine has nothing to
// run under that name. It is the failure that used to be silent: entering a
// desk starts what its manifest declares, behind the switch, and a name nothing
// could resolve cost one line in zded's log and nothing anybody saw
// (docs/roadmap.md, 0.1).
//
// One line per desk and app, because that is the shape of the fix: the desk to
// open and the name in it to correct or define. A count would send somebody
// through the directory looking for which one, which is the same reason the
// manifests above get a line each.
//
// Warnings, never failures. Layer 2 is provisioned by hand on every machine
// there is (docs/delivery.md), so a machine whose desks name apps nobody has
// defined yet is the ordinary young machine and not a broken one - and a
// command that exits non-zero everywhere is one nobody reads the output of.
func deskApps(s Session) []Check {
	d := s.Desks
	switch {
	case d.Err != nil:
		return []Check{{Warn, "desk apps", "not known: " + d.Err.Error() + d.askedOf()}}
	case len(d.Unrunnable) == 0:
		// The directory is named on this line alone, and that is deliberate: it
		// is the reading that can quietly be about the wrong place, since zded
		// can be started with another one (cmd/zded, -desks).
		return []Check{{OK, "desk apps", "no desk in " + d.Dir + " names an app this machine cannot start" + d.askedOf()}}
	case d.resolver() == byApps && !d.Configured:
		// A machine with nothing in zde.apps gets one line and not one per name.
		// The reason is the same sentence every time and the fix is a single
		// edit, so a line each would bury the rest of the report under one fact
		// - which is the wall of noise this check exists to replace.
		//
		// Only for that resolver. A zinc app is a YAML file of its own in a
		// store (docs/delivery.md, layer 2), so there is no one edit to name and
		// a line each is exactly right: they are that many things to write.
		return []Check{{Warn, "desk apps", d.Unrunnable[0].Err.Error() + ", and the desks name " +
			strings.Join(wanted(d.Unrunnable), ", ") + d.askedOf()}}
	}
	out := make([]Check, 0, len(d.Unrunnable))
	for _, a := range d.Unrunnable {
		out = append(out, Check{Warn, "desk apps", a.Desk + " names " + a.App + ": " + a.Err.Error() + d.askedOf()})
	}
	return out
}

// resolver is which of the two answered, and what a Session written down
// without one would have used. Judge is a pure function of a struct anybody can
// write by hand (this package's doc), so the zero value has to mean the same
// thing the machine it describes would have done.
func (d Desks) resolver() string {
	if d.Resolver == byZcr {
		return byZcr
	}
	return byApps
}

// askedOf is the tail every line of that check carries.
//
// It is on the line and not only in the code because a warning nobody can see
// the basis of is one people learn to ignore, and these two resolvers do not
// answer the same question: zde.apps is the keymap's own logical-name-to-argv
// map (internal/apps), and a manifest's `app:` is a zinc app name, which is
// what `zcr run <app>@<instance>` takes (internal/manifest, App). On a machine
// that keeps its apps in zinc, a verdict from zde.apps is about the wrong
// namespace entirely - and saying which one answered is what lets somebody
// reading a warning they disagree with find out why in one line rather than in
// the source.
func (d Desks) askedOf() string {
	if d.resolver() == byZcr {
		return " - asked of " + byZcr
	}
	return " - asked of " + byApps + ", since no " + byZcr + " is on PATH"
}

// logind is whether the four verbs a power menu is made of would work on this
// machine: a log out, a suspend, a reboot and a power off, which are
// TerminateSession, Suspend, Reboot and PowerOff on
// org.freedesktop.login1.Manager.
//
// Warnings, never failures. A session with no logind is a session somebody can
// still work in - it locks, and every key but one still does what it did - and
// doctor's exit status has to keep meaning "this machine is missing something
// it was promised". A container has no logind and is not broken.
//
// One line when all four would work, and one per thing that would not
// otherwise. That is the shape the rest of this report uses and for the same
// reason: what fixes one of these is per verb - a polkit rule, an inhibitor to
// go and stop - and a count would send somebody looking for which.
func logind(s Session) []Check {
	l := s.Power
	if l.Err != nil {
		// Said as the consequence and not only as the reading. "no system bus"
		// is a fact; a machine that cannot be told to go is what somebody is
		// standing in front of.
		return []Check{{Warn, "logind", l.Err.Error() +
			", so nothing can log out, suspend, reboot or power off this machine: a power menu, where there is one, could only lock"}}
	}
	var out []Check
	for _, c := range l.Can {
		switch {
		case c.Err != nil:
			out = append(out, Check{Warn, "logind", "could not ask logind whether this session may " + c.What + ": " + c.Err.Error()})
		case c.Answer == "yes":
			// Nothing. A verb that works has nothing to say, and a report where
			// every line is a warning is one nobody reads to the end.
		case c.Answer == "challenge":
			// polkit would want an authentication first, and a zde session has
			// no agent to put that question to anybody with. Whatever asks has
			// to ask non-interactively or else wait for a dialog nobody will
			// ever see, so this is a refusal however it is worded.
			out = append(out, Check{Warn, "logind", c.What + " would be refused: polkit answers " + strconv.Quote(c.Answer) +
				", and this session has no authentication agent to answer it with - security.polkit.extraConfig is where a rule goes"})
		case c.Answer == "na":
			out = append(out, Check{Warn, "logind", c.What + " is not available on this machine: logind answers " + strconv.Quote(c.Answer)})
		default:
			out = append(out, Check{Warn, "logind", c.What + " would be refused: logind answers " + strconv.Quote(c.Answer)})
		}
	}
	switch {
	case l.SessionErr != nil:
		out = append(out, Check{Warn, "logind", "could not ask which session this is, so a log out has nothing to end: " + l.SessionErr.Error()})
	case l.Session == "":
		// It refuses rather than guessing, which is the right way round and
		// still a key that does nothing. Worth saying before somebody presses
		// it: the two places the answer comes from are the ones to go and look
		// at (docs/verify.md).
		out = append(out, Check{Warn, "logind", "logind names no session for this user, so a log out would refuse " +
			"rather than end somebody else's - loginctl session-status says what it can see"})
	}
	if len(out) == 0 {
		// The aside is there because this check landed before the thing that
		// uses it, and a line about four verbs nothing presses would otherwise
		// read as a check about nothing.
		return []Check{{OK, "logind", "session " + l.Session + " is what a log out would end, and suspend, " +
			"reboot and power off are this session's to use - what a power menu asks for, on a build that has one"}}
	}
	return out
}

// wanted is every name the desks asked for, once each and in order: what a
// machine with nothing configured would have to define.
func wanted(unrunnable []DeskApp) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range unrunnable {
		if seen[a.App] {
			continue
		}
		seen[a.App] = true
		out = append(out, a.App)
	}
	sort.Strings(out)
	return out
}

// locker is the check this command is most worth having for. A locker with no
// PAM service takes the screen and then refuses every password: that is not a
// locked machine, it is a lost one, and nothing else on the machine looks
// wrong until the moment it happens.
func locker(s Session) Check {
	l := s.Lock
	switch {
	case l.Err != nil:
		return Check{Fail, "locker", "the apps file could not be read, so nothing resolves: " + l.Err.Error()}
	case !l.Configured:
		return Check{Warn, "locker", "nothing is configured to lock the screen, so Mod+Ctrl+semicolon does nothing"}
	case l.BinErr != nil:
		return Check{Fail, "locker", l.Argv0 + " is configured to lock the screen and is not there"}
	case l.PAM == PAMAbsent:
		return Check{Fail, "locker", l.Argv0 + " has no " + l.ServicePath() +
			": it would take the screen and refuse every password"}
	case l.PAM == PAMUnknown:
		return Check{Warn, "locker", l.Service + " is configured, and this machine has no " + pamDir +
			" to say whether it could authenticate"}
	}
	return Check{OK, "locker", l.Service + ", authenticating through " + l.ServicePath()}
}

// journal is a warning: entries that could not be read are the queue's own
// history, and a session with a few unreadable ones still runs (see
// internal/journal - a bad line is skipped rather than fatal, and this is the
// count that stops that being silent).
func journal(s Session) Check {
	switch {
	case s.Status == nil:
		return Check{Warn, "journal", noDaemon}
	case s.Status.Skipped > 0:
		return Check{Warn, "journal", fmt.Sprintf("%d entries could not be read", s.Status.Skipped)}
	}
	return Check{OK, "journal", "every entry read"}
}
