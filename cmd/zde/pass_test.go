package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/pass"
)

// A machine with a pepper, one site on cheap parameters, and nothing else. The
// parameters are in the file rather than from `zde pass add`, because the
// shipped ones spend 64 MiB per derivation by design and a test suite that ran
// them a dozen times would be one nobody runs.
func machine(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// The gate on printing a derived password, open for the length of a test
	// that has to look at one. Set here rather than in the test bodies so that
	// nothing depends on what is in a developer's own environment.
	t.Setenv("ZDE_PASS_SHOW_SECRET", "1")
	if err := pass.CreatePepper(pass.PepperPath()); err != nil {
		t.Fatal(err)
	}
	const sites = `{"version":1,"sites":[{"service":"example.org","counter":2,
	  "params":{"version":1,"time":1,"memory_kib":64,"lanes":1},
	  "policy":{"length":20,"allow":["lower","upper","digit","symbol"],
	            "require":{"lower":1,"upper":1,"digit":1,"symbol":1}}}]}`
	if err := os.WriteFile(pass.StorePath(), []byte(sites), 0o600); err != nil {
		t.Fatal(err)
	}
}

// caught runs one command with the streams pointed at files, and answers with
// what each of them got.
func caught(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	in, err := os.CreateTemp(dir, "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteString(stdin); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	out, err := os.CreateTemp(dir, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	said, err := os.CreateTemp(dir, "stderr")
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, out, said
	runErr := run(args)
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	o, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.ReadFile(said.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(o), string(s), runErr
}

const master = "correct horse battery staple\n"

// The whole of principle 5 in one test: a derived password reaches no stream at
// all unless somebody asked for it twice. Until the trusted window can type one
// into a field, printing it is a development path and not a feature - and there
// is deliberately no spelling of any of this that puts a password on the
// clipboard.
func TestADerivedPasswordIsOnNoStreamUnlessTwoGatesAreOpen(t *testing.T) {
	machine(t)
	out, said, err := caught(t, master, "pass", "get", "example.org", "-show-secret", "-counter", "2")
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(out)
	if len(secret) != 20 {
		t.Fatalf("stdout held %q, which is not the password", out)
	}
	if strings.Contains(said, secret) {
		t.Errorf("the password was also on stderr: %q", said)
	}
	if strings.Contains(out, "correct horse") || strings.Contains(said, "correct horse") {
		t.Error("the master was printed back")
	}

	// The same command with one gate shut prints nothing secret, and says so.
	t.Setenv("ZDE_PASS_SHOW_SECRET", "")
	out, said, err = caught(t, master, "pass", "get", "example.org", "-show-secret")
	if err == nil {
		t.Fatal("-show-secret alone printed a password")
	}
	if strings.Contains(out+said+err.Error(), secret) {
		t.Errorf("the password leaked through the refusal: %q %q %v", out, said, err)
	}

	// And with neither gate, which is the ordinary path: it says that it
	// derived, and under what, and holds nothing back that is not the secret.
	t.Setenv("ZDE_PASS_SHOW_SECRET", "1")
	out, said, err = caught(t, master, "pass", "get", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out+said, secret) {
		t.Errorf("a plain `pass get` printed the password: %q %q", out, said)
	}
	for _, want := range []string{"counter 2", "argon2id v1", "20 characters"} {
		if !strings.Contains(out, want) {
			t.Errorf("`pass get` said %q, which does not say %q", out, want)
		}
	}
}

// A process's arguments are readable by anybody with an account on this machine
// for as long as it lives, so there must be no spelling of any pass verb that
// takes the master. The prompt is the only way in.
func TestTheMasterIsNeverACommandLineArgument(t *testing.T) {
	machine(t)
	for _, args := range [][]string{
		{"pass", "get", "example.org", "correct horse battery staple"},
		{"pass", "get", "example.org", "-master", "correct horse battery staple"},
		{"pass", "add", "example.com", "correct horse battery staple"},
	} {
		out, said, err := caught(t, "", args...)
		if err == nil {
			t.Errorf("`zde %s` was accepted", strings.Join(args, " "))
		}
		if strings.Contains(out+said, "correct horse") {
			t.Errorf("`zde %s` printed the argument back", strings.Join(args, " "))
		}
	}
}

// `zde pass` on its own stays a command nothing answers, because that is what
// the keymap's pass.open spawns and the trusted window is not written. The
// palette reads this to decide whether to mark the row as doing nothing
// (cmd/zde, TestLiveActionsAreTheOnesZdeKnows), so a verb added here without a
// window would quietly promise a key that opens nothing.
func TestTheTrustedWindowIsStillNotAVerb(t *testing.T) {
	machine(t)
	_, _, err := caught(t, "", "pass")
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("`zde pass` answered %v, and the window that would answer it is not written", err)
	}
}

// Rotating is a new password and nothing else, and the old one is still at the
// old number: that is what makes an increment nobody meant a thing to undo
// rather than an account nobody can get into.
func TestARotationSaysWhereTheCounterWasAndTheOldPasswordIsStillThere(t *testing.T) {
	machine(t)
	before, _, err := caught(t, master, "pass", "get", "example.org", "-show-secret")
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := caught(t, "", "pass", "rotate", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 -> 3") {
		t.Errorf("the rotation said %q, which does not say where the counter was", out)
	}
	after, _, err := caught(t, master, "pass", "get", "example.org", "-show-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(after) == strings.TrimSpace(before) {
		t.Fatal("rotating derived the same password")
	}
	// The old one, without touching the store.
	old, _, err := caught(t, master, "pass", "get", "example.org", "-show-secret", "-counter", "2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(old) != strings.TrimSpace(before) {
		t.Error("the password at the old counter is not the one that was there")
	}
	// And putting the counter back is one command.
	if _, _, err := caught(t, "", "pass", "counter", "example.org", "2"); err != nil {
		t.Fatal(err)
	}
	now, _, err := caught(t, master, "pass", "get", "example.org", "-show-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(now) != strings.TrimSpace(before) {
		t.Error("putting the counter back did not put the password back")
	}
}

// Moving a site onto today's Argon2id parameters is a different password, and
// the command has to say so: the person who runs it has to go and change that
// account before they close the terminal.
func TestAnUpgradeSaysItChangedThePassword(t *testing.T) {
	machine(t)
	out, _, err := caught(t, "", "pass", "upgrade", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "t=1 m=64KiB p=1 -> argon2id v1 t=3 m=65536KiB p=4") {
		t.Errorf("the upgrade said %q, which does not say what moved", out)
	}
	if !strings.Contains(out, "different password") {
		t.Errorf("the upgrade said %q, which does not say what it cost", out)
	}
	out, _, err = caught(t, "", "pass", "upgrade", "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "different password") {
		t.Errorf("a site already on today's parameters was told its password changed: %q", out)
	}
}

// Each of the failure paths as a person meets them, at the command rather than
// in the package: the answer has to name the file and say what to do.
func TestWhatTheCommandSaysWhenItWillNotDerive(t *testing.T) {
	machine(t)
	// A pepper somebody made group-readable.
	if err := os.Chmod(pass.PepperPath(), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := caught(t, master, "pass", "get", "example.org"); err == nil {
		t.Error("a group-readable pepper derived a password")
	} else if !strings.Contains(err.Error(), "0640") {
		t.Errorf("it said %q", err)
	}
	if err := os.Chmod(pass.PepperPath(), 0o600); err != nil {
		t.Fatal(err)
	}
	// A site nobody recorded, and the pepper is not even read for it: the
	// master is never asked for on a path that cannot end in a password.
	out, said, err := caught(t, "", "pass", "get", "exmaple.org")
	if err == nil {
		t.Error("a service nobody recorded was derived for")
	}
	if strings.Contains(said, "master password") {
		t.Errorf("it asked for the master before it knew the site: %q", said)
	}
	if out != "" {
		t.Errorf("it printed %q", out)
	}
	// And a missing pepper on a machine that has one recorded site.
	if err := os.Remove(pass.PepperPath()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := caught(t, master, "pass", "get", "example.org"); err == nil {
		t.Error("a machine with no pepper derived a password")
	} else if !strings.Contains(err.Error(), "zde pass init") {
		t.Errorf("it said %q", err)
	}
}

// add records a site, list prints what it recorded, and neither invents a
// policy the site did not ask for.
func TestAddingASiteRecordsWhatTheSiteTakes(t *testing.T) {
	machine(t)
	if _, _, err := caught(t, "", "pass", "add", "bank.example", "-length", "12", "-classes", "digit"); err != nil {
		t.Fatal(err)
	}
	out, _, err := caught(t, "", "pass", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bank.example", "12 chars, digit", "counter 1", "argon2id v1 t=3 m=65536KiB p=4"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing is %q, which does not say %q", out, want)
		}
	}
	// A site recorded twice is a mistake and not a second site.
	if _, _, err := caught(t, "", "pass", "add", "https://BANK.example/login"); err == nil {
		t.Error("the same site was recorded twice under two spellings")
	}
	// And a policy nothing can satisfy is refused here, where somebody can fix
	// it, rather than at the login form.
	if _, _, err := caught(t, "", "pass", "add", "other.example", "-classes", "digit", "-exclude", "0123456789"); err == nil {
		t.Error("a policy with no characters in it was recorded")
	}
	if _, err := os.Stat(filepath.Join(pass.Dir(), "sites.json")); err != nil {
		t.Fatalf("the store is not where it says it is: %v", err)
	}
}
