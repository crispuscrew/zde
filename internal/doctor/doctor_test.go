package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		Units: []Unit{
			{Name: "zded", State: "active"},
			{Name: "zde-bar", State: "active"},
		},
		Podman: Podman{Rootless: true},
		Lock: Locker{
			Configured: true,
			Argv0:      "/nix/store/abc-swaylock-1.8/bin/swaylock",
			Service:    "swaylock",
			PAM:        PAMPresent,
		},
		Desks: Desks{Dir: "/home/u/.config/zde/desks", Configured: true},
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
		"unit", "unit", "manifests", "desk apps", "locker", "journal",
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

// A machine with nothing configured at all is one fact and one edit, so it is
// said once however many desks mention however many names. Eight lines carrying
// the same sentence would bury every other line of the report.
func TestAMachineWithNothingConfiguredSaysSoOnceAndNamesWhatTheDesksWanted(t *testing.T) {
	s := healthy()
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

// The gathering half, against files rather than a struct: what a desk declares
// is asked of the same resolver a launch would ask, so the report and the key
// cannot end up disagreeing about whether a name resolves.
func TestTheDesksAreJudgedByTheResolverALaunchWouldUse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
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

	d := probeDesks(manifest.DefaultDir())
	if d.Err != nil {
		t.Fatalf("probeDesks: %v", d.Err)
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
	d := probeDesks(manifest.DefaultDir())
	if d.Err != nil || len(d.Unrunnable) != 0 {
		t.Errorf("a machine with no desks = %+v, want nothing to say", d)
	}
	if c := only(t, Judge(Session{Desks: d}), "desk apps"); c.Level != OK {
		t.Errorf("desk apps = %s, want an all-clear", c)
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
