package pass

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Two spellings of one site must not be two passwords, and the store is where
// that is decided. What Canonical takes off is noise anybody's browser adds;
// what it leaves alone is a name, because guessing that mail.google.com is
// google.com would be zde deciding somebody's accounts are one account.
func TestOneSiteSpelledSeveralWays(t *testing.T) {
	for _, c := range []struct{ typed, want string }{
		{"github.com", "github.com"},
		{"GitHub.com", "github.com"},
		{"  github.com  ", "github.com"},
		{"https://github.com", "github.com"},
		{"https://github.com/crispuscrew/zde", "github.com"},
		{"www.github.com", "github.com"},
		{"HTTPS://WWW.GitHub.com/login?next=%2F", "github.com"},
		{"github.com.", "github.com"},
	} {
		if got := Canonical(c.typed); got != c.want {
			t.Errorf("%q is looked up as %q and should be %q", c.typed, got, c.want)
		}
	}
	if Canonical("mail.google.com") == Canonical("google.com") {
		t.Error("two hosts were guessed to be one account")
	}
}

func store(t *testing.T) (string, *Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sites.json")
	s := &Store{Version: storeVersion}
	site := &Site{Service: "github.com", Counter: 1, Params: cheap(), Policy: DefaultPolicy()}
	if err := s.Add(site); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	return path, s
}

// A name nobody recorded is refused, and this is the rule that makes the whole
// thing usable. Deriving from whatever was typed would answer a typo with a
// password that is not wrong in any way anybody can see: it just does not open
// the account, and the person retypes their master looking for the mistake.
func TestAServiceNobodyRecordedIsNotDerivedFor(t *testing.T) {
	path, _ := store(t)
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find("githbu.com"); err == nil {
		t.Fatal("a service nobody added was found")
	} else if !strings.Contains(err.Error(), "zde pass add") {
		t.Errorf("an unknown service answered %q, which does not say how to record one", err)
	}
	for _, spelling := range []string{"github.com", "GitHub.com", "https://www.github.com/x"} {
		if _, err := s.Find(spelling); err != nil {
			t.Errorf("%q did not find the site it names: %v", spelling, err)
		}
	}
}

// An alias is the answer to "the same account under another name", and it has
// to derive the same password - that is the entire point of it. It also may not
// take a name something else already answers to.
func TestAnAliasIsTheSamePasswordAndNotASecondSite(t *testing.T) {
	path, _ := store(t)
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	site, err := s.Find("github.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Alias(site, "GitHub"); err != nil {
		t.Fatal(err)
	}
	found, err := s.Find("github")
	if err != nil {
		t.Fatal(err)
	}
	if found.Service != site.Service {
		t.Fatalf("the alias found %s", found.Service)
	}
	// The derivation input is Service and never the name that found it.
	one, err := Derive([]byte(fixedMaster), fixedPepper(), found.Service, found.Counter, found.Params, found.Policy)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Derive([]byte(fixedMaster), fixedPepper(), site.Service, site.Counter, site.Params, site.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if string(one) != string(two) {
		t.Error("a site and its alias derived two different passwords")
	}
	if err := s.Alias(site, "github.com"); err == nil {
		t.Error("a site took an alias that is already a name here")
	}
	if err := s.Add(&Site{Service: "github", Counter: 1, Params: cheap(), Policy: DefaultPolicy()}); err == nil {
		t.Error("a second site took a name an alias already answers to")
	}
}

// The parameters are recorded with the site, which is the thing that makes a
// future change to Default survivable: an old site keeps deriving what it
// derived, and moving it is a verb somebody types.
func TestASiteKeepsTheParametersItWasRecordedUnder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sites.json")
	// A file as an older zde would have left it, with the parameters of its
	// day. Written out rather than built with Save, because what is under test
	// is reading somebody else's file.
	const file = `{
  "version": 1,
  "sites": [
    {
      "service": "example.org",
      "counter": 3,
      "params": { "version": 1, "time": 1, "memory_kib": 64, "lanes": 1 },
      "policy": { "length": 20, "allow": ["lower","upper","digit","symbol"],
                  "require": {"lower":1,"upper":1,"digit":1,"symbol":1} }
    }
  ]
}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	site, err := s.Find("example.org")
	if err != nil {
		t.Fatal(err)
	}
	if site.Params != (Params{Version: 1, Time: 1, MemoryKiB: 64, Lanes: 1}) {
		t.Fatalf("the file said t=1 m=64 p=1 and the site is on %+v", site.Params)
	}
	old, err := Derive([]byte(fixedMaster), fixedPepper(), site.Service, site.Counter, site.Params, site.Policy)
	if err != nil {
		t.Fatal(err)
	}
	// And an upgrade is a different password, said out loud rather than
	// discovered at the login form.
	if !site.Restamp(Default()) {
		t.Fatal("re-stamping a site on old parameters changed nothing")
	}
	now, err := Derive([]byte(fixedMaster), fixedPepper(), site.Service, site.Counter, site.Params, site.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if string(old) == string(now) {
		t.Error("today's parameters derived what yesterday's did, so the recorded ones are not used")
	}
	if site.Restamp(Default()) {
		t.Error("re-stamping a site that is already on today's parameters said something moved")
	}
}

// Every way the file can be wrong, and each one refuses the whole file. Half a
// store loading would be a machine that derives for some accounts and cannot
// say why not for the others.
func TestAStoreFileThatIsWrongIsRefusedWhole(t *testing.T) {
	for _, c := range []struct{ what, file, says string }{
		{"a version this zde does not read", `{"version":99,"sites":[]}`, "version 99"},
		{"a key nothing here means", `{"version":1,"sites":[],"master":"hunter2"}`, "unknown field"},
		{"a counter of zero", `{"version":1,"sites":[{"service":"a.com","counter":0,"params":{"version":1,"time":1,"memory_kib":64,"lanes":1},"policy":{"length":8,"allow":["lower"]}}]}`, "starts at 1"},
		{"parameters that would take the machine", `{"version":1,"sites":[{"service":"a.com","counter":1,"params":{"version":1,"time":1,"memory_kib":1073741824,"lanes":1},"policy":{"length":8,"allow":["lower"]}}]}`, "memory"},
		{"a policy nothing satisfies", `{"version":1,"sites":[{"service":"a.com","counter":1,"params":{"version":1,"time":1,"memory_kib":64,"lanes":1},"policy":{"length":8,"allow":["lower"],"require":{"digit":1}}}]}`, "does not allow"},
		{"one name for two sites", `{"version":1,"sites":[{"service":"a.com","counter":1,"params":{"version":1,"time":1,"memory_kib":64,"lanes":1},"policy":{"length":8,"allow":["lower"]}},{"service":"b.com","aliases":["a.com"],"counter":1,"params":{"version":1,"time":1,"memory_kib":64,"lanes":1},"policy":{"length":8,"allow":["lower"]}}]}`, "names both"},
		{"a service that is not a name", `{"version":1,"sites":[{"service":"a b","counter":1,"params":{"version":1,"time":1,"memory_kib":64,"lanes":1},"policy":{"length":8,"allow":["lower"]}}]}`, "space"},
		{"something that is not the file at all", `hello`, "invalid"},
	} {
		path := filepath.Join(t.TempDir(), "sites.json")
		if err := os.WriteFile(path, []byte(c.file), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := LoadStore(path)
		if err == nil {
			t.Errorf("%s loaded, with %d sites", c.what, len(s.Sites))
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s was refused with %q, which does not say %q", c.what, err, c.says)
		}
	}
}

// A machine that has never recorded a site has no file, and that is not a
// fault: `zde pass list` on a fresh install has to say so rather than fail.
func TestNoStoreYetIsAnEmptyStore(t *testing.T) {
	s, err := LoadStore(filepath.Join(t.TempDir(), "sites.json"))
	if err != nil {
		t.Fatalf("a machine with no sites yet: %v", err)
	}
	if len(s.Sites) != 0 {
		t.Errorf("%d sites came from nowhere", len(s.Sites))
	}
}

// The counters are not secret and the list of accounts somebody holds is still
// nobody else's, so the file is 0600 - and it survives being written, which is
// what a rename over a temporary buys.
func TestTheStoreIsWrittenPrivatelyAndWhole(t *testing.T) {
	path, want := store(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("the store is mode %04o and has to be 0600", fi.Mode().Perm())
	}
	got, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sites) != len(want.Sites) {
		t.Fatalf("wrote %d sites and read %d", len(want.Sites), len(got.Sites))
	}
	if !reflect.DeepEqual(got.Sites[0], want.Sites[0]) {
		t.Errorf("wrote %+v and read %+v", want.Sites[0], got.Sites[0])
	}
}
