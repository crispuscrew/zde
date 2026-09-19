package zded

import (
	"os"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/bus"
	"github.com/crispuscrew/zde/internal/keymap"
)

// The trap must fail a real bus dial even when all current methods use fakes.
func TestTheWireServerKeepsADispatchOffTheMachine(t *testing.T) {
	machine := keepOffTheMachine(t)
	if conn, err := bus.System(); err == nil {
		conn.Close()
		t.Fatal("the system bus answered a unit test")
	}
	if reached := machine.reachedFor(); len(reached) != 1 {
		t.Fatalf("system bus dial bypassed the trap: %v", reached)
	}
	if conn, err := bus.Session(); err == nil {
		conn.Close()
		t.Fatal("the session bus answered a unit test")
	}
	if reached := machine.reachedFor(); len(reached) != 1 {
		t.Fatalf("session bus dial bypassed the trap: %v", reached)
	}
	for _, would := range []struct{ what, path string }{
		{"desk.reconcile would write", dynamicPath()},
		{"palette.list would read", keymap.TextPath()},
		{"ask.run would read", apps.Path(askFile)},
		{"system.lock-preset would read", apps.Path(lockFile)},
		{"desk.panic would read", apps.Path(panicFile)},
	} {
		if !strings.HasPrefix(would.path, machine.dir+string(os.PathSeparator)) {
			t.Errorf("%s %s, outside this test's directory", would.what, would.path)
		}
	}
}
