package zded

import (
	"sort"
	"strings"
	"testing"
)

// QML method names are not compiled against the daemon; dispatch every reachable name.
func TestTheBarAsksForMethodsThatExist(t *testing.T) {
	src := readShell(t)
	asked := kindsIn(methodInQML, src)
	for method := range kindsIn(methodHandedOnInQML, src) {
		asked[method] = true
	}
	if len(asked) == 0 {
		t.Fatal("no method names found in the active shell: update the source scanners")
	}
	srv, machine := wireServer(t)
	methods := make([]string, 0, len(asked))
	for method := range asked {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	for _, method := range methods {
		got := srv.Dispatch(Request{Method: method})
		// Validation or missing-state errors are allowed; an unknown method is not.
		if strings.HasPrefix(got.Error, "unknown method") {
			t.Errorf("the shell asks for %q and zded does not have it: %s", method, got.Error)
		}
		for _, reached := range machine.reachedFor() {
			t.Errorf("answering %q %s: wire tests must use fakes, not the host", method, reached)
		}
	}
}

// Event constants and active QML consumers must agree in both directions.
func TestTheBarListensForEventsThatExist(t *testing.T) {
	found := kindsIn(kindInQML, readShell(t))
	if len(found) == 0 {
		t.Fatal("no event kinds found in the active shell: update the source scanner")
	}
	kinds := eventKinds(t)
	for kind := range found {
		if !kinds[kind] {
			t.Errorf("the shell reads event %q but no Event constant declares it", kind)
		}
		delete(kinds, kind)
	}
	for kind := range kinds {
		t.Errorf("zded sends event %q and no active shell component draws it", kind)
	}
}
