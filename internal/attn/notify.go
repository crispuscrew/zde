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
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/crispuscrew/zde/internal/bus"
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

// SelfFrom is what the desktop's own messages say sent them, and the one name
// in that column no app can take: an arrival off the bus that claims it is
// recorded under the bus's own name for its connection instead (see claim).
//
// Reserved because the column is otherwise a claim and nothing checks it
// (docs/vision.md, principle 6 - a sender is a claim until it is known by the
// socket it arrived on). That is defensible for one app impersonating another,
// which is a lie about a peer; it is not defensible for the desktop's own name,
// because the whole point of a notification that says "this desk could not
// start browser@vshop" is that the thing telling you is the thing that tried.
// Without the reservation, `notify-send -a zde` was that sentence exactly:
// same column, same shape, same popup with buttons on it, and the session bus
// is precisely the surface a sandboxed app is given (principle 7).
//
// What it buys, said plainly: an arrival cannot be this string, so this string
// is the desktop. What it does not buy is a defence against a name that merely
// looks like it - "zde-session", or "zdе" with a Cyrillic е - because that is
// the general problem of an unverified column and it ends where attribution by
// channel begins. What a person can rely on is narrower and worth knowing: an
// impostor is drawn under its bus address (":1.57"), which no app name looks
// like, on the popup, in the centre, in the queue and in `zde queue`.
//
// It is also a ring the history never evicts, for the same reason the nameless
// one is not (history.go, nobody): a name that no app can mint is not a name an
// app can push the count with.
const SelfFrom = "zde"

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

// actionTextMax bounds one action's key and one action's label.
//
// It exists because the count was wrong everywhere. Both went through oneLine,
// so both were bounded at summaryMax, and nine of them was 9 x 600 characters -
// 24 KB a record in an alphabet that costs four bytes a character, which is
// half as much again as the body that every memory figure in this package is
// built around (see bodyMax, snapshotBodyMax, SendersMax). A list of buttons is
// not the largest thing a notification says, and while it was unbounded in
// practice it was.
//
// Eighty, because both of the things it bounds are short by nature. A key is an
// identifier the sender wants handed back, and the longest shapes in the wild
// are reverse-DNS or a UUID, both under forty. A label is a word or three on a
// button, and the center draws every one of them on one line under the list, so
// eighty is already past what fits.
//
// A label longer than this is cut, because a label is read and nothing depends
// on its bytes. A key longer than this is not kept at all - see takeActions.
const actionTextMax = 80

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
// Still bounded, because the body is attacker-controlled and it is the largest
// single thing that decides how big the history can get. The arithmetic: the
// history holds a ring of PerSenderMax for each of SendersMax senders plus the
// two nothing on the bus can reach - the nameless one and the desktop's own -
// so 420 records, and 420 x 4000 characters is 6.7 MB of body if every record
// is at its limit and written in an alphabet that costs four bytes a
// character - a few hundred kilobytes in any session made of real
// notifications. SendersMax carries the whole count, which adds the summaries,
// the sender names and the action lists to that and comes to about 10 MB.
//
// Largest single thing, and only since the action lists were bounded. Nine
// actions at summaryMax for both key and label was 24 KB a record against this
// 16 KB, so the sentence above used to be false in the direction that matters:
// see actionTextMax.
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
	// Replaces is the notification already in the history that this one
	// supersedes, or zero for an ordinary arrival.
	//
	// Not the sender's replaces_id, and that is the point. That number is the
	// sender's own name for a thing (notify-send -r 42, and dunstify's -r
	// before it), and only this side knows which item it stood for - and only
	// after checking that the connection asking is the one that sent it. By
	// the time it is in here it is a fact rather than a claim, which is what
	// lets the history put the new record where the old one was instead of
	// beside it (history.go, Replace).
	Replaces uint64
}

// Local is a notification zde sends itself, through the same door as anything
// on the bus: zded is the session's notification server, so what it has to tell
// somebody it can hand to its own sink rather than dial out and back.
//
// It exists for the bounds. Notify clamps everything a client sends and nothing
// else did, so a caller filling in a Notification by hand was the one way into
// the history with no bound on it - and what zde has to say is usually a
// program's own complaint, which is one line until the day it is a screen of
// them (internal/zded, launchesFailed).
func Local(from, text, body string) Notification {
	return Notification{From: from, Text: oneLine(text), Body: bodyText(body)}
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
//
// Bounded, and that is what lets the daemon bring this up on the way past
// rather than around it (cmd/zded): a session bus that accepts the connection
// and never authenticates used to stop zded before it served anything at all.
func Serve(sink Sink, version string) (*Server, error) {
	conn, err := bus.Session()
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
// nowhere to put them. The actions are kept with their labels, in the order the
// sender declared them and bounded by actionsMax, because the center offers
// every one of them and the capability list says so (see takeActions, Invoke,
// GetCapabilities).
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
	//
	// Which item it was is carried into the arrival rather than left behind
	// here. It is the same fact the history needs to keep one download to one
	// row instead of a hundred (history.go, Replace), and this is the only
	// place that knows it.
	var replaced uint64
	if replaces != 0 {
		if old, ok := s.lookup(owned{sender, replaces}); ok {
			if err := s.sink.Closed(old); err != nil {
				return 0, dbus.MakeFailedError(err)
			}
			s.forget(old)
			replaced = old
		}
	}

	kept, extra := takeActions(actions)
	id, err := s.sink.Arrived(Notification{
		From:     claim(app, sender),
		Text:     text,
		Body:     rest,
		Urgent:   urgency(hints) == 2,
		Actions:  kept,
		Extra:    extra,
		Replaces: replaced,
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
// It is a claim about immediacy too, since the popup: an arrival puts a card on
// the screen with those same buttons on it, and takes itself away (shell,
// AttnPopup.qml). What the popup is subject to is the mode - quiet shows none of
// them, focus only what the sender called urgent - and the center still holds
// every one either way, which is the part worth knowing. An app that sends
// buttons in quiet mode is offering them to somebody who will find them behind
// Mod+n, on their own time.
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
// Labels go through the same cleaning summaries do, at actionTextMax: this one
// ends up on a surface, and a label with a newline in it is a row that draws
// over the one below.
//
// A key past actionTextMax is counted and not kept, where a label past it is
// cut. The difference is what each is for. The key is what goes back to the
// sender as ActionInvoked, so a shortened one is a key that app never declared
// and a button that quietly does nothing when it is pressed; counted, the
// surface says there is an action it cannot reach, which is exactly what it
// says about the tenth one. A label is read by a person and nothing depends on
// its bytes.
func takeActions(actions []string) ([]Action, int) {
	var kept []Action
	declared := 0
	for i := 0; i < len(actions); i += 2 {
		key := oneLine(actions[i])
		if key == "" {
			continue // an empty key addresses nothing
		}
		declared++
		if utf8.RuneCountInString(key) > actionTextMax {
			continue // counted above, and unreachable rather than wrong
		}
		if len(kept) >= actionsMax {
			continue
		}
		label := ""
		if i+1 < len(actions) {
			label = actionLabel(actions[i+1])
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
//
// The desktop's own name is reserved the same way and for the same reason, one
// step further on: a dash borrowed says a person typed this, and "zde"
// borrowed says the session itself did (see SelfFrom). The bus's name for a
// connection is what both fall back to, because the bus hands that out rather
// than letting the peer choose it.
func claim(app string, sender dbus.Sender) string {
	app = oneLine(app)
	if app == "" || app == "-" || isSelf(app) {
		return string(sender)
	}
	return app
}

// isSelf reports whether a claim would be read as the desktop's own name.
//
// Not a string equality, because the column is read by a person and not by a
// parser: "ZDE", "[zde]" and "z d e" all arrive at the same word, and a
// reservation that only caught the lowercase spelling would be one an attacker
// steps around by pressing shift. Case is folded and everything that is not a
// letter or a digit is dropped, so what is compared is the word somebody reads.
//
// It stays narrow on purpose. Only a claim that reduces to exactly this word is
// taken away: an app called "zdeco", or "zde-helper", reduces to something else
// and keeps its name. Refusing everything with those three letters in it would
// be zde renaming other people's apps.
func isSelf(app string) bool {
	var b strings.Builder
	for _, r := range app {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String() == SelfFrom
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

// Line is foreign text on its way to a row somebody reads: one printable line,
// bounded the way a summary is.
//
// Exported because the notification path is not the only one that takes a
// string from something else and puts it in front of a person, and it was the
// only one that had a filter. A window title is set by the application
// (internal/zded, jump.go); an inhibitor's reason is written by whatever ran
// systemd-inhibit (power.go); a workspace name in a conflict line is niri's
// (server.go, reconcile). All of them end up in a terminal, where ESC is not a
// character but the start of an instruction, and in a one-line row, where a
// newline is a second row with nothing in column one.
//
// One function rather than one per package, because this is a filter with
// corners in it - the zero-width joiner that has to survive, the space left
// where something was dropped - and four copies of a filter is three of them
// drifting. What it is not is a filter for everything: text that keeps its own
// shape, an answer with paragraphs and indented code in it, is a different job
// and is done by Text below.
func Line(s string) string { return oneLine(s) }

// Text is foreign text that keeps its own shape on its way to a terminal: what
// a terminal reads as an instruction taken out, and everything else left
// exactly as it was written.
//
// The other half of Line's job, and the difference is the shape. Line reflows -
// it folds runs of whitespace and cuts at a bound - because it is making a row.
// This is for text that has paragraphs and indented code in it: a tier's answer
// as it streams (cmd/zde, askRun), a parser's complaint about a manifest with
// its caret under the column that is wrong. So tabs and newlines are shape and
// stay. A carriage return is not shape: it is how a line is drawn over with
// another one, which is a way of hiding what was printed rather than of writing
// anything. Nothing is cut, because there is no row to fit and an error cut off
// before the path in it is one nobody can act on.
//
// The zero-width joiner survives, for the reason clean keeps it: it is
// unprintable by every test Go has, and a family emoji without it is three
// people.
//
// Here beside Line rather than in the command that first needed it, because
// three binaries print somebody else's text now and these rune decisions are
// the ones Line already makes. A second copy is the copy that drifts.
func Text(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == zwj:
			return r
		case unicode.IsPrint(r):
			return r
		default:
			return -1
		}
	}, s)
}

// zwj is the zero-width joiner, spelled rather than typed: it is invisible in
// source, and a rune nobody can see in a filter is one nobody can review.
const zwj = '\u200d'

// Block is one message on its way to a terminal whole - an error, which is the
// text zde prints that it least often wrote itself.
//
// Text and two things a whole message needs that a stream does not. The first
// line starts in column one and no other line does, because the lines after it
// are somebody else's: zcr's output from a failed launch, logind's refusal,
// niri's message, a parser's several lines about one file. Indented they read
// as what they are - the rest of this error - and cannot be a second error zde
// never printed. The indent is two spaces for every line after the first, so
// whatever a parser lined up under a column of its own is still lined up.
//
// A message with nothing printable in it is quoted rather than dropped. It is
// the one case where this would otherwise print an empty line and exit 1, which
// tells a person that something failed and nothing about what - so the bytes go
// out the way Go writes a string it cannot show, escaped and harmless.
func Block(s string) string {
	out := strings.TrimSpace(Text(s))
	if out == "" {
		return strconv.Quote(s)
	}
	return strings.ReplaceAll(out, "\n", "\n  ")
}

// oneLine makes anything an app sends fit one line of the queue.
//
// Normalised rather than refused, which is the opposite of what `zde queue
// add` does with the same problem - and deliberately. A person typing a
// reminder can be told to try again; an app's notification is the only copy
// there will ever be of something that already happened.
func oneLine(s string) string { return clean(s, summaryMax, false) }

// actionLabel is what a person reads on one of a notification's buttons, on
// one line and bounded at actionTextMax rather than at a summary's 300.
func actionLabel(s string) string { return clean(s, actionTextMax, false) }

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
		case r == zwj || unicode.IsPrint(r):
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
