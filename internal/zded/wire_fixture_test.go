package zded

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crispuscrew/zde/internal/link"
)

// Furnished fakes exercise surface paths; the trap catches any unfaked host access.
func wireServer(t *testing.T) (*Server, *theMachine) {
	t.Helper()
	machine := keepOffTheMachine(t)
	server := New("test", nil, &fakeCompositor{
		m:       twoDesks(),
		focused: "vshop.DP-1.code",
		output:  "DP-1",
		windows: []Window{{ID: 1, AppID: "term", Title: "a shell", Workspace: "vshop.DP-1.code"}},
	}, nil)
	t.Cleanup(func() { server.Close() })
	withLink(server, nil, link.ErrNoManager)
	withLogind(server, &fakeLogind{}, nil)
	server.launch = func(_ context.Context, address string) error {
		machine.note("started the app " + address)
		return errors.New("nothing starts a container from a unit test")
	}
	server.spawn = func(argv []string) error {
		machine.note("ran " + strings.Join(argv, " "))
		return errors.New("nothing starts a program from a unit test")
	}
	return server, machine
}

type theMachine struct {
	dir     string
	mu      sync.Mutex
	reached []string
}

// Redirect both buses and configuration paths; no dispatched method may use the host.
func keepOffTheMachine(t *testing.T) *theMachine {
	t.Helper()
	// A short path avoids the Unix socket pathname limit with long test names.
	directory, err := os.MkdirTemp("", "zd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	machine := &theMachine{dir: directory}
	socket := filepath.Join(directory, "bus")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			machine.note("opened a D-Bus connection")
			// EOF ends the authentication handshake immediately.
			conn.Close()
		}
	}()
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+socket)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+socket)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(directory, "config"))
	t.Setenv("HOME", directory)
	return machine
}

func (machine *theMachine) note(what string) {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	machine.reached = append(machine.reached, what)
}

// Drain per dispatch so failures identify the method that reached the host.
func (machine *theMachine) reachedFor() []string {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	out := machine.reached
	machine.reached = nil
	return out
}
