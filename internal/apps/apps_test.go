package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
