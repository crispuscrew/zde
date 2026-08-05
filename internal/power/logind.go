package power

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/crispuscrew/zde/internal/bus"
)

// The names logind fixes. Every one of these is API: they are what loginctl
// itself calls.
const (
	service   = "org.freedesktop.login1"
	mgrIface  = service + ".Manager"
	sessIface = service + ".Session"
	userIface = service + ".User"
	propsGet  = "org.freedesktop.DBus.Properties.Get"
	mgrPath   = dbus.ObjectPath("/org/freedesktop/login1")
)

// askFor bounds one question put to logind, the same two seconds every other
// bus question in zde gets (internal/link, askFor; internal/bt, askFor).
//
// It is a bound and not a guess: zded answers keybinds on one socket, a client
// gives a call five seconds (internal/zded, Client.Call), and a bus call with
// no deadline is a power menu that never opens because logind is waiting on a
// disk. Two leaves room inside the caller's five for the reply to get back.
const askFor = 2 * time.Second

// Logind is systemd-logind on the system bus.
type Logind struct{ conn *dbus.Conn }

// Open connects, or says there is nothing here to connect to.
//
// The absence is one of the answers, which is why it comes back as an error the
// caller can recognise rather than as a broken connection: a machine with no
// logind still has a power menu, with the lock on it and the reason on the rows
// that need one (internal/zded, powerChoices).
func Open() (Manager, error) {
	conn, err := bus.System()
	if err != nil {
		// No system bus, or one that will not finish saying hello. Bounded
		// there rather than here (internal/bus): a dial with no deadline is the
		// keypress that never comes back.
		return nil, fmt.Errorf("%w: %v", ErrNoLogind, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), askFor)
	defer cancel()
	var owned bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, service).Store(&owned); err != nil {
		conn.Close()
		return nil, err
	}
	if !owned {
		conn.Close()
		return nil, ErrNoLogind
	}
	return &Logind{conn: conn}, nil
}

// Alive is whether the connection is still worth keeping, asked for by the
// daemon that holds it (internal/zded, logins).
//
// It is the bus connection it answers about, not logind. godbus reads
// Connected() off the context it closes when the connection ends
// (conn.ctx.Err() == nil), and what ends that is the socket to the system bus
// going: the broker restarted, or the library giving up on a read. A
// systemd-logind restart is not one of those - zded is connected to the bus
// broker and not to logind, the name is activatable, and a call made after
// logind comes back is routed to the new one - so there is nothing here that
// has to notice it, and this would not.
//
// What it does cover is worth the line anyway: a connection that has closed
// answers every call with the same "connection closed" for ever, and without
// this zded would hold one and refuse every power action for the rest of the
// session.
func (l *Logind) Alive() bool { return l.conn != nil && l.conn.Connected() }

// Close gives the bus connection back: a dropped one leaks the socket and the
// two goroutines the library runs on it.
func (l *Logind) Close() error {
	if l.conn == nil {
		return nil
	}
	return l.conn.Close()
}

func (l *Logind) within() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), askFor)
}

func (l *Logind) call(ctx context.Context, path dbus.ObjectPath, method string, out []any, args ...any) error {
	c := l.conn.Object(service, path).CallWithContext(ctx, method, 0, args...)
	if c.Err != nil {
		return c.Err
	}
	if len(out) == 0 {
		return nil
	}
	return c.Store(out...)
}

// sessionRow and inhibitorRow are logind's own reply shapes. Named structs
// rather than []any, because the fields are positional on the wire and a
// reordering here would read one session's seat as another's name.
//
// sessionRow is ListSessionsEx's a(sussussbto) and not ListSessions's
// a(susso). The extra fields are the point: the sixth is the session class, and
// without it there is no way to tell a person sitting at this machine from a
// display manager's greeter or a background job (power.go, Session.counts).
type sessionRow struct {
	ID     string
	UID    uint32
	User   string
	Seat   string
	Leader uint32
	Class  string
	TTY    string
	Idle   bool
	IdleAt uint64
	Path   dbus.ObjectPath
}

type inhibitorRow struct {
	What string
	Who  string
	Why  string
	Mode string
	UID  uint32
	PID  uint32
}

// State is who is logged in and what is holding a power action off.
//
// One method for both because they are one question - what would make this
// refuse, and what would it cost - asked once when the menu opens and again
// when logind says no. Two round trips on a local system bus is microseconds;
// two methods would be two chances for the menu and the refusal to describe
// different moments.
//
// ListSessionsEx and not ListSessions, which is the call every example uses.
// The older one answers a(susso) and carries no session class, so a greeter or
// a background job belonging to another uid reads exactly like a person sitting
// at this machine - and logind's own rule for whether a reboot needs the
// multiple-sessions authorisation filters by class (power.go, Session.counts).
// Same round trip, wider struct.
//
// Nothing falls back to ListSessions if this is missing. ListSessionsEx is
// systemd v256 and later, zde's own pin is 258 (flake.lock, nixos-26.05), and a
// fallback would have to count every session of another uid as a person - which
// is the lie this call was chosen to stop telling, kept alive on the one kind of
// machine where nobody would be looking for it. A logind too old to answer says
// so, and the rows carry that as the reason they will not work.
func (l *Logind) State() (State, error) {
	ctx, cancel := l.within()
	defer cancel()

	var rows []sessionRow
	if err := l.call(ctx, mgrPath, mgrIface+".ListSessionsEx", []any{&rows}); err != nil {
		return State{}, err
	}
	mine := l.myID(ctx)
	st := State{}
	for _, r := range rows {
		st.Sessions = append(st.Sessions, Session{
			ID:    r.ID,
			User:  r.User,
			Seat:  r.Seat,
			Class: r.Class,
			Mine:  mine != "" && r.ID == mine,
		})
	}
	var held []inhibitorRow
	if err := l.call(ctx, mgrPath, mgrIface+".ListInhibitors", []any{&held}); err != nil {
		// Not a reason to have no answer about the sessions. The inhibitors are
		// what explains a refusal, and a menu that opens without them is a menu
		// with one fewer warning on it; a menu that refuses to open at all is a
		// key that does nothing.
		return st, nil
	}
	for _, h := range held {
		if h.Mode != "block" {
			// A delay inhibitor postpones a suspend by seconds so something can
			// save its work. It is not why anything is refused, and putting it
			// in front of somebody would be a warning that never comes true.
			continue
		}
		st.Blocks = append(st.Blocks, Block{What: h.What, Who: h.Who, Why: h.Why})
	}
	return st, nil
}

// myID is this session's logind id, and empty when nothing can say.
//
// Three ways, because the obvious one does not work where this runs.
// XDG_SESSION_ID is what pam_systemd puts in the session's environment, and it
// is exact when the session handed it to whatever started this. Failing that,
// logind is asked which session this process is in - which is exact for a
// process started from a terminal inside the session, and has no answer at all
// for one in user@.service, because that unit lives outside every session's
// cgroup and zded's unit is in it (nix/home.nix). So the last resort is the
// user's display session, which is the same fallback loginctl makes for the
// same reason.
//
// Empty is not a failure: it means no row is marked as this one, so a log out
// says which session it cannot find rather than ending somebody's at a guess.
func (l *Logind) myID(ctx context.Context) string {
	if id := os.Getenv("XDG_SESSION_ID"); id != "" {
		return id
	}
	var path dbus.ObjectPath
	if err := l.call(ctx, mgrPath, mgrIface+".GetSessionByPID", []any{&path}, uint32(os.Getpid())); err == nil {
		if id, err := l.sessionID(ctx, path); err == nil {
			return id
		}
	}
	var userPath dbus.ObjectPath
	if err := l.call(ctx, mgrPath, mgrIface+".GetUser", []any{&userPath}, uint32(os.Getuid())); err != nil {
		return ""
	}
	var display struct {
		ID   string
		Path dbus.ObjectPath
	}
	var v dbus.Variant
	if err := l.call(ctx, userPath, propsGet, []any{&v}, userIface, "Display"); err != nil {
		return ""
	}
	if err := v.Store(&display); err != nil {
		return ""
	}
	return display.ID
}

func (l *Logind) sessionID(ctx context.Context, path dbus.ObjectPath) (string, error) {
	var v dbus.Variant
	if err := l.call(ctx, path, propsGet, []any{&v}, sessIface, "Id"); err != nil {
		return "", err
	}
	var id string
	err := v.Store(&id)
	return id, err
}

// Do performs one, and turns a refusal into words on the way out.
//
// The words are made from a state read now rather than from the one the menu
// was drawn with: a browser can take a block inhibitor in the minute somebody
// spends looking at the confirmation, and a refusal explained by a stale
// reading would name the wrong thing or nothing at all.
func (l *Logind) Do(w What) error {
	ctx, cancel := l.within()
	defer cancel()

	var err error
	switch w {
	case Logout:
		err = l.terminate(ctx)
	case Suspend:
		err = l.shutdownOrSleep(ctx, "Suspend")
	case Reboot:
		err = l.shutdownOrSleep(ctx, "Reboot")
	case PowerOff:
		err = l.shutdownOrSleep(ctx, "PowerOff")
	default:
		return fmt.Errorf("logind has no %q", w)
	}
	if err == nil {
		return nil
	}
	// A state that cannot be read leaves Because with nothing to point at,
	// which is the answer it gives for that case: refused, and no reason on
	// this machine to name.
	st, _ := l.State()
	return Because(w, st, err)
}

// shutdownOrSleep is the three of these that are logind Manager methods of one
// shape - Suspend, Reboot and PowerOff, each taking one boolean - and the false
// is the whole reason they are one function. A log out is not among them: it is
// TerminateSession on this session's id, and it goes to terminate below.
//
// interactive=false asks polkit to answer now rather than to go looking for an
// authentication agent. A zde session has no polkit agent, so interactive would
// buy a wait for a dialog nobody is ever going to see, on the connection a
// keypress is waiting on - and then the key would be one that did nothing for
// twenty seconds. A refusal that arrives is a refusal the surface can show.
//
// The boolean is also all it is: the legacy Suspend(b) methods map it to
// SD_LOGIND_INTERACTIVE and nothing else (method_do_shutdown_or_sleep,
// src/login/logind-dbus.c), so a block inhibitor is never skipped by this call
// and comes back as its own refusal (power.go, Because).
func (l *Logind) shutdownOrSleep(ctx context.Context, method string) error {
	return l.call(ctx, mgrPath, mgrIface+"."+method, nil, false)
}

// terminate ends this session, which is what logging out is.
//
// The session object rather than the compositor: `niri msg action quit` would
// end the windows and leave the session, the greeter would not come back, and a
// power menu whose log out leaves you looking at a black screen with a session
// still running is worse than one that has no log out. It is also the verb that
// still works when the compositor is the thing that has wedged, which is when
// somebody most wants it.
func (l *Logind) terminate(ctx context.Context) error {
	id := l.myID(ctx)
	if id == "" {
		// Nothing to end, and emphatically not a guess at somebody else's
		// session: a log out that picked the wrong row would end another
		// person's work, from a key.
		return errors.New("logind cannot say which session this is, so there is none to end")
	}
	return l.call(ctx, mgrPath, mgrIface+".TerminateSession", nil, id)
}

// errorName is the D-Bus name of a refusal, and empty for anything that did not
// come off a bus. It is here rather than in power.go so that the pure half of
// this package - what an inhibitor stands in the way of, and what a refusal is
// said as - carries no bus import and can be read without one.
func errorName(err error) string {
	var derr dbus.Error
	if !errors.As(err, &derr) {
		return ""
	}
	return derr.Name
}
