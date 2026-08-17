package doctor

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/zded"
)

// noon is the clock, fixed, so that a file's name and the time inside it are
// something a test can assert on rather than something it has to allow for.
var noon = time.Date(2026, 8, 13, 9, 4, 5, 0, time.UTC)

// darkMachine is the machine this whole file exists for: no compositor, no
// daemon, no journal to read, and a card that nothing has bound a driver to.
// Every probe in it came back with the reason it could not answer, which is the
// state the snapshot has to survive.
func darkMachine() Machine {
	return Machine{
		Graphics: Graphics{
			NiriErr:  errors.New("niri: NIRI_SOCKET is not set: is this running inside a niri session?"),
			CardsErr: errors.New("open /dev/dri: no such file or directory"),
			LogErr:   errors.New("journalctl did not answer in 5s"),
			Env:      []EnvVar{{Name: "WAYLAND_DISPLAY"}},
		},
		Versions: Versions{Zde: "0.1.0"},
		Hardware: Hardware{Firmware: "UEFI"},
	}
}

// dark is the session doctor gathers on that machine: nothing answered.
func dark() Session {
	return Session{
		Socket:  "/run/user/1000/zde/zded.sock",
		DialErr: errors.New("no zded at /run/user/1000/zde/zded.sock"),
	}
}

// The case the whole feature exists for. Nothing is up: no niri, no zded, no
// journal. The file is still written, it still has all four sections, and every
// blank in it says which question could not be put rather than being blank.
func TestTheSnapshotIsWrittenWhenNothingIsUp(t *testing.T) {
	text := renderReport(dark(), darkMachine(), noon)
	for _, heading := range []string{secGraphics, secVersions, secHardware, secDoctor} {
		if !strings.Contains(text, heading) {
			t.Errorf("%s is missing from a snapshot taken on a machine with nothing running:\n%s", heading, text)
		}
	}
	for _, said := range []string{
		"NIRI_SOCKET is not set",    // why niri could not be asked
		"journalctl did not answer", // why its log could not be read
		"no such file or directory", // why the cards could not be listed
		"no DRM card node at all",   // and what that means
		"not known",                 // the answer, said as an answer
	} {
		if !strings.Contains(text, said) {
			t.Errorf("a snapshot of a dark machine does not say %q:\n%s", said, text)
		}
	}
	// And no line of it is blank where a reading should be, which is the way
	// this fails quietly: a file full of "  model" and nothing after it.
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "" && strings.HasSuffix(line, " ") {
			t.Errorf("a reading ends in nothing: %q", line)
		}
	}
}

// Somebody is going to paste this into a bug report, so the two questions they
// will have have to be answerable off the file itself: is this safe to send,
// and what is missing from it.
func TestTheHeaderSaysWhatIsAndIsNotInTheFile(t *testing.T) {
	text := renderReport(dark(), darkMachine(), noon)
	for _, promise := range []string{
		"no notification text",
		"no clipboard content",
		"waiting in the queue",
		"no window titles",
		"private",
		"allowlist",
	} {
		if !strings.Contains(text, promise) {
			t.Errorf("the header does not say the file has no %s:\n%s", promise, text[:1200])
		}
	}
	if !strings.Contains(text, noon.Format(time.RFC3339)) {
		t.Error("the file does not say when it was taken")
	}
}

// A desk that declares private is history only (docs/vision.md, section 3), and
// this file leaves the machine. Neither the desk nor the app on it may be named
// in it - naming the app and hiding the desk would be the same disclosure with
// an extra step.
//
// Off manifests on a disk and through the probe, rather than off a Session
// written down by hand, because the fix is that the name is never gathered:
// a test that handed the renderer a private entry would be testing a state the
// probe cannot produce, and would have gone on passing while two other fields
// carried the same name into the file.
func TestNothingAboutAPrivateDeskReachesTheFile(t *testing.T) {
	withDesks(t,
		"work.yaml", "name: work\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n  - { app: browser }\n",
		"therapy.yaml", "name: therapy\nprivate: true\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n  - { app: journal-app }\n")
	withZcr(t)

	s := dark()
	s.Status = &zded.Status{Version: "0.1.0", Compositor: "connected"}
	s.Desks = probeDesks(manifest.DefaultDir(), anyone)
	text := renderReport(s, darkMachine(), noon)
	for _, secret := range []string{"therapy", "journal-app"} {
		if strings.Contains(text, secret) {
			t.Errorf("%q is on a desk declared private and is in the file:\n%s", secret, section(t, text, secDoctor))
		}
	}
	// The desk that did not declare it is still reported, or the redaction
	// would be a way to silence the check by declaring everything private.
	if !strings.Contains(text, "work names browser") {
		t.Errorf("the ordinary desk was redacted too:\n%s", section(t, text, secDoctor))
	}
	// And the count survives. A file that silently dropped lines is a file whose
	// all-clear cannot be trusted.
	doc := section(t, text, secDoctor)
	if !strings.Contains(doc, "1 app(s) on desks that declare private") {
		t.Errorf("nothing says something was left out:\n%s", doc)
	}
	// The same machine on the person's own screen: the terminal is theirs, and
	// a redaction that reached it would be zde hiding somebody's desk from them.
	own := Judge(Session{Desks: probeDesks(manifest.DefaultDir(), owner)}).String()
	if !strings.Contains(own, "therapy names journal-app") {
		t.Errorf("`zde doctor` on the owner's own screen hides their own desk:\n%s", own)
	}
}

// An all-clear that is not one. Every unrunnable app on this machine is on the
// private desk, so the list the report is judged off is empty - and an "ok" line
// saying no desk names an app this machine cannot start, printed over the top of
// an app this machine cannot start, is the one failure a redaction must not have.
func TestARedactionNeverTurnsIntoAnAllClear(t *testing.T) {
	withDesks(t, "therapy.yaml",
		"name: therapy\nprivate: true\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n  - { app: journal-app }\n")
	withZcr(t)

	r := Judge(Session{Desks: probeDesks(manifest.DefaultDir(), anyone)})
	c := only(t, r, "desk apps")
	if c.Level == OK {
		t.Errorf("a desk naming an app nothing can start reads as an all-clear once it declares private: %s", c)
	}
	if !strings.Contains(c.Detail, "1 app(s)") {
		t.Errorf("the count of what was left out is not on the line: %s", c)
	}
}

// The second field that carried the name, and the one a pass over the finished
// struct had no way to find: when the resolver stops answering, its error quotes
// whichever app was asked first - which is a name off a desk nobody chose.
func TestAResolverThatStopsAnsweringNamesNoApp(t *testing.T) {
	withDesks(t, "therapy.yaml",
		"name: therapy\nprivate: true\nmonitors: { DP-1: { workspaces: [code] } }\napps:\n  - { app: journal-app }\n")
	// A zcr on PATH that exits without a word, which is the resolver going
	// quiet partway through rather than refusing a name.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zcr"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	d := probeDesks(manifest.DefaultDir(), anyone)
	if d.Err == nil {
		t.Fatal("a resolver that exits 3 with nothing to say is not read as one that stopped answering")
	}
	if strings.Contains(d.Err.Error(), "journal-app") {
		t.Errorf("the app on the private desk is quoted in the error about the resolver: %v", d.Err)
	}
	if !strings.Contains(d.Err.Error(), "not named here") {
		t.Errorf("the error does not say a name was left out of it: %v", d.Err)
	}
	// And on the owner's own screen it is named, because the name is what makes
	// that line something they can act on.
	if own := probeDesks(manifest.DefaultDir(), owner); !strings.Contains(own.Err.Error(), "journal-app") {
		t.Errorf("`zde doctor` will not say which app it could not ask about: %v", own.Err)
	}
}

// The third field, and the one with no `private` flag to read: a manifest that
// will not parse is one nothing can read a `private: true` out of, and every
// entry in that list starts with a path that is a desk's name.
func TestAManifestThatWillNotParseIsCountedAndNotNamed(t *testing.T) {
	s := Session{
		Status: &zded.Status{Version: "0.1.0", Compositor: "connected", BadManifests: []string{
			"/home/u/.config/zde/desks/therapy.yaml: yaml: line 3: mapping values are not allowed",
			"/home/u/.config/zde/desks/divorce.yaml: manifest: no monitors, so the desk has nowhere to be",
		}},
	}
	s.hideManifests(anyone)
	text := renderReport(s, darkMachine(), noon)
	for _, secret := range []string{"therapy", "divorce"} {
		if strings.Contains(text, secret) {
			t.Errorf("%q names a manifest that would not parse and is in the file:\n%s", secret, section(t, text, secDoctor))
		}
	}
	doc := section(t, text, secDoctor)
	if !strings.Contains(doc, "2 manifest(s)") {
		t.Errorf("the desks that are not declared are not counted either:\n%s", doc)
	}
	if strings.Contains(doc, "every manifest zded has read parsed") {
		t.Errorf("an emptied list reads as an all-clear:\n%s", doc)
	}
	// And on the owner's own screen every one of them is named, because the
	// path is the file they have to go and open.
	own := Session{Status: &zded.Status{BadManifests: []string{"/home/u/.config/zde/desks/therapy.yaml: yaml: bad"}}}
	own.hideManifests(owner)
	if !strings.Contains(Judge(own).String(), "therapy.yaml") {
		t.Errorf("`zde doctor` will not say which manifest to go and fix:\n%s", Judge(own))
	}
}

// withDesks writes manifests into the directory this machine's probe reads,
// named in pairs: filename, content.
func withDesks(t *testing.T, pairs ...string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "zde", "desks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		if err := os.WriteFile(filepath.Join(dir, pairs[i]), []byte(pairs[i+1]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A machine with a private desk and nothing wrong with it says so too, because
// "there is a private desk here and it had nothing to report" and "there is no
// private desk" are otherwise the same blank.
func TestAPrivateDeskWithNothingWrongIsStillDeclared(t *testing.T) {
	s := dark()
	s.Status = &zded.Status{Version: "0.1.0", Compositor: "connected"}
	s.Desks = Desks{Dir: "/home/u/.config/zde/desks", Resolver: byZcr, Configured: true, Private: 2}
	doc := section(t, renderReport(s, darkMachine(), noon), secDoctor)
	if !strings.Contains(doc, "2 desk(s) declare private") {
		t.Errorf("a machine with private desks does not say so:\n%s", doc)
	}
}

// forge is what an attacker writes into a reading: a newline, and after it the
// two shapes this file is made of - a section heading in column one and a check
// result in the column an eye runs down. Then an escape sequence, because this
// file is printed straight to a terminal on every machine that has not turned
// zde.debug on (cmd/zde, runReport).
//
// None of it takes an account on this machine. A USB product string is whatever
// its maker wrote in a descriptor and arrives by putting the device in a port;
// an environment value is set by whatever started the session; a DMI string is
// firmware. The demonstration that opened this was a variable with a newline in
// it, which produced a second [doctor] heading and a line reading "ok forged
// every check on this machine passed".
const forge = "real reading\n[doctor]\nok    forged        every check on this machine passed\n" +
	"\x1b]0;a title this file just set\x07"

// The shape of this file is a promise, and every reading in it was written by
// something else. So no reading may start a line: column one is where a heading
// goes, and two spaces in is where a check result goes.
func TestNoReadingCanForgeAHeadingOrACheck(t *testing.T) {
	m := Machine{
		Graphics: Graphics{
			Socket:  forge,
			Screens: []string{forge, "DP-2"},
			Cards: []Card{{
				Node: forge, Driver: forge, PCI: forge,
				OpenErr: errors.New(forge),
			}},
			Fell: []string{forge},
			Saw:  []string{forge},
			Env:  []EnvVar{{Name: "LIBGL_ALWAYS_SOFTWARE", Value: forge, Set: true}},
		},
		Versions: Versions{
			Zde: "0.1.0", Niri: forge, Kernel: forge, OS: forge,
			Revision: forge, Booted: forge, Current: forge,
		},
		Hardware: Hardware{
			Model: forge, Board: forge, CPU: forge, Memory: forge,
			Firmware: "UEFI", Cmdline: forge, Inputs: []string{forge},
		},
	}
	s := dark()
	// The doctor section takes its own path into the file, through the row
	// filter a Check carries (doctor.go, String), so it is forged at too.
	s.Socket, s.Notify = forge, forge
	text := renderReport(s, m, noon)

	// Every line of every section is one this package wrote: a heading, a
	// blank, or a row two spaces in. A reading that added one of its own is a
	// reading that wrote a line of this file.
	body := text[strings.Index(text, secGraphics):]
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "  ") {
			continue
		}
		if line == secGraphics || line == secVersions || line == secHardware || line == secDoctor {
			continue
		}
		t.Errorf("a reading wrote a line of this file: %q", line)
	}
	// Said again as the two things that line would have been, because that is
	// what makes the file readable at all: one of each heading, and no check
	// result that no check produced. Counted as whole lines - the words are
	// still in the file, on the row of the reading they came in on, which is
	// the difference between a value that says "[doctor]" and a heading.
	if n := headings(text, secDoctor); n != 1 {
		t.Errorf("there are %d %s headings in the file:\n%s", n, secDoctor, text)
	}
	// The check results counted rather than looked for, because a filter that
	// indents what follows a newline defeats the heading and not this: a row
	// pushed two spaces in lands exactly where every row in this file already
	// is. So the only safe assertion is that the file has as many check rows as
	// there were checks, and no reading anywhere added one.
	if got, want := checkRows(text), len(Judge(s)); got != want {
		t.Errorf("the file has %d check results and %d checks were made:\n%s", got, want, section(t, text, secDoctor))
	}
	// And nothing a terminal acts on, since this file is printed to one
	// whenever there is nowhere to write it.
	if strings.ContainsRune(text, 0x1b) {
		t.Error("an escape sequence reached a file that is printed to a terminal")
	}
	// The reading itself survives, filtered rather than dropped: a snapshot
	// that silently lost a value is worse than one that shows a strange one.
	if !strings.Contains(text, "real reading") {
		t.Error("the filter dropped the reading instead of flattening it")
	}
}

// checkRows counts the lines shaped like a check result: two spaces, a level,
// and the column an eye runs down. It is what a person reads this section as,
// and therefore what a value must not be able to write one of.
var checkRow = regexp.MustCompile(`(?m)^  (ok|warn|fail|note) `)

func checkRows(text string) int { return len(checkRow.FindAllString(text, -1)) }

// headings counts the lines that are a section heading, as against the times
// those characters appear inside a reading somewhere.
func headings(text, heading string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if line == heading {
			n++
		}
	}
	return n
}

// The bound, enforced rather than derived: the doctor section is one line per
// check and a machine decides how many checks there are.
func TestASnapshotIsCappedAndSaysWhereItWasCut(t *testing.T) {
	s := dark()
	s.Status = &zded.Status{Version: "0.1.0", Compositor: "connected"}
	for i := 0; i < 4000; i++ {
		s.Status.BadManifests = append(s.Status.BadManifests,
			fmt.Sprintf("/home/u/.config/zde/desks/desk%04d.yaml: manifest: no monitors, so the desk has nowhere to be", i))
	}
	text := renderReport(s, darkMachine(), noon)
	if len(text) > reportBytesMax {
		t.Fatalf("a snapshot of %d bytes was written against a %d byte bound", len(text), reportBytesMax)
	}
	if !strings.Contains(text, "cut here") {
		t.Error("the file was cut and does not say so, which reads as a disk that filled up")
	}
	if !strings.HasSuffix(text, "\n") {
		t.Error("the file stops mid-line")
	}
}

// writeReport is what the unit runs. The file it leaves has to be 0600 and has
// to carry the boot in its name, because the journal beside it is asked for by
// boot and pairing the two by guessing at times is how that goes wrong.
func TestTheFileIsPrivateAndNamedForTheBootTheJournalWillBeAskedFor(t *testing.T) {
	dir := userDir(t)
	path, err := writeReport(filepath.Dir(dir), "hello\n", noon, "0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(path); base != "20260813T090405Z-0a1b2c3d.txt" {
		t.Errorf("the file is called %q", base)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("the snapshot is %v, and it is a file about somebody's machine", fi.Mode().Perm())
	}
	if !reportName.MatchString(filepath.Base(path)) {
		t.Errorf("this package would not recognise the name it just wrote: %q", filepath.Base(path))
	}
}

// A directory nobody made is a machine that did not ask for this, and the
// writer must not make itself one: a snapshot writer that quietly created
// somewhere to write would work on the machine nobody configured and write to
// the wrong place on the machine somebody did.
func TestNoDirectoryIsARefusalAndNotADirectoryCreated(t *testing.T) {
	root := t.TempDir()
	if _, err := writeReport(filepath.Join(root, "zde"), "hello\n", noon, "0a1b2c3d"); err == nil {
		t.Fatal("a snapshot was written into a directory layer 0 never made")
	}
	if _, err := os.Stat(filepath.Join(root, "zde")); err == nil {
		t.Error("the writer created the directory it was supposed to refuse")
	}
}

// The bound on the directory. This repo has been bitten twice by something that
// grows with events, so a log directory that keeps every boot for ever is not a
// thing to leave for later.
func TestOnlyTheNewestSnapshotsAreKept(t *testing.T) {
	dir := userDir(t)
	parent := filepath.Dir(dir)
	var written []string
	for i := 0; i < reportsMax+5; i++ {
		at := noon.Add(time.Duration(i) * time.Minute)
		path, err := writeReport(parent, "snapshot\n", at, "0a1b2c3d")
		if err != nil {
			t.Fatal(err)
		}
		written = append(written, filepath.Base(path))
	}
	left := namesIn(t, dir)
	if len(left) != reportsMax {
		t.Fatalf("%d snapshots are kept, against a bound of %d: %q", len(left), reportsMax, left)
	}
	// The newest, and by name: the timestamp is fixed-width UTC, so sorting the
	// names is sorting the boots, and a clock that jumped backwards cannot make
	// this delete the newest file.
	want := strings.Join(written[len(written)-reportsMax:], "|")
	if strings.Join(left, "|") != want {
		t.Errorf("the wrong ones were kept:\n got %q\nwant %q", left, want)
	}
}

// Only files this package generated, matched whole. The directory is on the
// root filesystem and somebody debugging will have copied things into it - a
// rotation that removed whatever was oldest would remove those.
func TestRotationRemovesNothingItDidNotWrite(t *testing.T) {
	dir := userDir(t)
	keep := []string{"notes.txt", "20260813T090405Z-0a1b2c3d.txt.bak", "old-report.log", "readme"}
	for _, name := range keep {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("mine\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < reportsMax+3; i++ {
		if _, err := writeReport(filepath.Dir(dir), "snapshot\n", noon.Add(time.Duration(i)*time.Minute), "0a1b2c3d"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was not written by zde and was deleted anyway", name)
		}
	}
}

// The name that steered rotation into deleting everything it exists to keep.
//
// Any process running as this account can write a file into that directory, and
// one called 29991231T235959Z-ffffffff.txt sorts above every real snapshot there
// will ever be. Eight of them and rotation kept those eight and deleted all
// eight real ones - and since it runs immediately after the write, the file it
// deleted was the one just written, while `zde report` printed the path of it.
func TestASnapshotNamedForACenturyFromNowCostsNoRealOne(t *testing.T) {
	dir := userDir(t)
	parent := filepath.Dir(dir)
	for i := 0; i < reportsMax; i++ {
		plant(t, dir, fmt.Sprintf("2999123%dT235959Z-ffffffff.txt", i))
	}
	var written []string
	for i := 0; i < reportsMax; i++ {
		path, err := writeReport(parent, "snapshot\n", noon.Add(time.Duration(i)*time.Minute), "0a1b2c3d")
		if err != nil {
			t.Fatal(err)
		}
		written = append(written, path)
	}
	// Every one of them, including the last: the path `zde report` prints has
	// to be a path that is still there when the person goes to read it.
	for _, path := range written {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("zde said it wrote %s and rotation deleted it: %v", filepath.Base(path), err)
		}
	}
}

// And a name from the past cannot cost more than one, however many there are: a
// process running as this account can plant names anywhere in the order this
// reads, so what has to be true is that rotation is never the instrument. One
// file written, at most the one file it replaced taken out.
func TestAWriteCostsAtMostTheSnapshotItReplaced(t *testing.T) {
	dir := userDir(t)
	for i := 0; i < 20; i++ {
		plant(t, dir, fmt.Sprintf("20200101T0000%02dZ-deadbeef.txt", i))
	}
	path, err := writeReport(filepath.Dir(dir), "snapshot\n", noon, "0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the snapshot just written is gone: %v", err)
	}
	if left := len(namesIn(t, dir)); left != 20 {
		t.Errorf("one write took %d files out of a directory of 20", 21-left)
	}
}

// plant writes a file under a name this package would recognise as its own,
// which is what an attacker does and what a clock that jumped does by accident.
func plant(t *testing.T, dir, name string) {
	t.Helper()
	if !reportName.MatchString(name) {
		t.Fatalf("%q is not a name this package would rotate, so planting it proves nothing", name)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("planted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A write that could not finish leaves nothing behind. The half of a report
// that did reach the disk has a name that looks like a whole one, no "cut here"
// line in it, and a person who was told no file was written - so it is a file
// nobody knows about that stops in the middle of the section they needed.
func TestAWriteThatCouldNotFinishLeavesNoFile(t *testing.T) {
	dir := userDir(t)
	// The limit the kernel enforces per file, which is how a full disk is
	// reproduced without one. SIGXFSZ comes with it and its default action is
	// to kill this process, so it is ignored for the duration and the write
	// gets EFBIG back instead.
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	var was unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &was); err != nil {
		t.Skipf("this machine will not say what its file size limit is: %v", err)
	}
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 64, Max: was.Max}); err != nil {
		t.Skipf("this machine will not take a file size limit: %v", err)
	}
	_, err := writeReport(filepath.Dir(dir), strings.Repeat("a snapshot line\n", 500), noon, "0a1b2c3d")
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &was); err != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Fatal("a report larger than this process may write reported success")
	}
	if left := namesIn(t, dir); len(left) != 0 {
		t.Errorf("a failed write left %q on the disk, under a name that reads as a whole report", left)
	}
	// And the retry works, which O_EXCL had otherwise made impossible for the
	// rest of the second - the one second somebody is most likely to try again in.
	if _, err := writeReport(filepath.Dir(dir), "the whole of it\n", noon, "0a1b2c3d"); err != nil {
		t.Errorf("the failed write blocked the retry that came after it: %v", err)
	}
}

// Two sessions in one boot are two snapshots, not one overwritten: the first
// one is often the interesting one, and O_EXCL is what makes that a promise
// rather than a hope.
func TestASnapshotNeverWritesOverOne(t *testing.T) {
	dir := userDir(t)
	if _, err := writeReport(filepath.Dir(dir), "first\n", noon, "0a1b2c3d"); err != nil {
		t.Fatal(err)
	}
	if _, err := writeReport(filepath.Dir(dir), "second\n", noon, "0a1b2c3d"); err == nil {
		t.Fatal("the second snapshot of the same second wrote over the first")
	}
	got, err := os.ReadFile(filepath.Join(dir, "20260813T090405Z-0a1b2c3d.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\n" {
		t.Errorf("what is on the disk is %q", got)
	}
}

// A machine with no boot id is a container rather than a machine, and it still
// gets a file: absent is a state everywhere else in this package.
func TestAMachineWithNoBootIdStillGetsASnapshot(t *testing.T) {
	dir := userDir(t)
	path, err := writeReport(filepath.Dir(dir), "hello\n", noon, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reportName.MatchString(filepath.Base(path)) {
		t.Errorf("the name a boot-less machine gets is %q, which this package would not rotate", filepath.Base(path))
	}
}

// userDir makes the directory layer 0 would have made for this account, and
// answers with it. writeReport is given its parent, the way the real one is
// given /var/log/zde.
func userDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), whoami())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
