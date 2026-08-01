// Package bus is the one thing zde's three D-Bus users need from the library
// and cannot ask it for: a connect that gives up.
//
// godbus opens the socket, runs a SASL handshake and sends the opening Hello,
// and not one of those three carries a deadline. WithContext looks like the
// answer and is not: the context it takes becomes the connection's own, so a
// context with a timeout on it does not bound the connect, it closes the
// connection when the timeout lands.
//
// It matters because every one of these connects sits in a request path. zded
// answers each keybind on one socket and a client gives a call five seconds
// (internal/zded, Client.Call), so an unbounded connect against a bus that
// accepts and then says nothing is a keypress that never comes back, a lock
// held for as long as that lasts, and a logout that hangs until systemd loses
// patience with the session. That was measured, not imagined: one bluetooth
// question against such a socket left zded ignoring SIGTERM.
//
// A connect that runs out of time is abandoned rather than cancelled, because
// the library offers nothing to cancel it with. This stops waiting, and the
// goroutine still inside the handshake closes whatever it eventually gets, so
// what a lost connect costs is one goroutine and one socket until the kernel
// or the bus ends it. That is a bound worth keeping an eye on where something
// dials on a clock, which is why the network side remembers an absent manager
// rather than asking again every five seconds (internal/zded, links).
package bus

import (
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

// Within is how long a bus has to finish connecting.
//
// The same two seconds every other question put to these buses gets
// (internal/bt, askFor; internal/link, askFor), and for the same reason:
// connecting to a bus that is up is a unix socket and a handshake on it, which
// is microseconds, so any number here is a number about a machine that is
// unwell. Two also leaves room for the call that follows inside the five
// seconds the client is prepared to wait.
const Within = 2 * time.Second

// System is a private connection to the system bus, where bluetooth and
// NetworkManager both live.
func System() (*dbus.Conn, error) {
	return Connect(Within, func() (*dbus.Conn, error) { return dbus.ConnectSystemBus() })
}

// Session is the same for the session bus, which is where the notification
// name is taken.
func Session() (*dbus.Conn, error) {
	return Connect(Within, func() (*dbus.Conn, error) { return dbus.ConnectSessionBus() })
}

// Connect runs dial and gives up after within.
//
// The dial is an argument rather than a bus name because the bound is the part
// worth testing, and testing a bound needs a dial that hangs rather than a bus
// that does.
func Connect(within time.Duration, dial func() (*dbus.Conn, error)) (*dbus.Conn, error) {
	type dialled struct {
		conn *dbus.Conn
		err  error
	}
	// Buffered, so the goroutine finishes whether or not anybody is still here
	// to take what it opened.
	out := make(chan dialled, 1)
	go func() {
		conn, err := dial()
		out <- dialled{conn, err}
	}()
	select {
	case d := <-out:
		return d.conn, d.err
	case <-time.After(within):
	}
	// Nobody is holding this one now, so nobody would ever close it.
	go func() {
		if d := <-out; d.conn != nil {
			d.conn.Close()
		}
	}()
	return nil, fmt.Errorf("the bus did not finish connecting within %s", within)
}
