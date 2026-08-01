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
	"sync"
	"unicode"

	"github.com/godbus/dbus/v5"
)

// The names the desktop notification spec fixes. Every app that has ever sent
// a notification expects exactly these.
//
// BusName is exported because a session where it is not zded's is a session
// where notifications go somewhere nobody is looking, and doctor has to be
// able to say which name it means (internal/doctor).
const (
	BusName   = "org.freedesktop.Notifications"
	busPath   = "/org/freedesktop/Notifications"
	busIface  = "org.freedesktop.Notifications"
	specLevel = "1.2"
)

// The spec's reasons for NotificationClosed. A client that waits for one -
// notify-send --wait does - waits for ever if it never comes.
const (
	ReasonDismissed = 2 // taken off by the person, which for zde is queue done
	ReasonClosed    = 3 // the sending app called CloseNotification
)

// DefaultAction is the spec's name for the action a notification means when it
// is chosen rather than when one of its buttons is pressed: open the message,
// show the download. It keeps a distinction of its own in the center - it is
// what Enter does, where the rest are a key each - because it is the one action
// a sender says is the obvious thing to do.
const DefaultAction = "default"

// actionsMax is how many of a notification's actions are kept, and it is the
// number the notification center can offer with one keypress each (digits 1 to
// 9). Bounded because the list comes from an app on the session bus and nothing
// stops one sending a thousand: the history holds hundreds of records, and
// unbounded lists inside a bounded list is not a bound.
//
// What was declared beyond it is counted rather than forgotten, so the surface
// can say there are actions it cannot reach instead of quietly showing fewer
// than the app offered. The day the center can offer more than nine, this moves
// with it.
const actionsMax = 9

// Action is one thing a sender says can be done about a notification: the key
// it wants back, and the label a person reads.
type Action struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// summaryMax bounds what one notification puts on one line of the queue,
// counted the way a person counts: in characters. An app that sends an essay
// gets the front of it.
const summaryMax = 300

// bodyMax bounds the rest of the message.
//
// Its own bound, and a much larger one, because the body is the part somebody
// comes back to the notification center to read: at the summary's 300 it was
// cutting an ordinary two-paragraph message in half, which made "it lands in
// history with what was sent" a claim the code did not keep.
//
// Still bounded, because the body is attacker-controlled and it is what decides
// how large the history can get. The arithmetic: HistoryMax is 200 records, so
// 200 x 4000 characters is about 3 MB if every record is at its limit and
// written in an alphabet that costs four bytes a character - and a few hundred
// kilobytes in any session made of real notifications.
const bodyMax = 4000

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
	// Body is the rest of what was sent: its lines as the app wrote them, up to
	// bodyMax characters. Kept because a notification is meant to land in
	// history with what it said and not with a headline (docs/vision.md,
	// principle 3), and the center is what shows it.
	Body string
	// Urgent is the spec's urgency 2 (critical). Apps use it for what should
	// interrupt rather than wait, and focus mode is what reads it (mode.go).
	Urgent bool
	// Actions is what the sender says can be done about it, in the order it
	// sent them, bounded by actionsMax. Every one of them is offered in the
	// notification center, which is what lets this server claim the spec's
	// "actions" capability (see GetCapabilities) - a claim that would be a lie
	// if only the default were reachable.
	Actions []Action
	// Extra is how many more the sender declared than were kept. Counted so the
	// center can say that some cannot be reached from here, which is the honest
	// version of a list that quietly ends.
	Extra int
}

// Sink is where a notification goes. attn does not own the queue - the journal
// does, through zded - so this is the little of it that arriving needs.
type Sink interface {
	// Arrived puts one on the queue and answers with the id to address it by.
	Arrived(Notification) (uint64, error)
	// Closed takes one off, by an id Arrived gave out.
	Closed(id uint64) error
}

// Server owns the notification bus name for as long as it is open.
type Server struct {
	conn    *dbus.Conn
	sink    Sink
	version string
	// emit sends NotificationClosed. A field rather than a call on the
	// connection so that what is emitted can be watched without a bus.
	emit func(id uint64, reason uint32)
	// act sends ActionInvoked, the other half of the same arrangement.
	act func(id uint64, key string)
	// holds says whether a bus name still has an owner, which is how invoking
	// an action can tell "sent" from "sent to nobody". A field for the same
	// reason as the two above.
	holds func(sender dbus.Sender) bool

	mu sync.Mutex
	// mine is what each connection has called its own notifications: sender
	// and the id it used, to the queue item that became. It is what stops one
	// app closing another's - and anybody's closing a reminder a person typed.
	mine map[owned]uint64
	// by is the same table read the other way: the item, to the names it is
	// known by - at most two, the id this server gave and the one the sender
	// asked for. It is what makes forgetting a notification, and finding out
	// who sent one, a lookup instead of a walk over everything the session has
	// received since it started.
	by map[uint64][]owned
}

// owned is a notification as its sender addresses it. The sender is the bus's
// own name for the connection, handed out by the bus rather than claimed by
// the peer, so unlike the app name in a notification it cannot be borrowed.
type owned struct {
	sender dbus.Sender
	id     uint32
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
	s := &Server{conn: conn, sink: sink, version: version, mine: map[owned]uint64{}, by: map[uint64][]owned{}}
	s.emit = func(id uint64, reason uint32) {
		conn.Emit(busPath, busIface+".NotificationClosed", uint32(id), reason)
	}
	s.act = func(id uint64, key string) {
		conn.Emit(busPath, busIface+".ActionInvoked", uint32(id), key)
	}
	// NameHasOwner, asked of the bus itself. A signal is a broadcast and says
	// nothing about who heard it, so this is the only way to tell somebody that
	// the app they are trying to act on has exited. A bus that will not answer
	// counts as still there: refusing to invoke because the check failed would
	// turn a bad moment on the bus into a key that does nothing.
	s.holds = func(sender dbus.Sender) bool {
		var has bool
		if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, string(sender)).Store(&has); err != nil {
			return true
		}
		return has
	}
	// Exported through a type that has these four methods and nothing else.
	// ExportAll puts every exported method of what it is given on the bus, so
	// handing it the Server would publish Close - and anything on the session
	// bus could then end notifications for the rest of the session by calling
	// it. That bus is exactly the surface a sandboxed app is meant to have
	// (docs/vision.md, principle 7), so this is reachable by design.
	iface := &notifications{server: s}
	if err := conn.ExportAll(iface, busPath, busIface); err != nil {
		conn.Close()
		return nil, err
	}
	// What the diagnostic tools read. Without it gdbus cannot call Notify
	// without being told every argument's type by hand, which is a poor
	// welcome for whoever is finding out why notifications are not arriving.
	if err := conn.Export(introspectable(), busPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		conn.Close()
		return nil, err
	}
	// DoNotQueue: without it, losing the race leaves zded waiting to become
	// the server later, which is a session where notifications work after
	// something else exits and not before.
	reply, err := conn.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		return nil, errors.New("another notification server already has " + BusName)
	}
	return s, nil
}

// Close gives up the name and the connection. Not on the bus: see Serve.
func (s *Server) Close() error { return s.conn.Close() }

// Dismissed says a notification is gone for a reason the sender did not ask
// for - somebody finished it. A client blocked on its closure (notify-send
// --wait is one) waits for exactly this.
func (s *Server) Dismissed(id uint64) {
	s.forget(id)
	s.emitClosed(id, ReasonDismissed)
}

// Invoke fires one of a notification's actions: the key the sender asked for
// back, sent as the spec's ActionInvoked. Which key is the caller's to say -
// zded checks it against what that notification declared, because the list is
// the record's and this half only knows the bus.
//
// It refuses rather than emitting into nothing. ActionInvoked is a broadcast,
// so a signal for an app that has exited goes out and is heard by nobody, and
// the person is left looking at a row that did something invisible. The two
// refusals are different facts and say so: nothing is holding this id any more
// (it was replaced, or finished), and the app that sent it has gone.
func (s *Server) Invoke(id uint64, key string) error {
	sender, ok := s.senderOf(id)
	if !ok {
		return fmt.Errorf("nothing on the bus is holding notification %d any more", id)
	}
	// Only where there is a bus to ask. A server built without a connection is
	// a test one, and treating that as "the app is gone" would make this method
	// untestable rather than safe.
	if s.holds != nil && !s.holds(sender) {
		return fmt.Errorf("the app that sent this has exited, so there is nothing left to act on")
	}
	if s.act == nil {
		return errors.New("no bus to invoke it on")
	}
	s.act(id, key)
	return nil
}

// senderOf is the connection that sent the notification with this id. Every
// name for one item belongs to the connection that sent it, so the first is as
// good as any.
func (s *Server) senderOf(id uint64) (dbus.Sender, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := s.by[id]
	if len(names) == 0 {
		return "", false
	}
	return names[0].sender, true
}

// remember records a name a sender can address a notification by.
//
// A name that already pointed at something else is moved rather than copied. It
// happens: a sender reusing a fixed id (notify-send -r 42) and the journal
// later handing out 42 to that same sender are the same key, and leaving it
// listed under both would let forgetting the older one delete the live entry
// for the newer.
func (s *Server) remember(k owned, id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mine == nil {
		s.mine = map[owned]uint64{}
	}
	if s.by == nil {
		s.by = map[uint64][]owned{}
	}
	if old, taken := s.mine[k]; taken {
		s.by[old] = drop(s.by[old], k)
		if len(s.by[old]) == 0 {
			delete(s.by, old)
		}
	}
	s.mine[k] = id
	s.by[id] = append(s.by[id], k)
}

// drop removes one name from a list of them. The lists are one or two long, so
// this is a loop and not an index.
func drop(names []owned, k owned) []owned {
	out := names[:0]
	for _, n := range names {
		if n != k {
			out = append(out, n)
		}
	}
	return out
}

func (s *Server) emitClosed(id uint64, reason uint32) {
	if s.emit == nil {
		return
	}
	s.emit(id, reason)
}

// Forget drops every name a sender had for a notification that nothing can
// address any more.
//
// Exported because the daemon is what knows when that moment is: a record that
// has fallen off the end of the history cannot be dismissed or invoked by
// anybody, so nothing needs to remember who sent it (internal/zded, Arrived).
// Without that the table grew one entry per notification for the life of the
// session - and the modes made it worse, because a notification a mode keeps
// off the queue is one nobody can finish, so nothing else would ever prune it.
func (s *Server) Forget(id uint64) { s.forget(id) }

func (s *Server) forget(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.by[id] {
		delete(s.mine, k)
	}
	delete(s.by, id)
}

// notifications is the object on the bus: these four methods and nothing else.
type notifications struct{ server *Server }

// Notify is the spec's one method that matters. The arguments are its order,
// not ours: icon and timeout are accepted and dropped, because a queue has
// nowhere to put them. Of the actions, only whether there is a default one is
// kept - that is the single action a list of rows can offer (see Invoke), and
// the labels belong to buttons nothing draws yet.
func (n *notifications) Notify(
	sender dbus.Sender,
	app string,
	replaces uint32,
	icon string,
	summary string,
	body string,
	actions []string,
	hints map[string]dbus.Variant,
	timeout int32,
) (uint32, *dbus.Error) {
	s := n.server
	text := oneLine(summary)
	rest := bodyText(body)
	if text == "" {
		// Some apps put everything in the body, and a notification with no
		// summary is ordinary: notify-send with one argument sends one. The
		// front of the message becomes the summary and is bounded like one,
		// because that is the line the queue and the center's row draw.
		//
		// The body stays. Discarding it here was a notification losing
		// everything past 300 characters for no reason but which field it
		// arrived in - the one shape where zde kept less than the app sent,
		// against a principle that says what arrives lands in history whole
		// (docs/vision.md, principle 3).
		text = oneLine(body)
		if rest == text {
			// The whole message fitted on the line. Keeping it as well would
			// have the center draw the same sentence twice, once as the row
			// and once underneath it.
			rest = ""
		}
	}
	if text == "" {
		return 0, dbus.MakeFailedError(errors.New("a notification with neither summary nor body says nothing"))
	}

	// Replacing is how one download stays one queue item through a hundred
	// updates. Only this sender's own notifications can be replaced, and only
	// by the name this sender gave them: ids are the journal's and small, so
	// without that an app could replace a reminder somebody typed.
	if replaces != 0 {
		if old, ok := s.lookup(owned{sender, replaces}); ok {
			if err := s.sink.Closed(old); err != nil {
				return 0, dbus.MakeFailedError(err)
			}
			s.forget(old)
		}
	}

	kept, extra := takeActions(actions)
	id, err := s.sink.Arrived(Notification{
		From:    claim(app, sender),
		Text:    text,
		Body:    rest,
		Urgent:  urgency(hints) == 2,
		Actions: kept,
		Extra:   extra,
	})
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}

	// Under both names: the one the server gave, which the spec says to use,
	// and the one the sender asked for, because reusing a fixed id is what
	// every volume OSD in the world does (notify-send -r 42, and dunstify's
	// -r before it).
	s.remember(owned{sender, uint32(id)}, id)
	if replaces != 0 {
		s.remember(owned{sender, replaces}, id)
	}
	return uint32(id), nil
}

// CloseNotification is how an app takes back its own notification - a download
// that finished, a message read elsewhere. Its own: an id it was never given
// closes nothing, which is what keeps this from being a way to delete other
// people's reminders.
func (n *notifications) CloseNotification(sender dbus.Sender, id uint32) *dbus.Error {
	s := n.server
	if item, ok := s.lookup(owned{sender, id}); ok {
		if err := s.sink.Closed(item); err != nil {
			return dbus.MakeFailedError(err)
		}
		s.forget(item)
	}
	// The spec wants the signal whether or not anything was there to close.
	s.emitClosed(uint64(id), ReasonClosed)
	return nil
}

// GetCapabilities says what this server does, and only that (docs/roadmap.md,
// cross-cutting: where a mechanism is partial, say so).
//
// "actions" is claimed because every action a sender declares is offered to the
// person: the notification center lists them with their own labels, a keypress
// each, and the default is what Enter means. It was not claimed while only the
// default could be invoked, because the capability promises the whole list and
// half a list is a promise apps would send buttons against.
//
// It is a claim about reach, not about immediacy. There is no popup yet, so the
// offer is behind Mod+n rather than in front of you, and an app expecting a
// button on the screen the moment it sends will not see one. That is the part
// worth knowing before reading this as more than it says.
//
// Nothing else is claimed. action-icons, body-markup, body-images, icon-static
// and sound are all things the spec defines and this does not do.
func (n *notifications) GetCapabilities() ([]string, *dbus.Error) {
	return []string{"actions", "body", "persistence"}, nil
}

// takeActions reads the actions a sender declared.
//
// The spec sends them as pairs - key, label, key, label - so the keys are the
// even positions. A key with no label after it is the sender's mistake and its
// key becomes its label: showing "reply" beats dropping an action somebody
// declared, and beats a button with nothing written on it.
//
// Labels go through oneLine for the same reason summaries do: this one ends up
// on a surface, and a label with a newline in it is a row that draws over the
// one below.
func takeActions(actions []string) ([]Action, int) {
	var kept []Action
	declared := 0
	for i := 0; i < len(actions); i += 2 {
		key := oneLine(actions[i])
		if key == "" {
			continue // an empty key addresses nothing
		}
		declared++
		if len(kept) >= actionsMax {
			continue
		}
		label := ""
		if i+1 < len(actions) {
			label = oneLine(actions[i+1])
		}
		if label == "" {
			label = key
		}
		kept = append(kept, Action{Key: key, Label: label})
	}
	return kept, declared - len(kept)
}

// GetServerInformation is what an app reads to decide what to send.
func (n *notifications) GetServerInformation() (name, vendor, version, spec string, err *dbus.Error) {
	return "zded", "zde", n.server.version, specLevel, nil
}

func (s *Server) lookup(k owned) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.mine[k]
	return id, ok
}

// claim is what to record as the sender. The app's own name for itself when it
// gave one, and the bus's name for the connection when it did not - a dash in
// the queue means a person typed it, so an app that sends nothing, or sends a
// dash, must not land in that column looking hand-written.
func claim(app string, sender dbus.Sender) string {
	app = oneLine(app)
	if app == "" || app == "-" {
		return string(sender)
	}
	return app
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
func oneLine(s string) string { return clean(s, summaryMax, false) }

// bodyText is the rest of the message, kept the way it was written: its line
// breaks survive, because a body is where the paragraph goes and flattening it
// loses the shape of the thing the center exists to show.
func bodyText(s string) string { return clean(s, bodyMax, true) }

// clean keeps what can be printed, bounded in characters rather than bytes: a
// notification written in Cyrillic is not half a notification.
//
// What is dropped leaves a space behind it, so a control character between two
// words does not join them. Zero-width joiners stay: they are invisible and
// unprintable, and without them a family emoji arrives as three people. With
// lines, a run of whitespace that had a newline in it becomes one newline, so
// the paragraphs stay and the blank space between them does not.
func clean(s string, max int, lines bool) string {
	var b strings.Builder
	kept := 0
	// The separator owed before the next rune that gets written. A newline
	// outranks a space: whitespace that spanned a line break was a line break.
	gap := ""
	for _, r := range s {
		switch {
		case lines && r == '\n':
			if b.Len() > 0 {
				gap = "\n"
			}
			continue
		case unicode.IsSpace(r):
			if b.Len() > 0 && gap == "" {
				gap = " "
			}
			continue
		case r == '‍' || unicode.IsPrint(r):
		default:
			// Something was here. A word boundary is a better guess at what it
			// meant than joining what sat on either side of it.
			if b.Len() > 0 && gap == "" {
				gap = " "
			}
			continue
		}
		if gap != "" {
			b.WriteString(gap)
			kept++
			gap = ""
		}
		if kept >= max {
			break
		}
		b.WriteRune(r)
		kept++
	}
	return strings.TrimSpace(b.String())
}

// introspectable is the XML the diagnostic tools read, written out rather than
// generated: the four methods and the one signal, in the spec's own types.
func introspectable() introspectXML {
	return introspectXML(`<node>
  <interface name="` + busIface + `">
    <method name="Notify">
      <arg name="app_name" type="s" direction="in"/>
      <arg name="replaces_id" type="u" direction="in"/>
      <arg name="app_icon" type="s" direction="in"/>
      <arg name="summary" type="s" direction="in"/>
      <arg name="body" type="s" direction="in"/>
      <arg name="actions" type="as" direction="in"/>
      <arg name="hints" type="a{sv}" direction="in"/>
      <arg name="expire_timeout" type="i" direction="in"/>
      <arg name="id" type="u" direction="out"/>
    </method>
    <method name="CloseNotification">
      <arg name="id" type="u" direction="in"/>
    </method>
    <method name="GetCapabilities">
      <arg name="capabilities" type="as" direction="out"/>
    </method>
    <method name="GetServerInformation">
      <arg name="name" type="s" direction="out"/>
      <arg name="vendor" type="s" direction="out"/>
      <arg name="version" type="s" direction="out"/>
      <arg name="spec_version" type="s" direction="out"/>
    </method>
    <signal name="NotificationClosed">
      <arg name="id" type="u"/>
      <arg name="reason" type="u"/>
    </signal>
    <signal name="ActionInvoked">
      <arg name="id" type="u"/>
      <arg name="action_key" type="s"/>
    </signal>
  </interface>
</node>`)
}

// introspectXML answers the one method org.freedesktop.DBus.Introspectable
// has. Written rather than pulled in: the generator lives in a subpackage of
// the bus library, and this is a constant string.
type introspectXML string

func (x introspectXML) Introspect() (string, *dbus.Error) { return string(x), nil }
