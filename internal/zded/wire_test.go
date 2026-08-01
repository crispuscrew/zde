package zded

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The names on the wire, in the two files that have to agree about them.
//
// Every name in this protocol is dotted - desk.switch, attn.mode, and a kind
// like ask.panel - so every pattern here has to be able to see a dot. One that
// could not was worse than no pattern at all: the scan that looks for it went
// blind while the scan that demands it did not, and the test failed on a tree
// where nothing was wrong.
var (
	// Both ways the QML writes a method: as JSON text, {"method":"queue.list"},
	// and as a QML object passed to JSON.stringify, where the key is bare. The
	// second form is how the picker sends desk.switch, and requiring the quotes
	// meant this could not see the one method it was most likely to miss.
	// Deliberately not a JSON parse: these are string literals and object
	// literals in a QML file, and the point is to find the names wherever they
	// are written.
	methodInQML = regexp.MustCompile(`"?method"?\s*:\s*"([a-zA-Z0-9.-]+)"`)
	// How the QML asks which event it is: msg.event.kind === "picker".
	kindInQML = regexp.MustCompile(`\.kind\s*===\s*"([a-zA-Z0-9.-]+)"`)
	// How zded declares one. The convention is the contract: a kind zded can
	// broadcast is a constant named Event<Something> in this package, whether it
	// stands alone or sits in a const block. One declared under another name is
	// not found here, and this file will call it undrawn.
	kindInGo = regexp.MustCompile(`\bEvent[A-Za-z0-9]*\s*=\s*"([a-zA-Z0-9.-]+)"`)
)

// The bar talks to zded directly, in QML, over the same socket `zde` uses. That
// makes this protocol something with two consumers and only one of them
// compiled: the method name in shell/shell.qml is a string in a file nothing
// else reads, so renaming a method here leaves zded answering "unknown method",
// the bar showing an empty queue for ever, and every test in the repo green.
//
// So this is the test that fails instead. It reads the method names out of the
// QML and asks the real dispatcher about each one.
func TestTheBarAsksForMethodsThatExist(t *testing.T) {
	found := methodInQML.FindAllStringSubmatch(string(readShell(t)), -1)
	if len(found) == 0 {
		t.Fatalf("no method name found in the shell, so this test is checking nothing: "+
			"either the bar stopped speaking the protocol directly, and this test "+
			"should go, or it spells the request differently now, and %s "+
			"should follow it", methodInQML)
	}

	srv := New("test", nil, nil, nil)
	for _, m := range found {
		method := m[1]
		got := srv.Dispatch(Request{Method: method})
		// A method can legitimately refuse this call - queue.list needs a
		// journal, and there is none here. What it must not do is not exist.
		if strings.HasPrefix(got.Error, "unknown method") {
			t.Errorf("the bar asks for %q and zded does not have it: %s", method, got.Error)
		}
	}
}

// The same hole, one door along: an event kind is a string in the Go source and
// a string in the QML, and nothing compiles either against the other. Renaming
// EventCenter would leave zded broadcasting a kind the shell ignores, so Mod+n
// would open nothing and print nothing - and every test in the repo would stay
// green, because the daemon's half still works perfectly.
//
// The kinds are read out of the Go source rather than written down here. A list
// in a test is a list somebody has to remember to update, and this one sits
// where several branches land at once: each of them adds a surface, each adds a
// kind, and a written list would have turned this red on every one of those
// merges while the tree was fine.
func TestTheBarListensForEventsThatExist(t *testing.T) {
	found := kindInQML.FindAllStringSubmatch(string(readShell(t)), -1)
	if len(found) == 0 {
		t.Fatalf("no event kind found in the shell, so this test is checking nothing: "+
			"either the shell stopped reading events, and this test should go, "+
			"or it spells the comparison differently now and %s should follow it", kindInQML)
	}
	kinds := eventKinds(t)
	for _, m := range found {
		if !kinds[m[1]] {
			t.Errorf("the shell listens for the event kind %q and no Event constant in this package has that value "+
				"(a kind zded can send is a constant named Event<Something>, which is what this test scans for)", m[1])
		}
		delete(kinds, m[1])
	}
	for kind := range kinds {
		t.Errorf("zded sends the event kind %q and nothing in the shell draws it", kind)
	}
}

// Both scanners, handed source of their own. This is the test for the test: the
// thing that broke here was not the wiring but the reading of it, and a pattern
// that cannot see a dotted name fails in two different directions at once -
// silently in the scan that looks for one, loudly in the scan that demands it.
func TestTheWireScannersSeeADottedName(t *testing.T) {
	kinds := kindsIn(kindInGo, []byte(`
const EventPicker = "picker"

const (
	// A kind with a dot in it, which is the shape a branch adding a surface
	// with more than one of them reaches for.
	EventAskPanel = "ask.panel"
	EventNet      = "connections"
)
`))
	for _, want := range []string{"picker", "ask.panel", "connections"} {
		if !kinds[want] {
			t.Errorf("the Go scan did not find the kind %q: %v", want, kinds)
		}
	}

	drawn := kindsIn(kindInQML, []byte(`if (msg.event.kind === "ask.panel") root.openAsk(msg.event);`))
	if !drawn["ask.panel"] {
		t.Errorf("the QML scan did not find a dotted kind: %v", drawn)
	}

	// And the methods, which have been dotted since the first one: every verb
	// in this protocol is group.verb.
	methods := kindsIn(methodInQML, []byte(`stream.write('{"method":"attn.mode"}\n');`))
	if !methods["attn.mode"] {
		t.Errorf("the method scan did not find a dotted method: %v", methods)
	}
}

// eventKinds is every kind this package can broadcast, read out of its own
// source. Every .go file rather than events.go alone: a branch that adds a
// surface declares its kind next to the code that sends it, and a scan that
// looked in one file would call that kind undeclared.
func eventKinds(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	kinds := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for kind := range kindsIn(kindInGo, src) {
			kinds[kind] = true
		}
	}
	if len(kinds) == 0 {
		t.Fatal("no Event constant found in this package, so this test is checking nothing: " +
			"either the events went away, or they are declared under a name this scan does not know")
	}
	return kinds
}

// kindsIn is the first capture of every match, as a set.
func kindsIn(re *regexp.Regexp, src []byte) map[string]bool {
	out := map[string]bool{}
	for _, m := range re.FindAllSubmatch(src, -1) {
		out[string(m[1])] = true
	}
	return out
}

func readShell(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "shell", "shell.qml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return src
}
