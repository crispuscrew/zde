package zded

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
	path := filepath.Join("..", "..", "shell", "shell.qml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	// Both ways the QML writes one: as JSON text, {"method":"queue.list"}, and
	// as a QML object passed to JSON.stringify, where the key is bare. The
	// second form is how the picker sends desk.switch, and requiring the quotes
	// meant this test could not see the one method it was most likely to miss.
	// Deliberately not a JSON parse: these are string literals and object
	// literals in a QML file, and the point is to find the names wherever they
	// are written.
	re := regexp.MustCompile(`"?method"?\s*:\s*"([a-zA-Z.-]+)"`)
	found := re.FindAllStringSubmatch(string(src), -1)
	if len(found) == 0 {
		t.Fatalf("no method name found in %s, so this test is checking nothing: "+
			"either the bar stopped speaking the protocol directly, and this test "+
			"should go, or it spells the request differently now, and the regexp "+
			"should follow it", path)
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
func TestTheBarListensForEventsThatExist(t *testing.T) {
	path := filepath.Join("..", "..", "shell", "shell.qml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// How the QML asks: msg.event.kind === "picker".
	re := regexp.MustCompile(`\.kind\s*===\s*"([a-z-]+)"`)
	found := re.FindAllStringSubmatch(string(src), -1)
	if len(found) == 0 {
		t.Fatalf("no event kind found in %s, so this test is checking nothing: "+
			"either the shell stopped reading events, and this test should go, "+
			"or it spells the comparison differently now", path)
	}
	kinds := map[string]bool{EventPicker: true, EventWindows: true, EventCenter: true}
	for _, m := range found {
		if !kinds[m[1]] {
			t.Errorf("the shell listens for the event kind %q and zded never sends it", m[1])
		}
		delete(kinds, m[1])
	}
	for kind := range kinds {
		t.Errorf("zded sends the event kind %q and nothing in the shell draws it", kind)
	}
}
