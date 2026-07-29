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

	// Matches what the QML writes: {"method":"queue.list"}. Deliberately not a
	// JSON parse - the line is a QML string literal with an escaped newline in
	// it, and the point is to find the names wherever they are written.
	re := regexp.MustCompile(`"method"\s*:\s*"([a-zA-Z.-]+)"`)
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
