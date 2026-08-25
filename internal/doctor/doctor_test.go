package doctor

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/crispuscrew/zde/internal/bus"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/zded"
)

// healthy is a session with nothing wrong with it. Every case below spoils
// exactly one thing in it, because a check that also fires on a good session
// is no better than one that never fires - and this report is read by somebody
// deciding whether to keep looking.
func healthy() Session {
	return Session{
		Socket: "/run/user/1000/zde/zded.sock",
		Status: &zded.Status{
			Version:       "0.1.0",
			Compositor:    "connected",
			Desks:         2,
			Shell:         true,
			Notifications: true,
			Zinc:          true,
		},
		// The units a login installs, in the state an ordinary machine has them.
		Units: []Unit{
			{Name: "zded", State: "active"},
			{Name: "zde-bar", State: "active"},
			{Name: "zde-idle", State: "active"},
			{Name: "zde-report", State: "inactive", Optional: true, Option: "zde.debug"},
			{Name: "zde-replay", State: "inactive", Optional: true, Option: "zde.capture.replay.enable"},
		},
		Podman: Podman{Rootless: true},
		Lock: Locker{
			Configured: true,
			Argv0:      "/nix/store/abc-swaylock-1.8/bin/swaylock",
			Service:    "swaylock",
			PAM:        PAMPresent,
		},
		Desks: Desks{Dir: "/home/u/.config/zde/desks", Resolver: byZcr, Configured: true},
		Power: Logind{
			Session: "2",
			// Named, because this is the session `zde doctor` gathers on the
			// person's own screen: the holders below arrive with their words in
			// them, and the snapshot's half of that is asserted in report_test.
			Named: true,
			Can: []Can{
				{What: "reboot", Answer: "yes"},
				{What: "power off", Answer: "yes"},
			},
		},
	}
}

// only returns the one check with this name, and complains if there is not
// exactly one: several checks share a name on purpose (a line per manifest, a
// line per unit) and a test that silently read the first would pass on a
// report that had lost the rest.
func only(t *testing.T, r Report, name string) Check {
	t.Helper()
	found := named(r, name)
	if len(found) != 1 {
		t.Fatalf("want one %q check, got %d:\n%s", name, len(found), r)
	}
	return found[0]
}

func named(r Report, name string) []Check {
	var found []Check
	for _, c := range r {
		if c.Name == name {
			found = append(found, c)
		}
	}
	return found
}

func TestHealthySessionSaysSoOnEveryLine(t *testing.T) {
	r := Judge(healthy())
	for _, c := range r {
		if c.Level != OK {
			t.Errorf("healthy session: %s", c)
		}
	}
	if r.Failed() != 0 {
		t.Errorf("healthy session failed %d checks:\n%s", r.Failed(), r)
	}
	if got := only(t, r, "zded").Detail; !strings.Contains(got, "0.1.0") ||
		!strings.Contains(got, "/run/user/1000/zde/zded.sock") {
		t.Errorf("zded line = %q, want the version and the socket it is on", got)
	}
}

// The order and the names are the report: somebody diffs one of these against
// another machine's, and a line that moves or is renamed reads as a difference
// between the machines.
func TestTheReportIsTheSameShapeEveryTime(t *testing.T) {
	want := []string{
		"zded", "compositor", "shell", "notify", "zinc", "podman",
		"unit", "unit", "unit", "unit", "unit", "manifests", "desk apps", "locker", "logind", "idle", "journal",
	}
	var got []string
	for _, c := range Judge(healthy()) {
		got = append(got, c.Name)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("report is\n  %v\nwant\n  %v", got, want)
	}
}

// Three columns, and the level first: this is piped into a bug report, where
// the level is the column an eye runs down.
func TestALineIsLevelNameThenDetail(t *testing.T) {
	got := Check{Warn, "shell", "not listening"}.String()
	if got != "warn  shell         not listening" {
		t.Errorf("line = %q", got)
	}
	if strings.HasSuffix(Report{{OK, "zded", "up"}}.String(), "\n") == false {
		t.Error("the report does not end its last line")
	}
}

// The longest line in this report is zde's own, and it is the one that cannot
// be cut: the idle check says which half of the mechanism it could not see, and
// that half is at the end of the sentence with the two documents that explain
// it. Folded onto a queue row it stopped at "and e", so the one check whose
// whole purpose is to refuse an all-clear printed one with its qualification
// trailing off - and nothing about the line looked wrong.
//
// Every shape the check takes, because the caveat is on all four of them and
// three of them are longer than the one a healthy machine prints.
func TestTheIdleCaveatIsPrintedToItsLastWord(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*Session)
	}{
		{"logind has nothing holding it", func(*Session) {}},
		{"nothing owns logind", func(s *Session) { s.Power.Absent = true }},
		{"logind could not be asked", func(s *Session) { s.Power.Err = errors.New("no system bus") }},
		{"something is holding it", func(s *Session) {
			s.Power.Holds = []Hold{{Who: "steam", Why: "playing a game"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := healthy()
			tc.spoil(&s)
			said := false
			for _, c := range named(Judge(s), "idle") {
				line := c.String()
				if strings.Contains(line, "more characters)") {
					t.Errorf("an idle line was cut: %q", line)
				}
				if strings.Contains(line, unseen) {
					said = true
					// The last words of it, said again on their own: this is
					// what a reader has to reach for the line to mean what it
					// says, and the smoke test greps for exactly this
					// (nix/tests/smoke.nix).
					if !strings.Contains(line, "docs/verify.md, section 11)") {
						t.Errorf("the caveat lost its docs pointer: %q", line)
					}
				}
			}
			if !said {
				t.Errorf("no idle line carries the whole caveat:\n%s", Judge(s))
			}
		})
	}
}

// detailMax's own arithmetic, held to what it was chosen against: the bound is
// above the longest sentence this package writes, so that a cut is always
// somebody else's text. It was 450 characters when the number was picked, and
// a check that grew past two thousand would be back where this started, with
// zde's own qualification taken off the end of zde's own sentence.
func TestNothingZdeWritesInACheckReachesTheBound(t *testing.T) {
	for _, s := range []Session{spoiled(), healthy()} {
		for _, c := range Judge(s) {
			if n := len([]rune(c.Detail)); n >= detailMax {
				t.Errorf("the %s check writes %d characters, which is past detailMax: %s", c.Name, n, c.Detail)
			}
		}
	}
}

// spoiled is a session with nothing right with it, so that every check says the
// longest thing it has to say. Nothing here is another program's words: the
// point is the length of zde's own sentences.
func spoiled() Session {
	s := healthy()
	s.Status.Zinc, s.Status.Shell, s.Status.Notifications = false, false, false
	s.Podman = Podman{Rootless: false}
	s.Lock = Locker{Configured: true, Argv0: "/nix/store/abc-swaylock-1.8/bin/swaylock", Service: "swaylock"}
	s.Power = Logind{Absent: true}
	s.Desks.Private = 2
	return s
}

// The bound did not go away with the cut, it stopped being silent. A detail is
// mostly another program's words and nothing outside this repo agrees to be
// short, so what is printed is bounded - and where it was cut it says so, which
// is what a queue row's filter would not.
func TestADetailPastTheBoundIsCutOutLoud(t *testing.T) {
	line := Check{Fail, "manifests", strings.Repeat("é", detailMax*2)}.String()
	if n := strings.Count(line, "é"); n != detailMax {
		t.Errorf("%d characters of the detail were printed, want %d", n, detailMax)
	}
	if !strings.Contains(line, "more characters)") {
		t.Errorf("a detail was cut and does not say so: %q", line)
	}
	// Characters and not bytes, the way every bound in this tree counts: a
	// manifest error in Cyrillic is not half an error.
	if n := len([]rune(line)); n > detailAt+detailMax+64 {
		t.Errorf("the line is %d characters, which is past its own bound", n)
	}
}

// Almost nothing in this column was written by zde. A name it cannot resolve is
// refused in zcr's own words, a hand-edited manifest is answered by a YAML
// parser quoting the file back, and this report is read in a terminal and pasted
// into bug threads, where ESC is not a character but the start of an
// instruction.
//
// A newline is now kept rather than folded away, so the second half of this is
// where it lands. A check's level is in column one; a continuation is twenty
// characters in, under the detail it continues. Nothing a stranger sends can
// reach column one, so nothing a stranger sends can be a check nobody made.
func TestAForeignDetailCannotDriveTheTerminalOrWriteACheckOfItsOwn(t *testing.T) {
	got := Check{Warn, "desk apps", "zcr: no app \x1b[2Jdefined\x1b]0;pwned\x07\r\n" +
		"ok    forged        every check on this machine passed\n\ttry: zc list\x00"}.String()

	if strings.ContainsAny(got, "\x1b\r\x00\a") {
		t.Errorf("something a terminal acts on reached the report: %q", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) == 1 {
		t.Fatalf("the detail was folded onto one line: %q", got)
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, strings.Repeat(" ", detailAt)) {
			t.Errorf("a continuation is not under the detail column: %q", line)
		}
		for _, level := range []Level{OK, Warn, Fail} {
			if strings.HasPrefix(line, string(level)) {
				t.Errorf("a stranger wrote a check result: %q", line)
			}
		}
	}
	// And what somebody would act on is still all there, which is the point of
	// keeping the shape rather than cutting at the first line.
	for _, want := range []string{"no app", "defined", "try: zc list"} {
		if !strings.Contains(got, want) {
			t.Errorf("the filter dropped %q, which is what the line was for: %q", want, got)
		}
	}
}

// A detail that filtered away to nothing is shown as its bytes rather than left
// blank, for the reason attn.Block shows one: a check that says something is
// wrong and then says nothing about what is worse than a check that shows a
// strange string.
func TestADetailWithNothingPrintableInItIsStillSaid(t *testing.T) {
	got := Check{Fail, "manifests", "\x00\x1b\a"}.String()
	if strings.ContainsAny(got, "\x00\x1b\a") {
		t.Errorf("the bytes were printed rather than escaped: %q", got)
	}
	if !strings.Contains(got, `\x1b`) {
		t.Errorf("a check failed and its line says nothing: %q", got)
	}
}

// Warnings must not reach the exit status. Every machine has some today -
// there is no layer 2 to install zcr from - and a command that always exits
// non-zero is one nobody reads the output of.
func TestOnlyFailuresCount(t *testing.T) {
	s := healthy()
	s.Status.Shell = false
	s.Status.Zinc = false
	s.Units[1].State = "inactive"
	s.Podman = Podman{Err: exec.ErrNotFound}
	r := Judge(s)
	if r.Failed() != 0 {
		t.Errorf("warnings reached the exit status:\n%s", r)
	}
	for _, name := range []string{"shell", "zinc", "podman"} {
		if c := only(t, r, name); c.Level != Warn {
			t.Errorf("%s = %s, want a warning", name, c)
		}
	}

	s.Status.Compositor = "NIRI_SOCKET is not set"
	if n := Judge(s).Failed(); n != 1 {
		t.Errorf("a broken compositor and %d failures, want 1", n)
	}
}

// The case this whole command exists for: zded is not answering, and every
// answer that was its to give says so rather than going missing. A line that
// disappears is one somebody has to notice is not there.
func TestWithoutTheDaemonEveryLineItOwnedSaysWhy(t *testing.T) {
	s := healthy()
	s.Status = nil
	s.DialErr = errors.New("zde: no zded at /run/user/1000/zde/zded.sock: connect: no such file")
	r := Judge(s)

	if c := only(t, r, "zded"); c.Level != Fail || !strings.Contains(c.Detail, "no such file") {
		t.Errorf("zded = %s, want the failure and its reason", c)
	}
	for _, name := range []string{"compositor", "shell", "zinc", "manifests", "journal"} {
		c := only(t, r, name)
		if c.Level != Warn || !strings.Contains(c.Detail, "zded is not answering") {
			t.Errorf("%s = %s, want it to say the daemon could not be asked", name, c)
		}
	}
	// And what does not depend on the daemon is still answered, because a
	// session with no zded is exactly when somebody needs to know whether the
	// screen lock would let them back in.
	if c := only(t, r, "locker"); c.Level != OK {
		t.Errorf("locker = %s, want an answer that did not need zded", c)
	}
}

// The compositor is a failure and not a warning: zded answering with no niri
// behind it is a session where every desk key is silent.
func TestCompositorFailsWithNirisOwnReason(t *testing.T) {
	s := healthy()
	s.Status.Compositor = "NIRI_SOCKET is not set"
	c := only(t, Judge(s), "compositor")
	if c.Level != Fail || !strings.Contains(c.Detail, "NIRI_SOCKET") {
		t.Errorf("compositor = %s, want the failure and the reason niri gave", c)
	}
}

// The useful half of a notification check is who took the name, since the
// answer is to go and stop that program.
func TestNotifyNamesWhoeverTookTheName(t *testing.T) {
	s := healthy()
	s.Status.Notifications = false
	s.Notify = "dunst, pid 812"
	c := only(t, Judge(s), "notify")
	if c.Level != Fail || !strings.Contains(c.Detail, "dunst, pid 812") {
		t.Errorf("notify = %s, want the failure and the owner", c)
	}
	if !strings.Contains(c.Detail, "org.freedesktop.Notifications") {
		t.Errorf("notify = %s, want the name it is about", c)
	}
}

// Nobody holding it is a different session from somebody else holding it: the
// first is a bus that was not there when zded started, and nothing will arrive
// at all.
func TestNotifyWithNobodyOnTheBus(t *testing.T) {
	s := healthy()
	s.Status.Notifications = false
	c := only(t, Judge(s), "notify")
	if c.Level != Fail || !strings.Contains(c.Detail, "nothing owns") {
		t.Errorf("notify = %s, want the failure and that nobody has it", c)
	}

	s.NotifyErr = errors.New("session bus: dial unix: no such file")
	c = only(t, Judge(s), "notify")
	if c.Level != Fail || !strings.Contains(c.Detail, "no such file") {
		t.Errorf("notify = %s, want why the bus could not be asked", c)
	}
}

// With no daemon to say whether the name is zded's, whoever holds it may be a
// zded this doctor could not reach - so it is a reading, not a verdict.
func TestNotifyIsOnlyAWarningWithNoDaemon(t *testing.T) {
	s := healthy()
	s.Status = nil
	s.DialErr = errors.New("no zded")
	s.Notify = "zded, pid 4"
	if c := only(t, Judge(s), "notify"); c.Level != Warn {
		t.Errorf("notify = %s, want a warning while zded cannot be asked", c)
	}
}

// A locker that cannot authenticate is the finding this command is most worth
// having: it takes the screen and then refuses every password, and nothing
// else about the machine looks wrong until the moment it happens.
func TestALockerWithNoPAMServiceIsALostMachine(t *testing.T) {
	s := healthy()
	s.Lock.PAM = PAMAbsent
	c := only(t, Judge(s), "locker")
	if c.Level != Fail {
		t.Fatalf("locker = %s, want a failure", c)
	}
	if !strings.Contains(c.Detail, "/etc/pam.d/swaylock") {
		t.Errorf("locker = %s, want the file it went looking for", c)
	}
	if !strings.Contains(c.Detail, "refuse every password") {
		t.Errorf("locker = %s, want what it would cost", c)
	}
}

func TestLockerCases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lock  Locker
		level Level
		want  string
	}{
		{
			// Mod+Ctrl+semicolon does nothing at all. Bad, and not the same as
			// a machine that locks and will not unlock.
			"nothing configured", Locker{}, Warn, "nothing is configured to lock",
		},
		{
			"binary is gone",
			Locker{Configured: true, Argv0: "/nix/store/gone/bin/swaylock", BinErr: exec.ErrNotFound, Service: "swaylock", PAM: PAMPresent},
			Fail, "/nix/store/gone/bin/swaylock",
		},
		{
			// No PAM at all is a machine doctor cannot answer for, and
			// inventing a failure out of it would be a lie about the one check
			// people would act on.
			"no pam directory",
			Locker{Configured: true, Argv0: "/bin/swaylock", Service: "swaylock", PAM: PAMUnknown},
			Warn, "/etc/pam.d",
		},
		{
			"apps file unreadable",
			Locker{Err: errors.New("apps.json: unexpected end of JSON input")},
			Fail, "unexpected end of JSON input",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := healthy()
			s.Lock = tc.lock
			c := only(t, Judge(s), "locker")
			if c.Level != tc.level || !strings.Contains(c.Detail, tc.want) {
				t.Errorf("locker = %s, want %s containing %q", c, tc.level, tc.want)
			}
		})
	}
}

// One line per manifest, because this is the answer to "where did my desk go"
// and a count would send somebody through the directory looking for which one.
func TestEveryUnreadableManifestGetsItsOwnLine(t *testing.T) {
	s := healthy()
	s.Status.BadManifests = []string{
		"broken.yaml: yaml: unmarshal errors",
		"half.yaml: no monitors",
	}
	r := Judge(s)
	lines := named(r, "manifests")
	if len(lines) != 2 {
		t.Fatalf("want a line per manifest, got %d:\n%s", len(lines), r)
	}
	for i, c := range lines {
		if c.Level != Fail || !strings.Contains(c.Detail, s.Status.BadManifests[i]) {
			t.Errorf("manifest line = %s, want the failure and what is wrong with it", c)
		}
	}
	if r.Failed() != 2 {
		t.Errorf("two unreadable manifests and %d failures", r.Failed())
	}
}

// The line this is all for: a desk that names an app nothing here can start
// used to cost one line in zded's log, at the moment somebody was looking at
// the desk rather than at a log. The report says which desk and which name,
// because that is the edit - open that manifest, or define that app.
func TestEveryAppADeskNamesAndCannotStartGetsItsOwnLine(t *testing.T) {
	s := healthy()
	s.Desks.Unrunnable = []DeskApp{
		{Desk: "film", App: "player", Err: errors.New(`no app called "player"; there is [help lock terminal]`)},
		{Desk: "vshop", App: "nvim", Err: errors.New(`no app called "nvim"; there is [help lock terminal]`)},
	}
	r := Judge(s)
	lines := named(r, "desk apps")
	if len(lines) != 2 {
		t.Fatalf("want a line per app a desk cannot start, got %d:\n%s", len(lines), r)
	}
	for _, want := range []string{"film names player", "vshop names nvim"} {
		if !strings.Contains(r.String(), want) {
			t.Errorf("no line says %q:\n%s", want, r)
		}
	}
	// apps.Argv's own words, not a second opinion: what this machine does have
	// is the other half of the fix, and it is already in that error.
	if !strings.Contains(lines[0].Detail, "there is [help lock terminal]") {
		t.Errorf("desk apps = %s, want the resolver's own answer", lines[0])
	}
}

// Never a failure. Layer 2 is provisioned by hand on every machine there is, so
// a desk naming an app nobody has defined yet is a young machine and not a
// broken one - and doctor's exit status has to keep meaning something.
func TestADeskNamingAnAppNobodyDefinedIsNotAFailure(t *testing.T) {
	s := healthy()
	s.Desks.Unrunnable = []DeskApp{{Desk: "vshop", App: "nvim", Err: errors.New("no app called \"nvim\"")}}
	r := Judge(s)
	if only(t, r, "desk apps").Level != Warn {
		t.Errorf("desk apps = %s, want a warning", only(t, r, "desk apps"))
	}
	if r.Failed() != 0 {
		t.Errorf("a desk naming an undefined app failed %d checks:\n%s", r.Failed(), r)
	}
}

// A machine with nothing in zde.apps is one fact and one edit, so it is said
// once however many desks mention however many names. Eight lines carrying the
// same sentence would bury every other line of the report.
func TestAMachineWithNothingConfiguredSaysSoOnceAndNamesWhatTheDesksWanted(t *testing.T) {
	s := healthy()
	// zde.apps and not zcr on purpose: this collapse is that resolver's alone,
	// because that one is a single option to set and zinc's apps are a file
	// each.
	s.Desks.Resolver = byApps
	s.Desks.Configured = false
	nothing := errors.New("nothing is configured to run: set zde.apps in your home-manager config, " +
		"which is what writes /home/u/.config/zde/apps.json")
	for _, app := range []string{"nvim", "browser", "nvim"} {
		s.Desks.Unrunnable = append(s.Desks.Unrunnable, DeskApp{Desk: "vshop", App: app, Err: nothing})
	}
	c := only(t, Judge(s), "desk apps")
	if c.Level != Warn || !strings.Contains(c.Detail, "set zde.apps") {
		t.Errorf("desk apps = %s, want one warning saying what to set", c)
	}
	// And the names are still there, because they are what goes in that edit.
	if !strings.Contains(c.Detail, "browser, nvim") {
		t.Errorf("desk apps = %s, want the names the desks asked for, once each", c)
	}
}

// A check that could not be made must not read as an all-clear: an empty list
// of problems and no way to have found one look identical on the screen.
func TestDeskAppsSaysWhenItCouldNotAsk(t *testing.T) {
	s := healthy()
	s.Desks.Err = errors.New("/home/u/.config/zde/apps.json: unexpected end of JSON input")
	c := only(t, Judge(s), "desk apps")
	if c.Level != Warn || !strings.Contains(c.Detail, "unexpected end of JSON input") {
		t.Errorf("desk apps = %s, want a warning carrying why nothing could be judged", c)
	}
}

// The healthy line names the directory it read, and it is the only line that
// does: zded can be started with another one (-desks), so a report that never
// said which desks it looked at could be an all-clear about the wrong place.
func TestTheHealthyDeskAppsLineNamesTheDirectoryItRead(t *testing.T) {
	c := only(t, Judge(healthy()), "desk apps")
	if c.Level != OK || !strings.Contains(c.Detail, "/home/u/.config/zde/desks") {
		t.Errorf("desk apps = %s, want the directory the manifests were read from", c)
	}
}

// A unit that is not active is context and not a verdict: a daemon started by
// hand answers exactly as well, which is what anybody debugging this does.
func TestUnitsAreNeverAFailure(t *testing.T) {
	s := healthy()
	s.Units = []Unit{
		{Name: "zded", State: "failed"},
		{Name: "zde-bar", Err: errors.New("Failed to connect to bus")},
	}
	r := Judge(s)
	lines := named(r, "unit")
	if len(lines) != 2 {
		t.Fatalf("want a line per unit, got %d:\n%s", len(lines), r)
	}
	for _, c := range lines {
		if c.Level != Warn {
			t.Errorf("unit = %s, want a warning", c)
		}
	}
	if !strings.Contains(lines[0].Detail, "zded failed") {
		t.Errorf("unit = %s, want the unit and the state systemctl gave", lines[0])
	}
	if !strings.Contains(lines[1].Detail, "Failed to connect to bus") {
		t.Errorf("unit = %s, want why it could not be asked", lines[1])
	}
	if r.Failed() != 0 {
		t.Errorf("a unit that is not up failed %d checks:\n%s", r.Failed(), r)
	}
}

// Every user unit the home module installs is one doctor asks systemd about.
//
// Read off nix/home.nix rather than written out again here, because a second
// copy of the list is a second thing to forget. The consequence is what is
// asserted and not the list: for each unit that file installs, a machine where
// it has failed has a warning naming it. The names only - what each unit is for
// is the module's business.
func TestAFailedUnitTheHomeModuleInstallsIsAWarningThatNamesIt(t *testing.T) {
	for _, name := range unitsTheHomeModuleInstalls(t) {
		if name == "zde-lock" {
			// On demand and intentionally inactive until a lock request.
			continue
		}
		s := healthy()
		s.Units = nil
		for _, u := range loginUnits {
			state := "active"
			if u.Name == name {
				state = "failed"
			}
			s.Units = append(s.Units, Unit{Name: u.Name, State: state, Optional: u.Optional, Option: u.Option})
		}
		var found *Check
		for _, c := range named(Judge(s), "unit") {
			if strings.Contains(c.Detail, name) {
				found = &c
				break
			}
		}
		if found == nil {
			t.Errorf("%s failed and the report does not mention it, so the one thing that "+
				"could have said so is a unit doctor never asks about:\n%s", name, Judge(s))
			continue
		}
		if found.Level != Warn {
			t.Errorf("%s failed and reads as %s", name, *found)
		}
	}
}

// And the state that is not a complaint. systemd answers "inactive" for a unit
// that was never written as well as for one that has not run, so on a machine
// without zde.debug the snapshot unit is reported and not warned about. Failure
// is still a warning.
func TestAUnitOnlyDebugMachinesHaveIsNotAComplaintWhenItIsAbsent(t *testing.T) {
	s := healthy()
	c := unitNamed(t, Judge(s), "zde-report")
	if c.Level != OK {
		t.Errorf("an ordinary machine warns about a unit it never asked for: %s", c)
	}
	if !strings.Contains(c.Detail, "zde.debug") {
		t.Errorf("the line does not say why the word is not a complaint: %s", c)
	}

	// And it buys the unit nothing else. A snapshot that ran and failed is a
	// warning like any other, which is the whole reason it is on the list.
	for i := range s.Units {
		if s.Units[i].Name == "zde-report" {
			s.Units[i].State = "failed"
		}
	}
	if c := unitNamed(t, Judge(s), "zde-report"); c.Level != Warn {
		t.Errorf("a snapshot unit that failed reads as %s", c)
	}
}

func TestReplayUnitNamesItsOwnOptInWhenAbsent(t *testing.T) {
	c := unitNamed(t, Judge(healthy()), "zde-replay")
	if c.Level != OK || !strings.Contains(c.Detail, "zde.capture.replay.enable") {
		t.Errorf("an ordinary machine describes optional replay as %s", c)
	}
}

// unitNamed is the one unit row about a given unit.
func unitNamed(t *testing.T, r Report, name string) Check {
	t.Helper()
	var out []Check
	for _, c := range named(r, "unit") {
		if strings.Contains(c.Detail, name) {
			out = append(out, c)
		}
	}
	if len(out) != 1 {
		t.Fatalf("want one unit row naming %s, got %d:\n%s", name, len(out), r)
	}
	return out[0]
}

// unitsTheHomeModuleInstalls is the names under systemd.user.services in
// nix/home.nix: the block found by its assignment, the names one indent inside
// it. Anything it cannot make sense of fails rather than answering with none,
// because a test that found no units and passed would check nothing.
func unitsTheHomeModuleInstalls(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "nix", "home.nix")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the home module cannot be read, so nothing here can be checked against it: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "systemd.user.services = {" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s no longer assigns systemd.user.services in one block, so this test cannot "+
			"find the units a login installs and doctor's list is unchecked", path)
	}
	indent := strings.Repeat(" ", len(lines[start])-len(strings.TrimLeft(lines[start], " "))+2)
	name := regexp.MustCompile(`^` + indent + `([a-zA-Z][a-zA-Z0-9_-]*) = `)
	var out []string
	for _, l := range lines[start+1:] {
		if strings.TrimRight(l, " ") == strings.TrimSuffix(indent, "  ")+"};" {
			break
		}
		if m := name.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
		}
	}
	if len(out) < 2 {
		t.Fatalf("%s reads as installing %v, which is not a list of user units - the block has "+
			"moved or been reshaped and this test needs to be taught the new one", path, out)
	}
	return out
}

// The journal skips entries it cannot read rather than refusing to open
// (internal/journal), and this count is what stops that being silent.
func TestJournalSaysHowMuchItCouldNotRead(t *testing.T) {
	s := healthy()
	s.Status.Skipped = 3
	c := only(t, Judge(s), "journal")
	if c.Level != Warn || !strings.Contains(c.Detail, "3 entries") {
		t.Errorf("journal = %s, want a warning naming the count", c)
	}
}

// podman not being installed and podman refusing to work are different
// answers, and the second one carries the reason: a user with no subuid range
// is the ordinary way this fails, and it breaks nothing else on the machine.
func TestPodmanSaysWhichKindOfMissingItIs(t *testing.T) {
	s := healthy()
	s.Podman = Podman{Err: exec.ErrNotFound}
	if c := only(t, Judge(s), "podman"); c.Level != Warn || !strings.Contains(c.Detail, "not installed") {
		t.Errorf("podman = %s, want the not-installed answer", c)
	}

	s.Podman = Podman{Err: errors.New("cannot find UID/GID for user zde: no subuid ranges")}
	c := only(t, Judge(s), "podman")
	if c.Level != Warn || !strings.Contains(c.Detail, "subuid") {
		t.Errorf("podman = %s, want podman's own complaint", c)
	}

	s.Podman = Podman{Rootless: false}
	if c := only(t, Judge(s), "podman"); c.Level != Warn || !strings.Contains(c.Detail, "root") {
		t.Errorf("podman = %s, want it to say the sandbox would run as root", c)
	}
}

// noZcr is a machine layer 2 has not reached, which is what every dev host
// running these tests happens to be - and "happens to be" is the part worth
// removing: probeDesks asks whichever resolver PATH has, so a test that did not
// say which one it meant would pass or fail on whether the person running it
// had installed zinc.
func noZcr(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// withZcr puts a zcr on PATH that answers `where` for the apps named and
// refuses everything else in zinc's own words (zinc 0.10.1, cmdWhere - it
// refuses a name it cannot load because the state and bus paths it prints come
// out of that app's config).
func withZcr(t *testing.T, defined ...string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"[ \"$1\" = where ] || { echo \"zcr: unexpected: $*\" >&2; exit 2; }\n" +
		"case \"$2\" in\n" +
		"  " + strings.Join(defined, "|") + ") echo \"state: /state/zinc/$2\"; echo \"container: $2\"; exit 0 ;;\n" +
		"esac\n" +
		"echo \"zcr: no app \\\"$2\\\" defined (try: zc list)\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "zcr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// The bug this branch is about. A manifest's app is a zinc app name and
// zde.apps is the keymap's own name-to-argv map, so a machine that defines its
// apps in zinc and never sets that option would be warned about every desk it
// has - and every one of those desks launches perfectly.
func TestADeskWhoseAppsZincDefinesIsNotWarnedAboutForHavingNoZdeApps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	withZcr(t, "browser", "notes")
	if err := os.MkdirAll(filepath.Join(home, "zde", "desks"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No apps.json at all, which is what that machine looks like on disk.
	if err := os.WriteFile(filepath.Join(home, "zde", "desks", "vshop.yaml"), []byte(
		"name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n"+
			"  - { app: browser }\n"+
			"  - { app: notes, instance: work }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := probeDesks(manifest.DefaultDir(), owner)
	if d.Err != nil {
		t.Fatalf("probeDesks: %v", d.Err)
	}
	if d.Resolver != byZcr {
		t.Errorf("resolver = %q, want the one a launch uses where there is a zcr", d.Resolver)
	}
	if len(d.Unrunnable) != 0 {
		t.Fatalf("unrunnable = %+v, want nothing: zinc defines both", d.Unrunnable)
	}
	if c := only(t, Judge(Session{Desks: d}), "desk apps"); c.Level != OK {
		t.Errorf("desk apps = %s, want an all-clear", c)
	}
}

// And zcr's own refusal is the line, because zcr says why better than a
// paraphrase of zcr would - and it names the next command to run.
func TestWithAZcrTheLineCarriesZcrsOwnRefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	withZcr(t, "browser")
	if err := os.MkdirAll(filepath.Join(home, "zde", "desks"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A full zde.apps, and none of it is the answer: these are zinc app names.
	if err := os.WriteFile(filepath.Join(home, "zde", "apps.json"),
		[]byte(`{"terminal":["foot"],"editor":["nvim"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "zde", "desks", "vshop.yaml"), []byte(
		"name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n"+
			"  - { app: browser }\n"+
			"  - { app: absent-app }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := probeDesks(manifest.DefaultDir(), owner)
	if len(d.Unrunnable) != 1 || d.Unrunnable[0].App != "absent-app" {
		t.Fatalf("unrunnable = %+v, want the one name zinc has no app for", d.Unrunnable)
	}
	if got := d.Unrunnable[0].Err.Error(); !strings.Contains(got, `no app "absent-app" defined`) {
		t.Errorf("unrunnable err = %q, want zcr's own words", got)
	}
	// And it is a line about that app, not the one-line "set zde.apps" that a
	// machine with an empty apps.json gets: this machine's apps are zinc's, and
	// setting that option would not define one of them.
	c := only(t, Judge(Session{Desks: d}), "desk apps")
	if !strings.Contains(c.Detail, "vshop names absent-app") || strings.Contains(c.Detail, "set zde.apps") {
		t.Errorf("desk apps = %s, want the desk and the name and no advice about the wrong option", c)
	}
}

// A zcr that is there and not answering is a fact about the machine, and it
// must not arrive as a list of desks that are wrong: a partial list of faults
// reads exactly like a complete one, and the manifests here are fine.
func TestAZcrThatWillNotAnswerIsNotWrittenDownAsABrokenDesk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := t.TempDir()
	// Non-zero and silent, which is the shape of a zcr that fell over rather
	// than of one that answered: a refusal about a name comes with the name.
	if err := os.WriteFile(filepath.Join(dir, "zcr"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := os.MkdirAll(filepath.Join(home, "zde", "desks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "zde", "desks", "vshop.yaml"), []byte(
		"name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n"+
			"  - { app: browser }\n  - { app: notes }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := probeDesks(manifest.DefaultDir(), owner)
	if d.Err == nil {
		t.Fatalf("a zcr that answers nothing = %+v, want a check that says it could not be made", d)
	}
	if len(d.Unrunnable) != 0 {
		t.Errorf("unrunnable = %+v, want no desk blamed for a resolver that went quiet", d.Unrunnable)
	}
	if got := d.Err.Error(); !strings.Contains(got, "zcr where browser") {
		t.Errorf("err = %q, want the command to run by hand", got)
	}
	c := only(t, Judge(Session{Desks: d}), "desk apps")
	if c.Level != Warn || !strings.Contains(c.Detail, "not known") {
		t.Errorf("desk apps = %s, want a warning that no verdict was reached", c)
	}
}

// Every line of that check says which resolver answered it. The two do not
// answer the same question, so a warning somebody disagrees with has to say
// what it was asked of - otherwise the answer is in the source, and the warning
// is one they learn to skip.
func TestEveryDeskAppsLineSaysWhichResolverAnsweredIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolver string
		want     string
	}{
		{"zcr", byZcr, "asked of zcr"},
		{"zde.apps", byApps, "asked of zde.apps, since no zcr is on PATH"},
		// A session written down without one is judged the way a machine with
		// no zcr would have been, and says so rather than printing a blank.
		{"written down without one", "", "asked of zde.apps, since no zcr is on PATH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := healthy()
			s.Desks.Resolver = tc.resolver
			if c := only(t, Judge(s), "desk apps"); !strings.Contains(c.Detail, tc.want) {
				t.Errorf("the all-clear = %s, want it to say %q", c, tc.want)
			}

			s.Desks.Unrunnable = []DeskApp{{Desk: "vshop", App: "nvim", Err: errors.New("no such app")}}
			if c := only(t, Judge(s), "desk apps"); !strings.Contains(c.Detail, tc.want) {
				t.Errorf("a warning = %s, want it to say %q", c, tc.want)
			}

			s.Desks.Err = errors.New("something went wrong")
			if c := only(t, Judge(s), "desk apps"); !strings.Contains(c.Detail, tc.want) {
				t.Errorf("a check that could not be made = %s, want it to say %q", c, tc.want)
			}
		})
	}
}

// The gathering half, against files rather than a struct: with no zcr the
// desks are still judged, by the only map a machine without layer 2 has.
func TestWithNoZcrTheDesksAreJudgedByZdeApps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	noZcr(t)
	zde := filepath.Join(home, "zde")
	if err := os.MkdirAll(filepath.Join(zde, "desks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zde, "apps.json"), []byte(`{"terminal":["foot"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two instances of the same undefined app, on purpose: it is one thing to
	// define, so it is one line.
	if err := os.WriteFile(filepath.Join(zde, "desks", "vshop.yaml"), []byte(
		"name: vshop\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n"+
			"  - { app: terminal }\n"+
			"  - { app: nvim, instance: vshop }\n"+
			"  - { app: nvim, instance: haven }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := probeDesks(manifest.DefaultDir(), owner)
	if d.Err != nil {
		t.Fatalf("probeDesks: %v", d.Err)
	}
	if d.Resolver != byApps {
		t.Errorf("resolver = %q, want the fallback on a machine with no zcr", d.Resolver)
	}
	if !d.Configured {
		t.Error("a machine with a terminal reads as having nothing configured")
	}
	if len(d.Unrunnable) != 1 {
		t.Fatalf("unrunnable = %+v, want the one app this machine has no answer for", d.Unrunnable)
	}
	got := d.Unrunnable[0]
	if got.Desk != "vshop" || got.App != "nvim" || !strings.Contains(got.Err.Error(), "terminal") {
		t.Errorf("unrunnable = %+v, want vshop's nvim and what this machine does have", got)
	}
}

// A machine with no desks declared is where everyone starts, and it has nothing
// to say here. A directory that is not there must not read as a broken one.
func TestNoDesksAtAllIsNothingToReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	noZcr(t)
	d := probeDesks(manifest.DefaultDir(), owner)
	if d.Err != nil || len(d.Unrunnable) != 0 {
		t.Errorf("a machine with no desks = %+v, want nothing to say", d)
	}
	if c := only(t, Judge(Session{Desks: d}), "desk apps"); c.Level != OK {
		t.Errorf("desk apps = %s, want an all-clear", c)
	}
}

// A machine with no logind cannot be told to go, and every enabled power verb
// verbs a power menu is made of is logind's. Said once, as the consequence,
// because "nobody owns the name" is a reading and a machine that will not shut
// down is what somebody is standing in front of.
func TestNoLogindIsOneWarningSayingNoEnabledPowerVerbWouldWork(t *testing.T) {
	s := healthy()
	s.Power = Logind{Absent: true} // the bus answered: there is no logind here
	c := only(t, Judge(s), "logind")
	if c.Level != Warn {
		t.Fatalf("logind = %s, want a warning: a session with no logind is one somebody can still work in", c)
	}
	if !strings.Contains(c.Detail, logindName) {
		t.Errorf("logind = %s, want the name nobody is on", c)
	}
	for _, verb := range []string{"log out", "reboot", "power off"} {
		if !strings.Contains(c.Detail, verb) {
			t.Errorf("logind = %s, want it to name %q as one of what would not work", c, verb)
		}
	}
	if Judge(s).Failed() != 0 {
		t.Errorf("a machine with no logind failed a check:\n%s", Judge(s))
	}
}

// And a question that could not be put is not an answer. The probe is bounded
// at two seconds, so the ordinary way to land here is a machine that is slow or
// a system bus that is the thing that broke - neither of which is a machine
// that cannot be powered off, which is what this used to tell somebody on the
// strength of a dial that timed out. Every other check in this package has this
// middle state and this one is where it is worth most: it is read by somebody
// deciding whether the power menu is worth pressing.
// The line this check exists to word carefully.
//
// An empty inhibitor table is a true statement about logind and a false one
// about the machine: the Wayland protocol never reaches that bus. The caveat
// must distinguish conditional display power-off from the strict lock, which
// ignores both kinds of inhibitor.
func TestACleanIdleLineSaysWhatItCouldNotSee(t *testing.T) {
	c := only(t, Judge(healthy()), "idle")
	if c.Level != OK {
		t.Fatalf("idle = %s, want ok on a machine holding nothing", c)
	}
	if !strings.Contains(c.Detail, "logind") {
		t.Errorf("idle = %s, want it to name whose table was empty", c)
	}
	// The half it cannot see, named as the protocol so it can be looked up, and
	// with the reason niri cannot be asked.
	for _, want := range []string{"zwp_idle_inhibit_manager_v1", "invisible", "visible rather than focused"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("idle = %s, want it to carry %q", c, want)
		}
	}
	if !strings.Contains(c.Detail, "strict 180-second lock ignores idle inhibitors") {
		t.Errorf("idle = %s, want the strict-lock boundary", c)
	}
	// And it must not be the sentence a person would take away as a promise.
	if strings.Contains(c.Detail, "nothing is holding this session awake") {
		t.Errorf("idle = %s, and it can only speak for logind", c)
	}
}

// A holder is named, because the point of the line is that somebody can go and
// close the thing that is doing it.
func TestAnIdleHolderIsNamedWithWhatItCosts(t *testing.T) {
	s := healthy()
	s.Power.Holds = []Hold{{Who: "steam", Why: "Playing a game"}}
	r := Judge(s)
	found := named(r, "idle")
	if len(found) != 2 {
		t.Fatalf("want the holder and the caveat, got %d:\n%s", len(found), r)
	}
	if found[0].Level != Warn {
		t.Errorf("idle = %s, want a warning", found[0])
	}
	if !strings.Contains(found[0].Detail, "steam") || !strings.Contains(found[0].Detail, "Playing a game") {
		t.Errorf("idle = %s, want the holder and the reason it gave", found[0])
	}
	// The consequence and not only the reading, which is what every line here
	// owes somebody trying to find out what is wrong.
	if !strings.Contains(found[0].Detail, "idle") {
		t.Errorf("idle = %s, want what it costs", found[0])
	}
	// And the list is not claimed to be complete, because it cannot be.
	if !strings.Contains(found[1].Detail, "more than logind can see") {
		t.Errorf("idle = %s, want the list qualified", found[1])
	}
	// A held screen is still a session somebody can work in.
	if r.Failed() != 0 {
		t.Errorf("an idle hold failed a check:\n%s", r)
	}
}

// And they reach a file, which is the half this check was missing.
//
// `systemd-inhibit --who=` defaults, says its own manual page, "to the command
// line string" of whatever ran it - so an ordinary backup puts a path under
// somebody's home directory, the host it is copying to and the name of the file
// it is copying into logind's table, and every one of those went verbatim into
// a snapshot written to be pasted into a bug report. Neither string can cause
// or explain a black screen.
//
// So they are not gathered at all for a reader who is not the owner, which is
// the rule the private desks already had and the field nobody had applied it to
// (gather.go, audience). Asserted at the reading rather than at the row,
// because that is where the fix is: a name that was never put in the struct is
// one no later pass has to know about.
func TestAnIdleHoldersWordsAreNotGatheredForAnybodyButTheOwner(t *testing.T) {
	rows := []inhibitor{
		{What: "idle:sleep", Mode: "block", UID: 1000, PID: 4211,
			Who: "rsync -a /home/alice/Documents/divorce-papers/ backup.example.net:/srv/alice",
			Why: "Scheduled backup of /home/alice"},
		{What: "idle", Mode: "block", UID: 1000, PID: 4300,
			Who: "/home/alice/bin/record-session.sh", Why: "recording"},
		// Neither of these is holding idle off, and neither is the audience's
		// business: the narrowing happens first and is the same for both.
		{What: "sleep", Mode: "block", Who: "systemd-sleep", Why: "not idle"},
		{What: "idle", Mode: "delay", Who: "saver", Why: "a delay is not a hold"},
	}

	// The owner's own screen: their machine, their words, and the next thing
	// they do is go and stop one of these.
	own := heldIdle(rows, owner)
	if len(own) != 2 {
		t.Fatalf("heldIdle for the owner = %d rows, want the two holding idle off", len(own))
	}
	if !strings.Contains(own[0].Who, "divorce-papers") || !strings.Contains(own[0].Why, "/home/alice") {
		t.Errorf("`zde doctor` on the owner's own screen hid what is holding their screen: %+v", own[0])
	}

	// And a file that leaves the machine: the same rows, counted, with nothing
	// of anybody's prose in them.
	all := heldIdle(rows, anyone)
	if len(all) != len(own) {
		t.Fatalf("heldIdle for anyone = %d rows and the owner got %d: the count is the finding", len(all), len(own))
	}
	for i, h := range all {
		if h.Who != "" || h.Why != "" {
			t.Errorf("row %d carries a stranger's words into a file that leaves the machine: %+v", i, h)
		}
	}
}

// The row that is drawn from those readings. A holder that may not be named is
// still a holder, and this is the check whose whole purpose is to refuse to
// give an all-clear it cannot support - so a redaction that turned into one
// would be the worst outcome available here.
func TestHoldersThatMayNotBeNamedAreCountedAndStillAWarning(t *testing.T) {
	s := healthy()
	s.Power.Named = false
	s.Power.Holds = []Hold{{}, {}, {}}

	found := named(Judge(s), "idle")
	if len(found) != 2 {
		t.Fatalf("want the finding and the caveat, got %d:\n%s", len(found), Judge(s))
	}
	if found[0].Level != Warn {
		t.Errorf("idle = %s, want a warning of the same weight a named holder gets", found[0])
	}
	// The count, which is the finding.
	if !strings.Contains(found[0].Detail, "3 thing(s)") {
		t.Errorf("idle = %s, want how many are holding it", found[0])
	}
	// Never an all-clear, in either of the two shapes one could take.
	for _, wrong := range []string{"nothing holding this session awake", "nothing is holding"} {
		if strings.Contains(found[0].Detail, wrong) {
			t.Errorf("idle = %s, and three holders were reported as none", found[0])
		}
	}
	// What it costs, and where the answer is - because a file that talked
	// somebody out of looking further would be worse than no file.
	for _, want := range []string{"until they let go", "systemd-inhibit --list", "zde doctor"} {
		if !strings.Contains(found[0].Detail, want) {
			t.Errorf("idle = %s, want %q on the line", found[0], want)
		}
	}
	// And the caveat is still last, because it qualifies the finding.
	if !strings.Contains(found[1].Detail, "more than logind can see") {
		t.Errorf("idle = %s, want the caveat last", found[1])
	}
	if Judge(s).Failed() != 0 {
		t.Errorf("an idle hold nobody may name failed a check:\n%s", Judge(s))
	}
}

// A holder's own words reach a terminal, and `systemd-inhibit --why=...` takes
// them from whoever runs it. The report is piped into bug reports and read in a
// terminal, so an escape here rewrites the screen it is printed on.
func TestAnIdleHoldersOwnWordsCannotDriveTheReport(t *testing.T) {
	s := healthy()
	s.Power.Holds = []Hold{{Who: "steam\x1b[2J", Why: "one\ntwo"}}
	c := named(Judge(s), "idle")[0]
	if strings.ContainsAny(c.Detail, "\x1b\n\r") {
		t.Errorf("idle = %q, and a holder chose part of it", c.Detail)
	}
}

// And they cannot be zde's words either, which is the harder half.
//
// The filter above stops a holder driving the terminal; it does nothing about a
// holder writing English. `--who=nothing --why="... this machine is safe to
// walk away from."` produced a warning that read as an all-clear, in the one
// check whose whole purpose is to refuse to give one. What stops it is the
// shape of the line: zde's own subject first, and the holder's two strings
// quoted and attributed to it.
func TestAnIdleHoldersOwnWordsCannotReadAsZdesOwn(t *testing.T) {
	s := healthy()
	s.Power.Holds = []Hold{{
		Who: "nothing",
		Why: "and there is nothing else: logind has nothing holding this session awake, " +
			"and no Wayland app is holding one either, so this machine is safe to walk away from.",
	}}
	c := named(Judge(s), "idle")[0]

	// The finding is zde's and it is first, so it is what survives a reader who
	// stops at the start of the line or a file that stops at the end of one.
	if !strings.HasPrefix(c.Detail, "something is holding this session awake") {
		t.Errorf("idle = %q, want zde's own finding at the front", c.Detail)
	}
	// Both of the holder's strings are inside quotes, so neither is a clause of
	// zde's sentence.
	for _, want := range []string{`"nothing"`, `"and there is nothing else: logind has nothing`} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("idle = %q, want %s quoted as the holder's own", c.Detail, want)
		}
	}
	// And it is still a warning, on a line that ends by saying what it costs.
	if c.Level != Warn || !strings.Contains(c.Detail, "display power-off wait until it lets go") ||
		!strings.Contains(c.Detail, "strict lock does not") {
		t.Errorf("idle = %s, want a warning that says what a held idle timer costs", c)
	}
}

// The quotation cannot be closed from inside it, which is what makes quoting a
// defence rather than punctuation. A holder that ships a `"` of its own would
// otherwise end the attribution and write the rest of the line itself.
func TestAnIdleHolderCannotCloseTheQuotationAroundIt(t *testing.T) {
	s := healthy()
	s.Power.Holds = []Hold{{Who: `a" and zde says`, Why: `b" - so this machine is safe`}}
	c := named(Judge(s), "idle")[0]
	// Two quoted strings, so four quote characters and no more: every `"` the
	// holder sent came through escaped.
	if n := strings.Count(c.Detail, `"`) - strings.Count(c.Detail, `\"`); n != 4 {
		t.Errorf("idle = %q, want exactly the two quotations zde opened", c.Detail)
	}
}

// The number of rows is a stranger's to choose - logind holds InhibitorsMax of
// them, 8192 by default - and this report is read by people and written to a
// file with a ceiling on it. So the rows are sampled and the count is said.
func TestAFloodOfIdleHoldersIsCountedRatherThanPrinted(t *testing.T) {
	s := healthy()
	const flood = 500
	for i := 0; i < flood; i++ {
		s.Power.Holds = append(s.Power.Holds, Hold{
			Who: "holder" + strconv.Itoa(i),
			Why: strings.Repeat("x", 1<<20),
		})
	}
	found := named(Judge(s), "idle")
	// The sample, the line that counts the rest, and the caveat.
	if len(found) != holdsShown+2 {
		t.Fatalf("want %d lines for %d holders, got %d", holdsShown+2, flood, len(found))
	}
	if !strings.Contains(found[holdsShown].Detail, strconv.Itoa(flood-holdsShown)) {
		t.Errorf("idle = %q, want how many were not printed", found[holdsShown].Detail)
	}
	// And the whole of it stays a thing a person can read.
	n := 0
	for _, c := range found {
		n += len(c.String())
	}
	if n > 8<<10 {
		t.Errorf("the idle check printed %d bytes, which is not a report anybody reads", n)
	}
	// The caveat is still last, because it qualifies the list and not one row.
	if !strings.Contains(found[len(found)-1].Detail, "more than logind can see") {
		t.Errorf("idle = %q, want the caveat last", found[len(found)-1].Detail)
	}
}

// The caveat is the one thing in this check that is zde's own and has to arrive
// whole. It is spelled out rather than summarised because the summary is the
// mistake, and it is longer than the 300 characters that bound every string a
// stranger sends - so anything that ever put it through that filter, or through
// a per-line ceiling chosen for foreign text, would cut zde's own qualification
// off the end of an all-clear and leave the all-clear.
func TestTheCaveatArrivesWhole(t *testing.T) {
	held := healthy()
	held.Power.Holds = []Hold{{Who: "steam", Why: "Playing a game"}}
	absent := healthy()
	absent.Power = Logind{Absent: true}
	unreachable := healthy()
	unreachable.Power = Logind{Err: errors.New("the bus did not finish connecting")}

	for name, s := range map[string]Session{
		"an empty table":    healthy(),
		"a named holder":    held,
		"no logind at all":  absent,
		"a logind that was": unreachable,
	} {
		lines := named(Judge(s), "idle")
		// Last, always: it qualifies the list rather than any one row of it.
		if last := lines[len(lines)-1]; !strings.Contains(last.Detail, unseen) {
			t.Errorf("%s: idle = %q, want all %d characters of the caveat",
				name, last.Detail, len([]rune(unseen)))
		}
	}
}

// The three ways this question goes unanswered, none of which may read as an
// empty table. A logind that timed out saying "nothing is holding your screen"
// is doctor inventing an all-clear.
func TestAnUnaskedIdleQuestionIsNeverAnAllClear(t *testing.T) {
	for _, tc := range []struct {
		name string
		with func(*Session)
		want string
	}{{
		name: "no logind on the bus",
		with: func(s *Session) { s.Power = Logind{Absent: true} },
		want: "never asked",
	}, {
		name: "logind could not be reached",
		with: func(s *Session) { s.Power = Logind{Err: errors.New("the bus did not answer")} },
		want: "not known",
	}, {
		name: "logind refused the listing",
		with: func(s *Session) { s.Power.HoldsErr = errors.New("connection closed") },
		want: "systemd-inhibit --list",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			s := healthy()
			tc.with(&s)
			c := only(t, Judge(s), "idle")
			if c.Level != Warn {
				t.Errorf("idle = %s, want a warning", c)
			}
			if !strings.Contains(c.Detail, tc.want) {
				t.Errorf("idle = %s, want %q", c, tc.want)
			}
			// The one thing none of these may say.
			if c.Level == OK {
				t.Errorf("idle = %s, and the question was never answered", c)
			}
		})
	}
}

func TestALogindThatCouldNotBeAskedIsNotAMachineThatCannotBeToldToGo(t *testing.T) {
	s := healthy()
	s.Power = Logind{Err: errors.New("the bus did not finish connecting within 2s")}
	c := only(t, Judge(s), "logind")
	if c.Level != Warn {
		t.Fatalf("logind = %s, want a warning", c)
	}
	if !strings.Contains(c.Detail, "did not finish connecting") {
		t.Errorf("logind = %s, want the reading it came from", c)
	}
	// The line this must not be: a verdict about the machine, off a question
	// nothing answered.
	if strings.Contains(c.Detail, "nothing can log out") {
		t.Errorf("logind = %s, and nobody asked logind anything", c)
	}
	if !strings.Contains(c.Detail, "not known") || !strings.Contains(c.Detail, "never asked") {
		t.Errorf("logind = %s, want it to say the question was not put", c)
	}
	// And the way to put it by hand, since somebody reading this is one command
	// away from the answer doctor could not get.
	if !strings.Contains(c.Detail, "loginctl") {
		t.Errorf("logind = %s, want the question by hand", c)
	}
	if Judge(s).Failed() != 0 {
		t.Errorf("a logind that could not be asked failed a check:\n%s", Judge(s))
	}
}

// polkit answering "challenge" is a refusal on this machine, not a question
// somebody gets asked: a zde session has no authentication agent, so whatever
// asks logind either asks non-interactively or waits for a dialog nobody will
// ever see. The line has to name the verb, polkit's own word, and where a rule
// goes - it is the one finding here that is fixed by an edit rather than by
// stopping something.
func TestAPolkitChallengeIsSaidAsARefusalAndNamesTheVerb(t *testing.T) {
	s := healthy()
	s.Power.Can = []Can{
		{What: "reboot", Answer: "challenge"},
		{What: "power off", Answer: "challenge"},
	}
	r := Judge(s)
	lines := named(r, "logind")
	if len(lines) != 2 {
		t.Fatalf("want a line per verb that would not work, got %d:\n%s", len(lines), r)
	}
	for i, verb := range []string{"reboot", "power off"} {
		c := lines[i]
		if c.Level != Warn || !strings.Contains(c.Detail, verb) {
			t.Errorf("logind = %s, want a warning naming %q", c, verb)
		}
		if !strings.Contains(c.Detail, `"challenge"`) || !strings.Contains(c.Detail, "would be refused") {
			t.Errorf("logind = %s, want polkit's own word and what it costs", c)
		}
		if !strings.Contains(c.Detail, "polkit.extraConfig") {
			t.Errorf("logind = %s, want where the rule that fixes it goes", c)
		}
	}
	if r.Failed() != 0 {
		t.Errorf("polkit refusing a reboot failed a check:\n%s", r)
	}
}

// "na" is a machine that cannot do it at all rather than one that is not
// allowed to, and the two are different evenings: one is a polkit rule to
// write, and the other is hardware.
func TestAVerbTheMachineCannotDoIsNotSaidAsARefusal(t *testing.T) {
	s := healthy()
	s.Power.Can[0] = Can{What: "reboot", Answer: "na"}
	c := only(t, Judge(s), "logind")
	if c.Level != Warn || !strings.Contains(c.Detail, "not available on this machine") {
		t.Errorf("logind = %s, want it separated from a refusal", c)
	}
	if strings.Contains(c.Detail, "polkit") {
		t.Errorf("logind = %s, want no polkit advice about hardware that cannot reboot", c)
	}
}

// A log out with no session to end is the failure this half exists for: zded
// refuses rather than guessing, which is the right way round and still a key
// that does nothing. Saying it before somebody presses it is the whole point.
func TestALogOutWithNoSessionToEndSaysSoBeforeAnybodyPressesIt(t *testing.T) {
	s := healthy()
	s.Power.Session = ""
	c := only(t, Judge(s), "logind")
	if c.Level != Warn || !strings.Contains(c.Detail, "log out would refuse") {
		t.Errorf("logind = %s, want a warning about the log out", c)
	}
	if !strings.Contains(c.Detail, "loginctl session-status") {
		t.Errorf("logind = %s, want the command that says what logind can see", c)
	}

	s.Power.SessionErr = errors.New("Rejected send message, 1 matched rules")
	c = only(t, Judge(s), "logind")
	if c.Level != Warn || !strings.Contains(c.Detail, "matched rules") {
		t.Errorf("logind = %s, want why logind could not be asked", c)
	}
}

// The healthy line names the session, because that is the reading a person
// checks against `loginctl` when a log out ends the wrong thing - and it says
// what the three enabled verbs are for, since this check landed before the menu that
// presses them.
func TestTheHealthyLogindLineNamesTheSessionALogOutWouldEnd(t *testing.T) {
	c := only(t, Judge(healthy()), "logind")
	if c.Level != OK || !strings.Contains(c.Detail, "session 2") {
		t.Errorf("logind = %s, want the session id it would end", c)
	}
	if !strings.Contains(c.Detail, "power menu") {
		t.Errorf("logind = %s, want what asks for these verbs", c)
	}
}

// Whatever logind answers, this never reaches the exit status: a container has
// no logind and is not a broken machine, and the smoke test's rule is that
// doctor exits 0 on a session that works.
func TestNoLogindAnswerIsEverAFailure(t *testing.T) {
	for _, answer := range []string{"yes", "no", "na", "challenge", "", "something new"} {
		s := healthy()
		s.Power.Can = []Can{{What: "reboot", Answer: answer}}
		if n := Judge(s).Failed(); n != 0 {
			t.Errorf("logind answering %q failed %d checks:\n%s", answer, n, Judge(s))
		}
	}
	s := healthy()
	s.Power.Can[0].Err = errors.New("Connection reset by peer")
	c := named(Judge(s), "logind")[0]
	if c.Level != Warn || !strings.Contains(c.Detail, "Connection reset") {
		t.Errorf("logind = %s, want a warning carrying why it could not be asked", c)
	}
}

// The other half of that, and the half only a real bus can show: a session
// where the bus answers and has nobody on logind's name is a machine that has
// no logind, which is the verdict the line above must not be confused with. A
// Logind built by hand says what Judge does with each state and nothing at all
// about which state the probe produces, and that is where the two are told
// apart (gather.go, probeLogind).
func TestABusThatAnswersWithNoLogindOnItIsTheVerdictAndNotAReading(t *testing.T) {
	busWithNobodyOnIt(t)
	l := probeLogind(owner)
	if l.Err != nil {
		t.Fatalf("probeLogind against a bus that answered = %v, want its answer", l.Err)
	}
	if !l.Absent {
		t.Fatalf("probeLogind = %+v, want a machine with no logind said as one", l)
	}
	c := only(t, Judge(Session{Power: l}), "logind")
	if c.Level != Warn || !strings.Contains(c.Detail, "nothing can log out") {
		t.Errorf("logind = %s, want the warning that names what would not work", c)
	}
}

// busWithNobodyOnIt is a private message bus with nobody on it at all.
//
// A real dbus-daemon rather than a fake at the Go boundary, for the reason
// internal/link's fake NetworkManager is one (fakebus_test.go): what is being
// asserted here is what the other end says, and the seam is the same single
// environment variable, so no production code learns it is being tested.
func busWithNobodyOnIt(t *testing.T) { privateBus(t) }

// privateBus starts that daemon and answers with the address it is listening
// on, having already pointed this test's own environment at it.
//
// The address comes back so that a test can put something on the bus as well as
// ask about an empty one: the two callers below are "there is no logind here"
// and "there is a logind and this is what it says".
func privateBus(t *testing.T) string {
	t.Helper()
	const daemon = "dbus-daemon"
	if _, err := exec.LookPath(daemon); err != nil {
		t.Skipf("no %s on PATH, so there is no bus that answers to ask about logind: "+
			"add pkgs.dbus to the devshell (flake.nix) and this runs", daemon)
	}
	cfg := filepath.Join(t.TempDir(), "bus.conf")
	// The socket goes wherever the daemon puts it: a unix socket path is capped
	// at about 108 bytes and a Go temp directory has spent most of that.
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
	cmd := exec.Command(daemon, "--config-file="+cfg, "--nofork", "--print-address")
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
		t.Fatalf("%s never printed an address: %v", daemon, err)
	}
	t.Cleanup(func() {
		// Interrupt rather than kill, so it unlinks its socket on the way out
		// instead of leaving one in /tmp per test run.
		cmd.Process.Signal(os.Interrupt) //nolint:errcheck // it is going away either way
		cmd.Wait()                       //nolint:errcheck // its exit status is not this test's business
	})
	addr := strings.TrimSpace(line)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)
	return addr
}

// logindFake is the handful of calls probeLogind puts to logind, and nothing
// else. A real bus and a real decode rather than a Logind written down by hand,
// because what is being asserted is that the audience reaches the wire: the two
// strings are on the rows logind sent and the question is whether they are still
// there when the probe has finished with them.
type logindFake struct{ rows []inhibitor }

func (*logindFake) CanReboot() (string, *dbus.Error)   { return "yes", nil }
func (*logindFake) CanPowerOff() (string, *dbus.Error) { return "yes", nil }

func (f *logindFake) ListInhibitors() ([]inhibitor, *dbus.Error) { return f.rows, nil }

func (*logindFake) GetUser(uid uint32) (dbus.ObjectPath, *dbus.Error) {
	return dbus.ObjectPath("/org/freedesktop/login1/user/_1000"), nil
}

// userFake is that user object's one property: the graphical session, as the
// (id, path) pair logind answers with.
type userFake struct{}

func (userFake) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(struct {
		ID   string
		Path dbus.ObjectPath
	}{"2", "/org/freedesktop/login1/session/_32"}), nil
}

// busWithLogindOn puts that fake on a bus of this test's own and hands back
// nothing: the seam is the environment variable, so no production code learns
// it is being tested (see busWithNobodyOnIt).
func busWithLogindOn(t *testing.T, rows []inhibitor) {
	t.Helper()
	addr := privateBus(t)
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck // the test is over
	if err := conn.Export(&logindFake{rows: rows}, logindPath, logindMgr); err != nil {
		t.Fatal(err)
	}
	if err := conn.Export(userFake{}, "/org/freedesktop/login1/user/_1000", "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName(logindName, dbus.NameFlagDoNotQueue)
	if err != nil {
		t.Fatal(err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("the fake could not take %s on its own bus", logindName)
	}
}

// The whole probe, end to end, against a logind that answers. This is the line
// that says which of the two the readings are for, and it is one line away from
// the call that takes them - so it is asserted where a mistake in it would
// actually show: in what came back off the wire.
func TestTheProbeTakesAHoldersWordsForTheOwnerAndForNobodyElse(t *testing.T) {
	rows := []inhibitor{{
		What: "idle", Mode: "block", UID: 1000, PID: 4211,
		Who: "rsync -a /home/alice/Documents/divorce-papers/ backup.example.net:/srv/alice",
		Why: "Scheduled backup of /home/alice",
	}}
	busWithLogindOn(t, rows)

	own := probeLogind(owner)
	if own.Err != nil {
		t.Fatalf("probeLogind against a logind that answered = %v", own.Err)
	}
	if !own.Named || len(own.Holds) != 1 || !strings.Contains(own.Holds[0].Who, "divorce-papers") {
		t.Fatalf("the owner's own screen did not get the holder logind named: %+v", own)
	}

	all := probeLogind(anyone)
	if all.Named {
		t.Error("a gather for a file that leaves the machine says its holders are named")
	}
	if len(all.Holds) != 1 {
		t.Fatalf("probeLogind for anyone = %d holds, want the count to survive", len(all.Holds))
	}
	if all.Holds[0].Who != "" || all.Holds[0].Why != "" {
		t.Errorf("a holder's own words came off the wire into a file that leaves the machine: %+v", all.Holds[0])
	}
	// And the check drawn from it says the count and no name at all.
	c := named(Judge(Session{Power: all}), "idle")[0]
	if !strings.Contains(c.Detail, "1 thing(s)") || strings.Contains(c.Detail, "divorce-papers") {
		t.Errorf("idle = %s", c)
	}
}

// The probe half. Every check here has to work on a machine where the thing is
// simply absent, and a bus that is not there is the ordinary case in a build
// sandbox and a container - so it has to answer, quickly, rather than hang the
// one command somebody runs when something is already wrong.
func TestTheLogindProbeAnswersOnAMachineWithNoSystemBus(t *testing.T) {
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "no-bus-here"))
	start := time.Now()
	l := probeLogind(owner)
	if l.Err == nil {
		t.Fatalf("probeLogind against nothing = %+v, want the absence said out loud", l)
	}
	if took := time.Since(start); took > askFor+bus.Within {
		t.Errorf("probeLogind took %s with no bus to talk to, and doctor is the command that has to come back", took)
	}
	if c := only(t, Judge(Session{Power: l}), "logind"); c.Level != Warn {
		t.Errorf("logind = %s, want a warning and not a failure", c)
	}
}

// The three answers are three, and the middle one is the point: a machine with
// no /etc/pam.d is not a machine with a locker that cannot authenticate.
func TestPAMState(t *testing.T) {
	dir := t.TempDir()
	if got := pamState(dir, "swaylock"); got != PAMAbsent {
		t.Errorf("pamState with no service file = %v, want PAMAbsent", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "swaylock"), []byte("auth include login\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pamState(dir, "swaylock"); got != PAMPresent {
		t.Errorf("pamState with the service file there = %v, want PAMPresent", got)
	}
	if got := pamState(filepath.Join(dir, "nothing-here"), "swaylock"); got != PAMUnknown {
		t.Errorf("pamState on a machine with no PAM = %v, want PAMUnknown", got)
	}
}

// run is what every probe goes through, and a probe that hangs would take away
// the one command that could have explained the session.
func TestRunIsBounded(t *testing.T) {
	_, err := run(50*time.Millisecond, "sleep", "30")
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("run of a program that never returns = %v, want a deadline", err)
	}
	// And a program's silence stays tellable from its answer, which is what
	// lets zcr timing out be a fact about the machine rather than a fault
	// written down beside somebody's desk (resolves).
	var late noAnswer
	if !errors.As(err, &late) {
		t.Errorf("run of a program that never returns = %v, want it recognisable as no answer", err)
	}
	if _, err := run(probeTimeout, "sh", "-c", "echo said something >&2; exit 1"); errors.As(err, &late) {
		t.Errorf("a program that complained and exited = %v, want that told apart from silence", err)
	}
}

// A program that is not installed has to stay recognisable as one all the way
// to the report, which is what lets podman's two kinds of missing be told
// apart: not there at all, and there and refusing.
func TestRunKeepsANotInstalledErrorRecognisable(t *testing.T) {
	_, err := run(probeTimeout, "zde-doctor-no-such-program")
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("run of a program that is not installed = %v, want exec.ErrNotFound", err)
	}
}

// The program's own first line of complaint, because "exit status 125" is the
// start of another search rather than the end of this one.
func TestRunCarriesTheReasonAndTheOutput(t *testing.T) {
	if _, err := run(probeTimeout, "sh", "-c", "echo nope: no subuid ranges >&2; exit 125"); err == nil ||
		err.Error() != "nope: no subuid ranges" {
		t.Errorf("run error = %v, want the program's own complaint", err)
	}
	// And what it printed survives a non-zero exit, which is what `systemctl
	// --user is-active` needs: it prints the state and exits non-zero for
	// every state but one.
	out, err := run(probeTimeout, "sh", "-c", "echo inactive; exit 3")
	if strings.TrimSpace(out) != "inactive" {
		t.Errorf("run = %q (err %v), want what the program printed", out, err)
	}
}

// The deadline, against the one thing that used to walk straight through it.
//
// cmd.Run copies stdout and stderr from pipes, and a program that forks hands a
// copy of both ends to its child - so Wait waits on the fork rather than on the
// program, and probeTimeout was a promise this code did not keep. Measured
// before the WaitDelay: five seconds never returned and had to be killed at
// forty. None of the five programs doctor probes reproduced it, which is what
// makes this a test rather than a bug report - and doctor is what somebody runs
// when the machine is already misbehaving, which is when a child that forks and
// hangs is likeliest.
func TestARunIsBoundedAgainstAProgramThatForks(t *testing.T) {
	// Answers, then leaves a child holding the pipe. `podman info` against a
	// service coming up is the shape this stands in for.
	done := make(chan string, 1)
	go func() {
		out, _ := run(probeTimeout, "sh", "-c", "sleep 60 & echo answered")
		done <- out
	}()
	// probeTimeout is not even reached: the program exits at once, so what is
	// waited out is probeGrace. The slack is for a loaded machine.
	ceiling := probeTimeout + probeGrace + 3*time.Second
	select {
	case out := <-done:
		// And the answer came back whole. A bound that cost the output would
		// have turned a hang into a probe that quietly says nothing.
		if strings.TrimSpace(out) != "answered" {
			t.Errorf("run = %q, want what the program printed before it forked", out)
		}
	case <-time.After(ceiling):
		t.Fatalf("a probe against a program that forks has not returned in %v, "+
			"and its own deadline is %v", ceiling, probeTimeout)
	}
}
