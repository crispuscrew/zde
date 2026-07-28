// Package attn is what arrives while you are doing something else: the
// notification server, and the queue it feeds (docs/vision.md - "the queue
// protocol and per-desk display policies. Inside zded.").
//
// zded is the session's notification server rather than talking to one. A
// notification that only ever became a popup is a thing you either caught or
// missed; one that becomes a queue item on the desk it arrived on is a thing
// you can come back to (docs/model.md, section 3).
package attn

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/godbus/dbus/v5"
)

// The names the desktop notification spec fixes. Every app that has ever sent
// a notification expects exactly these.
const (
	busName   = "org.freedesktop.Notifications"
	busPath   = "/org/freedesktop/Notifications"
	busIface  = "org.freedesktop.Notifications"
	specLevel = "1.2"
)

// summaryMax bounds what one notification can put on one line of the queue. An
// app that sends an essay gets the first sentence of it.
const summaryMax = 300

// Notification is what an app said, narrowed to what the queue keeps.
type Notification struct {
	// From is the app's own claim about itself, and nothing checks it: any
	// client on the session bus may put any name here, including another
	// app's. It is recorded as a claim and used as one.
	//
	// When zinc gives each app its own filtered bus socket (docs/vision.md,
	// ask 4), which one a message arrived on will say who sent it, and this
	// becomes the fallback rather than the answer. Nothing here has to change
	// for that: the field stays, its source gets better.
	From string
	// Text is one printable line: the summary, or the first line of the body
	// when there is no summary.
	Text string
	// Urgent is the spec's urgency 2 (critical). Apps use it for what should
	// interrupt rather than wait, and attn's modes will read it.
	Urgent bool
	// Replaces is the id of a notification this supersedes, 0 for none. Apps
	// use it for progress: one download, many updates, one queue item.
	Replaces uint32
}

// Sink is where a notification goes. attn does not own the queue - the journal
// does, through zded - so this is the little of it that arriving needs.
type Sink interface {
	// Arrived puts one on the queue and answers with the id to address it by.
	Arrived(Notification) (uint32, error)
	// Closed takes one off, by an id Arrived gave out. An id nobody is waiting
	// on is not an error: it is not waiting either way.
	Closed(id uint32) error
}

// Server owns the notification bus name for as long as it is open.
type Server struct {
	conn    *dbus.Conn
	sink    Sink
	version string
}

// Serve connects to the session bus and takes the notification name.
//
// It refuses to share: the spec allows exactly one server, and two would mean
// notifications arriving in one of two places depending on who won a race. A
// session that already has one is not an error the daemon should die of,
// though - zded does the desks either way - so the caller decides what to do
// with the refusal.
func Serve(sink Sink, version string) (*Server, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	s := &Server{conn: conn, sink: sink, version: version}
	if err := conn.ExportAll(s, busPath, busIface); err != nil {
		conn.Close()
		return nil, err
	}
	// DoNotQueue: without it, losing the race leaves zded waiting to become
	// the server later, which is a session where notifications work after
	// something else exits and not before.
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		return nil, errors.New("another notification server already has " + busName)
	}
	return s, nil
}

// Close gives up the name and the connection.
func (s *Server) Close() error { return s.conn.Close() }

// Notify is the spec's one method that matters. The arguments are its order,
// not ours: icon, actions and timeout are accepted and dropped, because a
// queue has nowhere to put them until the notification center exists.
func (s *Server) Notify(
	app string,
	replaces uint32,
	icon string,
	summary string,
	body string,
	actions []string,
	hints map[string]dbus.Variant,
	timeout int32,
) (uint32, *dbus.Error) {
	text := oneLine(summary)
	if text == "" {
		// Some apps put everything in the body. Something is better than an
		// item that says nothing at all.
		text = oneLine(body)
	}
	if text == "" {
		return 0, dbus.MakeFailedError(errors.New("a notification with neither summary nor body says nothing"))
	}
	id, err := s.sink.Arrived(Notification{
		From:     oneLine(app),
		Text:     text,
		Urgent:   urgency(hints) == 2,
		Replaces: replaces,
	})
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	return id, nil
}

// CloseNotification is how an app takes back its own notification - a download
// that finished, a message read elsewhere.
func (s *Server) CloseNotification(id uint32) *dbus.Error {
	if err := s.sink.Closed(id); err != nil {
		return dbus.MakeFailedError(err)
	}
	// The spec wants the signal whether or not anything was there to close.
	// Reason 3: closed by a call to CloseNotification.
	s.conn.Emit(busPath, busIface+".NotificationClosed", id, uint32(3))
	return nil
}

// GetCapabilities says what this server does, and only that. Claiming "body"
// markup or "actions" would make apps send buttons nothing can press
// (docs/roadmap.md, cross-cutting: where a mechanism is partial, say so).
func (s *Server) GetCapabilities() ([]string, *dbus.Error) {
	return []string{"body", "persistence"}, nil
}

// GetServerInformation is what an app reads to decide what to send.
func (s *Server) GetServerInformation() (name, vendor, version, spec string, err *dbus.Error) {
	return "zded", "zde", s.version, specLevel, nil
}

// urgency reads the spec's hint: 0 low, 1 normal, 2 critical.
//
// The type is checked rather than converted, and that is deliberate. Storing
// the variant would take an int32 or a double and round it into a byte, so a
// sender that got the type wrong would still be claiming the one thing on a
// notification that buys it something - urgency is what will cross a quiet
// mode. The spec says byte; a byte is what counts.
func urgency(hints map[string]dbus.Variant) byte {
	v, ok := hints["urgency"]
	if !ok || v.Signature().String() != "y" {
		return 1
	}
	var b byte
	if err := v.Store(&b); err != nil {
		return 1
	}
	return b
}

// oneLine makes anything an app sends fit one line of the queue.
//
// Normalised rather than refused, which is the opposite of what `zde queue
// add` does with the same problem - and deliberately. A person typing a
// reminder can be told to try again; an app's notification is the only copy
// there will ever be of something that already happened.
func oneLine(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || unicode.IsSpace(r):
			space = b.Len() > 0
		case unicode.IsPrint(r):
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
		if b.Len() > summaryMax {
			break
		}
	}
	out := []rune(b.String())
	if len(out) > summaryMax {
		out = out[:summaryMax]
	}
	return strings.TrimSpace(string(out))
}
