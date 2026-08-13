package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
func TestNothingAboutAPrivateDeskReachesTheFile(t *testing.T) {
	s := dark()
	s.Status = &zded.Status{Version: "0.1.0", Compositor: "connected"}
	s.Desks = Desks{
		Dir:        "/home/u/.config/zde/desks",
		Resolver:   byZcr,
		Configured: true,
		Private:    1,
		Unrunnable: []DeskApp{
			{Desk: "work", App: "browser", Err: errors.New(`no app "browser" defined`)},
			{Desk: "therapy", App: "journal-app", Err: errors.New(`no app "journal-app" defined`), Private: true},
		},
	}
	text := renderReport(s, darkMachine(), noon)
	for _, secret := range []string{"therapy", "journal-app"} {
		if strings.Contains(text, secret) {
			t.Errorf("%q is on a desk declared private and is in the file:\n%s", secret, section(t, text, secDoctor))
		}
	}
	// The desk that did not declare it is still reported, or the redaction
	// would be a way to silence the check by declaring everything private.
	if !strings.Contains(text, "browser") {
		t.Errorf("the ordinary desk was redacted too:\n%s", section(t, text, secDoctor))
	}
	// And the count survives. A file that silently dropped lines is a file whose
	// all-clear cannot be trusted.
	doc := section(t, text, secDoctor)
	if !strings.Contains(doc, "1 app(s) on desks that declare private") {
		t.Errorf("nothing says something was left out:\n%s", doc)
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
