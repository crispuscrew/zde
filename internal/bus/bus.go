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
// answers each keybind on one socket and a client gives an ordinary call five
// seconds (internal/zded, Client.Call), so an unbounded connect against a bus that
// accepts and then says nothing is a keypress that never comes back, a lock
// held for as long as that lasts, and a logout that hangs until systemd loses
// patience with the session. That was measured, not imagined: one bluetooth
// question against such a socket left zded ignoring SIGTERM.
//
// A connect that runs out of time is cancelled by closing the socket it is
// waiting on, which is the only cancel there is: the library offers none, and
// walking away from one is not enough. That was measured too. Against a bus
// that accepts and then says nothing, the handshake parks in a read nothing
// will ever satisfy, so the goroutine never returns, so nobody ever closes
// what it opened - one socket and three goroutines an attempt, held until the
// process ends. The three are the one inside the handshake, the one waiting to
// close whatever it produces, and the one the library starts to watch the
// connection's context. Forty presses of the power key was forty of those, and
// a session out of file descriptors is a session with no power menu, no wifi
// list and no bar.
//
// Closing it needs the connection in hand while the part that hangs is still
// hanging, which is why the socket and the handshake are two steps here (see
// Connect) rather than the one call the library offers.
//
// That still leaves the dial worth doing rarely. A caller that asks on a clock
// pays the whole bound every time it asks, which is why the network side
// remembers an absent manager for five minutes rather than asking again every
// five seconds (internal/zded, links) and the power side remembers one for a
// minute (internal/zded, logins).
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

// System is a private connection to the system bus, where logind, bluetooth and
// NetworkManager all live.
//
// SystemBusPrivate and not ConnectSystemBus: the library documents the private
// one as "not ready to use. One must perform Auth and Hello on the connection
// before it is usable", and that is exactly the shape this package needs - the
// socket first, so there is something to close, and the handshake after (see
// Connect).
func System() (*dbus.Conn, error) {
	return Connect(Within, dbus.SystemBusPrivate)
}

// Session is the same for the session bus, which is where the notification
// name is taken.
func Session() (*dbus.Conn, error) {
	return Connect(Within, dbus.SessionBusPrivate)
}

// Connect opens a bus with open, finishes it, and gives up after within.
//
// open is the socket and nothing else. The handshake that makes it usable -
// the SASL exchange and the opening Hello, which is what ConnectSystemBus does
// after the same call - is run here instead, and that split is the whole point
// of this function: it is what puts a connection in this function's hands while
// the part that hangs is still hanging, so giving up can close the socket
// rather than walk away from it and leave the read parked on it for ever.
//
// One budget over both steps, because what a caller has is one deadline.
//
// open is an argument rather than a bus name because the bound is the part
// worth testing, and testing a bound needs a bus that hangs rather than one
// that answers.
func Connect(within time.Duration, open func(...dbus.ConnOption) (*dbus.Conn, error)) (*dbus.Conn, error) {
	deadline := time.After(within)
	type opened struct {
		conn *dbus.Conn
		err  error
	}
	// Buffered, so the goroutine finishes whether or not anybody is still here
	// to take what it opened.
	sock := make(chan opened, 1)
	go func() {
		conn, err := open()
		sock <- opened{conn, err}
	}()

	var conn *dbus.Conn
	select {
	case o := <-sock:
		if o.err != nil {
			return nil, o.err
		}
		conn = o.conn
	case <-deadline:
		// The one step that still has to be abandoned rather than cancelled,
		// because there is nothing yet to close. It is also the step that does
		// not hang on the buses zde talks to: opening a unix socket that is
		// being listened on returns at once or not at all. Whatever it opens
		// late is closed by whoever is left holding it, which is nobody.
		go func() {
			if o := <-sock; o.conn != nil {
				o.conn.Close()
			}
		}()
		return nil, tooSlow(within)
	}

	// Buffered for the same reason, and it is the reason this one is not a leak:
	// the Close below unblocks the read the handshake is parked in, so the
	// goroutine finishes, hands over its error and goes.
	shook := make(chan error, 1)
	go func() { shook <- handshake(conn) }()
	select {
	case err := <-shook:
		if err != nil {
			conn.Close() //nolint:errcheck // the connection is being given up on
			return nil, err
		}
		return conn, nil
	case <-deadline:
		conn.Close() //nolint:errcheck // the connection is being given up on
		return nil, tooSlow(within)
	}
}

// handshake is what the library's own Connect does after opening the socket.
func handshake(conn *dbus.Conn) error {
	if err := conn.Auth(nil); err != nil {
		return err
	}
	return conn.Hello()
}

func tooSlow(within time.Duration) error {
	return fmt.Errorf("the bus did not finish connecting within %s", within)
}
