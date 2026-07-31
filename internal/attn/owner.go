package attn

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
)

// noOwner is the bus's own way of saying nobody has the name. It is an error
// on the wire and an answer to us, which is the one distinction this file
// exists to make: "somebody else took it" and "nothing is a notification
// server here" are different sessions with different fixes.
const noOwner = "org.freedesktop.DBus.Error.NameHasNoOwner"

// Owner says who holds the notification name, for a doctor that has to explain
// why zded does not (docs/install.md, when it breaks). Empty and no error
// means nobody holds it.
//
// The answer is a description rather than a bus name on purpose. ":1.42" is
// the truth and tells nobody anything; the process behind it has a name a
// person recognises, and the bus will hand out its pid to anyone who asks.
// Which of those can be found out is this package's problem, since this
// package is the one that knows what a bus peer is.
func Owner() (string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", fmt.Errorf("session bus: %w", err)
	}
	defer conn.Close()

	bus := conn.BusObject()
	var unique string
	if err := bus.Call("org.freedesktop.DBus.GetNameOwner", 0, BusName).Store(&unique); err != nil {
		var derr dbus.Error
		if errors.As(err, &derr) && derr.Name == noOwner {
			return "", nil
		}
		return "", err
	}

	var pid uint32
	if err := bus.Call("org.freedesktop.DBus.GetConnectionUnixProcessID", 0, unique).Store(&pid); err != nil {
		// A peer the bus will not identify is still a peer holding the name,
		// and saying so beats saying nothing.
		return unique, nil
	}
	if comm := command(pid); comm != "" {
		return fmt.Sprintf("%s, pid %d", comm, pid), nil
	}
	return fmt.Sprintf("%s, pid %d", unique, pid), nil
}

// command is what a pid calls itself. /proc rather than anything heavier: the
// peer is on this machine by definition - it is on this session's bus - and
// the name is one short read away.
func command(pid uint32) string {
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(comm))
}
