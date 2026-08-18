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

// Check is one check's answer: one row of the report, and more than one line of
// it where the detail has more than one line in it (see String). Detail says
// the consequence and not only the reading wherever there is one to say:
// somebody running this is trying to find out what is broken, not to collect
// facts.
type Check struct {
	Level  Level
	Name   string
	Detail string
}

// The columns a row is made of: the level five wide, the name thirteen, and a
// space after each. detailAt is where the detail starts.
//
// Three names for what used to be two numbers inside a format string, because
// the width of the columns and the indent under them are now one fact, and a
// fact written twice is the copy that drifts. A continuation line indented to
// anything else is a line that reads as neither this row nor a new one (see
// String).
const (
	levelAt  = 5
	nameAt   = 13
	detailAt = levelAt + 1 + nameAt + 1
)

// detailMax is how much of one detail is printed. Past it the line is cut, and
// it says it was cut (see cut), which is the whole of the difference between
// this bound and the one it replaced.
//
// Two thousand characters, and the number is picked so that nothing this
// package writes can reach it. The longest sentence zde puts in this column is
// the idle check's caveat on a machine with no logind, 450 as this is written
// and held under the bound by a test of its own (see unseen); the
// widest row this package can construct is an idle holder's, at about 1370,
// which is both of a stranger's two strings at attn.Line's three hundred
// characters with every one of them a `"` that strconv.Quote escapes (see
// holder). Above both, so that a cut here is always somebody else's text and
// never zde's own qualification taken off the end of zde's own sentence, which
// is exactly what the bound before it did.
//
// What is under it is another program's complaint: podman's refusal, zcr's
// answer about an app name, a YAML parser on a manifest somebody edited by
// hand. Something a thousand characters past the widest thing zde writes here
// is a dump rather than a complaint, and the front of a dump is the part that
// names the file.
//
// It is not the only ceiling and it is the one that keeps the other from being
// spent on a single line. The state snapshot bounds the whole file at
// reportBytesMax and cuts what does not fit (report.go), so an unbounded detail
// would not merely be long: it would push the checks under it off the end of
// the file somebody carried away from a machine that will not boot.
const detailMax = 2000

// String is the row as it is printed. Fixed columns rather than the tabs the
// queue uses: this goes into a bug report to be read by people, and the level
// is the column an eye runs down. Nothing generated reads it, so nothing is
// owed a separator.
//
// The detail is filtered here, and it keeps the shape it was written in
// (internal/attn, Text rather than Line). Most of what is in this column was
// written by something else - systemctl's and podman's first line of complaint,
// zcr's refusal of an app name, a YAML parser's complaint about a manifest,
// logind's answer off the bus, an app name out of apps.json - and this report is
// read in a terminal and pasted into bug threads, where ESC is not a character
// but the start of an instruction. Text is what takes that away: out of all
// 1114112 runes it passes tab, newline and the zero-width joiner, and otherwise
// only what Go calls printable, so ESC, a carriage return and a NUL are gone
// before this returns. A tab survives and costs nothing here, because it can
// move text to the right and a row is told apart by what is on its left.
//
// Nearly all of that arrives on one line already, and this does not rely on it.
// A program's complaint is cut to its first line where it is run (gather.go,
// firstLine); a parser's several lines are folded where they enter the daemon
// (internal/zded, rememberProblems); a holder's two strings are folded and
// quoted (see holder). The one that is neither is an app name, which comes off
// apps.json and is whatever wrote that file - and that is where a newline has
// actually reached this column, which cmd/zde tests end to end against a real
// run. A filter here that leant on the folding upstream would be a filter that
// fails the first time one of those rules is relaxed.
//
// Line was the filter and it was the wrong one, which is worth keeping rather
// than deleting. Line makes a row: it folds a detail onto one line and cuts it
// at a queue row's three hundred characters, in silence. The longest line in
// this report is zde's own - the idle check's caveat about the half of the
// mechanism nothing here can see is 340 characters by itself (see unseen), on a
// line of 450 - so it stopped mid-word and lost the docs pointer at the end of
// it, and the one check whose whole purpose is to refuse to give an all-clear
// read as an all-clear whose qualification trailed off. A bound meant for
// foreign text had been pointed at zde's own sentence.
//
// So a newline is paid for by where it lands rather than by being taken away.
// Every line after the first is indented to detailAt, which is where the detail
// column starts: it reads as the rest of this check, and it cannot be read as
// another one, because a check's level is in column one and nothing twenty
// characters in is in column one. That is the same shape the snapshot's own
// rows are argued into (report.go, reading), reached from the other side: there
// no indent is safe, because every row in that file is already indented two,
// and here every indent is safe except the two attn.Block would have used.
//
// Which is why Block is not what this calls, though it is this filter with an
// indent on it already. Its two spaces are for an error printed on its own in
// column one (cmd/zde, complain), and two spaces under a twenty-character
// column is neither this row continued nor a new one. An indent is layout, and
// layout belongs to whoever owns the columns; the filtering is still attn's, and
// there is still one copy of it.
//
// A bound stays, because taking away a cut is not the same as taking away a
// ceiling: detailMax, cut the way this package cuts everything else, which is
// out loud (see cut, and lineMax for why a silent cut is the one a snapshot
// cannot afford).
//
// A detail with nothing printable in it is quoted rather than left blank, for
// the reason attn.Block quotes one: a check that says something is wrong and
// then says nothing about what is worse than a check that shows the bytes.
//
// Here rather than in each of the twenty places a Check is made, for the reason
// `zde` filters its errors in one place: a filter per call site is a filter the
// next check forgets.
func (c Check) String() string {
	detail := strings.TrimSpace(attn.Text(c.Detail))
	if detail == "" {
		detail = strconv.Quote(c.Detail)
	}
	detail = strings.ReplaceAll(cut(detail, detailMax), "\n", "\n"+strings.Repeat(" ", detailAt))
	return fmt.Sprintf("%-*s %-*s %s", levelAt, c.Level, nameAt, c.Name, detail)
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
	r = append(r, idle(s)...)
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
// their editor is unsandboxed.
//
// It is the daemon's answer about the daemon's PATH, which is what decides
// whether a desk switch can start anything. The desk apps check below asks the
// same question of this process instead (probeDesks), because it is made off
// the disk so that it still works on a session where zded is what is wrong -
// and the two can disagree: a zded started by systemd carries the session's
// PATH and this command carries the shell's. So this line does not stand for
// that one, which is why every line of it ends with the resolver that actually
// answered rather than pointing back here.
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
//
// Except where the readings are for somebody other than the owner, and there it
// is exactly a count: every entry starts with the manifest's path, a manifest's
// path is a desk's name, and a file that will not parse is one nothing can read
// a `private: true` out of - so there is no half of this list that is safe to
// name (see Session.BadHidden). The count is still a failure and still says the
// desks are gone, which is the part somebody has to act on.
func manifests(s Session) []Check {
	if s.Status == nil {
		return []Check{{Warn, "manifests", noDaemon}}
	}
	out := make([]Check, 0, len(s.Status.BadManifests)+1)
	for _, bad := range s.Status.BadManifests {
		out = append(out, Check{Fail, "manifests", bad})
	}
	if s.BadHidden > 0 {
		out = append(out, Check{Fail, "manifests", fmt.Sprintf(
			"%d manifest(s) zded read did not parse, so that many desks are not declared. They are not named here,"+
				" because a manifest is named by its path and a path is a desk name. `zde doctor` in a terminal names them",
			s.BadHidden)})
	}
	if len(out) == 0 {
		return []Check{{OK, "manifests", "every manifest zded has read parsed"}}
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
//
// Hidden is the entries that were never made, because the readings were
// gathered for somebody who is not this machine's owner (probeDesks). It gets a
// line of its own and the all-clear above it is withdrawn when there is one: an
// "ok" saying no desk names an app this machine cannot start, printed over the
// top of apps this machine cannot start, is the one failure mode a redaction
// must not have.
func deskApps(s Session) []Check {
	d := s.Desks
	switch {
	case d.Err != nil:
		return []Check{{Warn, "desk apps", "not known: " + d.Err.Error() + d.askedOf()}}
	case len(d.Unrunnable) == 0 && d.Hidden == 0:
		// The directory is named on this line alone, and that is deliberate: it
		// is the reading that can quietly be about the wrong place, since zded
		// can be started with another one (cmd/zded, -desks).
		return []Check{{OK, "desk apps", "no desk in " + d.Dir + " names an app this machine cannot start" + d.askedOf()}}
	case len(d.Unrunnable) == 0:
		return []Check{hiddenApps(d)}
	case d.resolver() == byApps && !d.Configured:
		// A machine with nothing in zde.apps gets one line and not one per name.
		// The reason is the same sentence every time and the fix is a single
		// edit, so a line each would bury the rest of the report under one fact
		// - which is the wall of noise this check exists to replace.
		//
		// Only for that resolver. A zinc app is a YAML file of its own in a
		// store (docs/delivery.md, layer 2), so there is no one edit to name and
		// a line each is exactly right: they are that many things to write.
		one := Check{Warn, "desk apps", d.Unrunnable[0].Err.Error() + ", and the desks name " +
			strings.Join(wanted(d.Unrunnable), ", ") + d.askedOf()}
		if d.Hidden > 0 {
			// The count still goes on a line of its own, because the names on
			// this one are the only ones there are: a reader who counted them
			// would count too few and think they had the whole list.
			return []Check{one, hiddenApps(d)}
		}
		return []Check{one}
	}
	out := make([]Check, 0, len(d.Unrunnable)+1)
	for _, a := range d.Unrunnable {
		out = append(out, Check{Warn, "desk apps", a.Desk + " names " + a.App + ": " + a.Err.Error() + d.askedOf()})
	}
	if d.Hidden > 0 {
		out = append(out, hiddenApps(d))
	}
	return out
}

// hiddenApps is the one line that stands for every entry a private desk cost
// this report, and it is the deliberate limit of the rule: a count, never a
// name. "Three apps on a desk you declared private could not be started" is a
// real fault somebody has to be told about without being told which - and a
// report that dropped those lines in silence would be a report whose all-clear
// is worth nothing.
func hiddenApps(d Desks) Check {
	return Check{Warn, "desk apps", fmt.Sprintf(
		"%d app(s) on desks that declare private could not be started, and neither those desks nor those apps"+
			" are named here. `zde doctor` in a terminal names them%s", d.Hidden, d.askedOf())}
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
	switch {
	case l.Absent:
		// Said as the consequence and not only as the reading. "nobody owns the
		// name" is a fact; a machine that cannot be told to go is what somebody
		// is standing in front of.
		return []Check{{Warn, "logind", "nothing owns " + logindName +
			", so nothing can log out, suspend, reboot or power off this machine: a power menu, where there is one, could only lock"}}
	case l.Err != nil:
		// The middle state every other check here has. A question that could not
		// be put has no answer, and the two-second bound above it means the
		// ordinary way to land here is a machine that is slow rather than one
		// that is missing something: telling somebody their machine cannot be
		// powered off, on the evidence of a dial that timed out, is doctor
		// inventing a fault. So it says what it could not do and hands over the
		// question by hand, the way the desk apps check does.
		return []Check{{Warn, "logind", "not known: " + l.Err.Error() +
			" - so whether this session may log out, suspend, reboot or power off was never asked, and `loginctl show-session` puts the same question by hand"}}
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

// unseen is the half of this question nothing on this machine can answer, said
// wherever the answer is drawn.
//
// It is spelled out rather than summarised because the summary is the mistake:
// "nothing is holding your screen awake" is what somebody will take away from a
// clean line here, and that is a claim about two mechanisms when only one of
// them was asked. The other one is the more likely of the two to be in use and
// the easier of the two for an app to reach.
const unseen = "a Wayland app holding zwp_idle_inhibit_manager_v1 never reaches logind and is invisible to " +
	"every interface zde has: niri hands that global to sandboxed clients unfiltered, honours it while the " +
	"surface is merely visible rather than focused, and exposes no way to read it back (docs/roadmap.md, the " +
	"idle inhibitor; docs/verify.md, section 11)"

// idle is whether anything is holding this session's idle timers off, which is
// whether a screen that would blank, suspend or lock on its own is going to.
//
// It is the check most at risk of being read as more than it is, so what it can
// see is on every line it prints. logind's inhibitor table is the whole of the
// source, and on a Wayland session that is the smaller half of the mechanism -
// see unseen, and internal/zded/idle.go for the measurement behind it.
//
// A warning and not a failure. A held idle timer is a session somebody can
// still work in, and on a machine with nothing configured to act on idle it
// costs nothing at all; what it is worth is that it be visible before somebody
// walks away from the machine rather than after.
//
// It is also the one check here whose readings are two strings a stranger wrote,
// so it is printed two ways. On the owner's own screen the holders are named,
// because the next thing that person does is go and stop one. In the state
// snapshot they are counted and not named, because `--who=` defaults to the
// holder's own command line and that file is written to be pasted into a bug
// report (gather.go, Logind.Named; report.go, reportHeader). Which of the two
// this is arrives on the reading rather than being decided here.
func idle(s Session) []Check {
	l := s.Power
	switch {
	case l.Absent:
		return []Check{{Warn, "idle", "nothing owns " + logindName +
			", so whether something is holding this session awake was never asked - and " + unseen}}
	case l.Err != nil:
		// Deliberately not "nothing is holding it". The logind check above has
		// already given the reason at length, so this one says only what it
		// means for this question and stops.
		return []Check{{Warn, "idle", "not known: logind could not be asked, so whether something is " +
			"holding this session awake is unanswered - and " + unseen}}
	case l.HoldsErr != nil:
		return []Check{{Warn, "idle", "logind would not list its inhibitors: " + l.HoldsErr.Error() +
			" - so this is unanswered, and `systemd-inhibit --list` puts the same question by hand"}}
	case len(l.Holds) == 0:
		// The line this whole check exists to word carefully. An empty table is
		// a true statement about logind and not about the machine.
		return []Check{{OK, "idle", "logind has nothing holding this session awake - though " + unseen}}
	}
	var out []Check
	if !l.Named {
		// The count, and never the words. This gather was not the owner's, so
		// the two strings on every row were not taken at all (gather.go,
		// Logind.Named) - and the count is still the whole of the finding, which
		// is why this is a warning of exactly the weight the named rows below
		// carry rather than a quieter line. A redaction that turned into an
		// all-clear would be the one failure this check must not have.
		//
		// Where the names are is said, because the answer exists and is one
		// command away on the machine itself. A file that talked somebody out of
		// looking further would be worse than no file.
		out = append(out, Check{Warn, "idle", fmt.Sprintf(
			"%d thing(s) logind names are holding this session awake, and nothing here says which: "+
				"an inhibitor's name is the command line of whatever took it and its reason is that "+
				"program's own prose, so neither is written into a file meant to leave this machine - "+
				"`systemd-inhibit --list`, or `zde doctor` on the machine itself, is where the names "+
				"are. Nothing that acts on this session going idle will fire until they let go",
			len(l.Holds))})
		return append(out, Check{Warn, "idle", "and there may be more than logind can see: " + unseen})
	}
	for i, h := range l.Holds {
		if i == holdsShown {
			// The rest are counted rather than printed. Nothing is claimed to
			// have been read: the number is logind's own and it is the number a
			// person needs to know they are looking at a table somebody filled
			// rather than at their machine (see holdsShown).
			out = append(out, Check{Warn, "idle", fmt.Sprintf(
				"and %d more holders logind named, not printed: a table this long is one "+
					"somebody filled, and `systemd-inhibit --list` is where the whole of it is",
				len(l.Holds)-holdsShown)})
			break
		}
		out = append(out, Check{Warn, "idle", holder(h) +
			" - so nothing that acts on this session going idle will fire until it lets go"})
	}
	// Last, because it qualifies the list above rather than any one row of it:
	// what is named is what logind knows, and there is no way to find out
	// whether anything else is holding the screen as well.
	out = append(out, Check{Warn, "idle", "and there may be more than logind can see: " + unseen})
	return out
}

// holdsShown is how many holders the report prints by name.
//
// There is a bound because the number of rows is a stranger's to choose.
// `systemd-inhibit --what=idle --who=... --why=...` takes both strings from
// whoever runs it, every local account can run it, and logind's own ceiling is
// InhibitorsMax, which defaults to 8192. Each row here is two strings at
// attn.Line's 300 characters, so an unbounded loop is up to 8192 lines and
// several megabytes of somebody else's prose in a report that is read by people
// and pasted into bug threads - and, where the report is written to a file with
// a ceiling on it, several megabytes that push the checks underneath this one
// off the end.
//
// Six, because the question this check answers is "is something holding the
// screen", and six holders answer it as well as eight thousand do. A machine
// that is not being played with holds nought or one: a download, a video call,
// a backup wrapped in `systemd-inhibit`. Six is more than anybody has and few
// enough that the caveat under them is still on the same screen.
//
// The count above the sample is what keeps it honest. A report that printed six
// and said nothing about the rest would be doing to a person what the crafted
// row below does: showing a part and letting it read as the whole.
const holdsShown = 6

// holder is one row of logind's inhibitor table, worded so that it cannot be
// read as zde's own words.
//
// Reached only where the row was gathered with its words in it, which is the
// owner's own screen (gather.go, Logind.Named). Everything below is about a
// stranger's prose arriving in a terminal; what stops the same prose arriving in
// a file that leaves the machine is that it is never gathered.
//
// Both strings are a stranger's. `systemd-inhibit --who=... --why=...` takes
// them from whoever runs it and every local account can run it, so they are the
// same kind of thing as a notification's summary and get the same filter
// (internal/attn, Line; internal/zded/power.go, held): one line, printable, cut
// at 300 characters, so `--why="$(printf '\033[2J')"` cannot clear the terminal
// the report is printed in.
//
// The filter was not enough, and that is what this function is for. The line
// used to be `<who> is holding this session awake: <why>`, which is a sentence
// whose subject and whose second clause are both written by the person being
// reported on. `--who=nothing --why="... logind has nothing holding this
// session awake, and no Wayland app is holding one either, so this machine is
// safe to walk away from."` produced a warning that reads as zde's own
// all-clear, in the one check whose entire purpose is to refuse to give one.
//
// Two things stop that now, and it takes both:
//
//   - zde's own subject comes first and is not a name anybody chose.
//     "something is holding this session awake" is true of every row here, it
//     is the finding, and it survives whatever the strings after it say. The
//     old shape put a chosen word in the position the eye reads as the fact.
//   - the strings are quoted, and quoted by strconv.Quote, so a `"` inside them
//     is escaped rather than closing the quotation. Attribution is the whole
//     point: what is inside the quotes is attributed out loud to the thing
//     being reported on, so text that claims to be zde reads as a holder
//     claiming to be zde, which is itself worth seeing.
//
// Quote and not a hand-rolled wrapper because it escapes the two characters
// that could end the quotation and leaves ordinary Unicode alone - a holder
// named in Cyrillic is still readable, which QuoteToASCII would not leave it.
func holder(h Hold) string {
	line := "something is holding this session awake, and it "
	// A name that filtered away to nothing is a holder that gave none, and
	// saying so is better than quoting an empty string at somebody. It is not a
	// row that goes away: the count is the finding, so a holder with no name is
	// still one holder.
	if who := attn.Line(h.Who); who != "" {
		line += "calls itself " + strconv.Quote(who)
	} else {
		line += "gave no name"
	}
	if why := attn.Line(h.Why); why != "" {
		return line + " and gives " + strconv.Quote(why) + " as its reason"
	}
	// Said rather than left out: a holder that gave no reason is a holder
	// nobody can go and close, and the blank is a fact about it.
	return line + " and gave no reason"
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
