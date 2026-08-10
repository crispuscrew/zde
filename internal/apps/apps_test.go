package apps

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestArgv(t *testing.T) {
	a := Apps{"terminal": {"foot"}, "editor": {"foot", "-e", "nvim"}}
	got, err := a.Argv("editor")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2] != "nvim" {
		t.Errorf("Argv = %v", got)
	}
}

// A key that does nothing teaches nobody anything. Both refusals have to say
// what the machine can start, because "terminal" being missing and the whole
// file being missing want different things done about them.
func TestArgvSaysWhatIsConfigured(t *testing.T) {
	a := Apps{"terminal": {"foot"}}
	_, err := a.Argv("browser")
	if err == nil {
		t.Fatal("launched an app that is not configured")
	}
	if !strings.Contains(err.Error(), "terminal") {
		t.Errorf("refusal %q does not say what is configured", err)
	}

	_, err = Apps{}.Argv("terminal")
	if err == nil {
		t.Fatal("launched from an empty configuration")
	}
	if !strings.Contains(err.Error(), "zde.apps") {
		t.Errorf("refusal %q does not say where the answer comes from", err)
	}
}

// An entry with no argv is not an app. It would otherwise be a name that
// resolves to nothing and a key that fails in a way nobody can explain.
func TestEmptyArgvIsNotAnApp(t *testing.T) {
	a := Apps{"terminal": {}, "editor": {"nvim"}}
	if _, err := a.Argv("terminal"); err == nil {
		t.Error("an app with no command was launched")
	}
	if names := a.Names(); len(names) != 1 || names[0] != "editor" {
		t.Errorf("Names = %v, want only the one that can run", names)
	}
}

// A machine with no file at all is a machine where nothing is configured, not
// an error: it is what every machine looks like before layer 1 has written
// anything, and the refusal from Argv is the better message.
func TestLoadMissingFile(t *testing.T) {
	a, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || len(a) != 0 {
		t.Errorf("Load on a missing file = %v, %v", a, err)
	}
}

func TestLoadNamesTheFileItCannotParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "apps.json") {
		t.Errorf("err = %v, want one naming the file", err)
	}
}

// A FIFO where the app map should be is refused rather than waited on.
//
// What comes out of this file is exec'd - by `zde app launch` with
// syscall.Exec, and by the daemon when it runs an ask tier - and until this it
// was read with a plain os.ReadFile, which on a FIFO waits in the kernel for a
// writer that never arrives. A key that opens a terminal did nothing, for as
// long as the session lasted, with no message anywhere.
//
// The deadline is the test: without the fix this hangs rather than fails.
func TestAFifoWhereTheAppsShouldBeIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := Load(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read a FIFO as the app map")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Load did not return in 10s: a key press would have hung here")
	}
}

// And the ceiling, because a file this size at this path is not an app map by
// any reading of it.
//
// Valid JSON on purpose, and only just over the line. A file of rubbish would
// be refused by the parser whether there was a ceiling or not, so a test built
// out of one would pass with the bound taken back out - which is what the first
// version of this test did.
func TestSomethingFarTooBigIsNotAnAppMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps.json")
	pad := strings.Repeat("x", bytesMax)
	body := `{"terminal":["foot"],"pad":["` + pad + `"]}`
	if len(body) <= bytesMax {
		t.Fatalf("the test file is %d bytes and the ceiling is %d", len(body), bytesMax)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("a file past the ceiling was parsed as an app map")
	} else if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("error is %q, want the size named", err)
	}
}

// The path with no HOME is still an absolute one.
//
// It used to join an empty string, which makes `.config/zde/apps.json` -
// relative to whatever directory the process was started in. A daemon reads
// this file to find a command line and then runs it, so "wherever you happened
// to be" is the one answer it must not give.
func TestThePathIsAbsoluteEvenWithNoHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := Path("apps.json"); !filepath.IsAbs(got) {
		t.Errorf("Path = %q, which is relative to whatever directory zde was started in", got)
	}
}

// A config symlinked into the nix store is the ordinary install, and it is
// read.
//
// home-manager writes xdg.configFile entries as links into the store
// (nix/home.nix says so where it explains why dynamic.kdl had to be an
// exception), so a defence written as "refuse a symlink, refuse a file that is
// not mine" would refuse to read this on every supported machine and pass every
// test that only used files it wrote itself.
func TestAnAppMapLinkedFromTheStoreIsStillRead(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "store-apps.json")
	if err := os.WriteFile(target, []byte(`{"terminal":["foot"]}`), 0o444); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "apps.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	a, err := Load(link)
	if err != nil {
		t.Fatalf("the ordinary install was refused: %v", err)
	}
	if argv, err := a.Argv("terminal"); err != nil || argv[0] != "foot" {
		t.Errorf("Argv = %v, %v", argv, err)
	}
}
