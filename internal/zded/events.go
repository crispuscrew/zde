package zded

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/clip"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/link"
)

// sendWait is how long a listener has to take a line before it stops being a
// listener. A healthy client on a unix socket takes it immediately; one that has
// stopped reading - a frozen shell that has not closed its socket - would
// otherwise block the write for ever, and with it every later broadcast, so
// pressing Mod+Tab would cost five seconds and a leaked goroutine each time
// until the session ended. Measured before this existed: 276 events filled the
// buffer and the 277th never returned.
//
// It bounds the whole send and not the write alone (see sendWithin). For a
// while it did not, and the sentence above was false in a second way that had
// nothing to do with a frozen client: a connection is one line at a time, so a
// broadcast waited for whatever was already being written to it, and an answer
// is written with five seconds of patience. One keypress, measured at 2.85
// seconds against a client that was subscribed, had asked, and had stopped
// reading.
const sendWait = 200 * time.Millisecond

// Events are how zded stops being a thing that only answers questions.
//
// Everything until now was request and reply, which is right for a key that
// wants something done and wrong for a surface that has to appear when a key is
// pressed. A shell cannot poll for that: the delay would be the delay between
// pressing Mod+Tab and seeing anything.
//
// So a client may ask to listen, and keep the connection. Events arrive on it
// as they happen, alongside the replies to anything else it asks on the same
// connection - told apart by the key, since an event line carries "event" where
// a reply carries "ok" or "error".
type Event struct {
	Kind string `json:"kind"`
	// The picker's contents, sent with the event rather than fetched after it:
	// zded already has them, and a surface that has to ask before it can draw
	// is a surface that appears in two steps.
	Desks []string `json:"desks,omitempty"`
	// Windows is the other picker's rows. One event type with two possible
	// payloads rather than two, because everything else about them is the same
	// - the screen, the token, the surface that draws it - and a shell that
	// learned to listen for one has already learned the other.
	Windows []Window `json:"windows,omitempty"`
	// Notifications is the notification center's rows, newest first. A third
	// payload on the same event for the same reason the second one is here: the
	// screen, the token and the acknowledgement are the same for every surface
	// zded asks for, and a shell that learned one has learned this one.
	Notifications []attn.Record `json:"notifications,omitempty"`
	// Actions is the palette's rows, sent the same way and for the same reason.
	Actions []Action `json:"actions,omitempty"`
	// Choices is the power menu's rows, with what each one is about to cost
	// already worked out (power.go): a confirmation that had to ask before it
	// could say what is being lost would fill in under somebody's finger.
	Choices []PowerChoice `json:"choices,omitempty"`
	// Clips is the clipboard history's rows: a preview of each entry and never
	// the entry itself, so what was copied stays in the daemon until somebody
	// picks a row (internal/clip, PreviewMax).
	Clips []clip.Row `json:"clips,omitempty"`
	// The connections surface's two: the wifi networks it lists, and the link
	// as it stands, so it can say what you are on without asking a second
	// question (net.go).
	Networks []link.Network `json:"networks,omitempty"`
	Link     *link.Status   `json:"link,omitempty"`
	On       string         `json:"on,omitempty"`
	// Output is the monitor to appear on: the one being looked at. A picker on
	// every screen is not a picker.
	Output string `json:"output,omitempty"`
	// Token is what the shell sends back once the surface is up. It is how the
	// asker learns that something was actually shown rather than merely
	// written to (see Switcher.Shown).
	Token string `json:"token,omitempty"`
	// Question is what the panel should open with already asked
	// (EventAskPanel), and empty for a panel somebody opened to type into.
	// It exists because `zde ask panel <question>` names a surface rather than
	// a terminal: the question was typed somewhere with no window, and the only
	// way it reaches the window is with the event that opens it.
	//
	// Its own field rather than Text, which is a piece of an answer coming the
	// other way. One field carrying both directions would be a shell deciding
	// which it had by the event kind, and a kind it did not know would be a
	// question drawn as an answer.
	Question string `json:"question,omitempty"`
	// Text is a piece of an answer as it arrives (EventAskText, internal/zded
	// ask.go). Pieces rather than one reply at the end, because an answer takes
	// seconds and a window that shows nothing until the last of it looks broken.
	Text string `json:"text,omitempty"`
	// Done ends a stream, and is the only thing that does: a surface that never
	// hears it waits for a piece that is not coming. Error says why it ended,
	// where it ended badly - a tier nobody configured, one that would not run,
	// one that said nothing at all.
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
}

// EventPicker asks the shell to show the desk switcher.
const EventPicker = "picker"

// EventWindows asks it to show the open windows instead. A kind of its own and
// not a flag on the picker event: a shell that has never heard of it ignores
// the line rather than drawing a desk picker with no desks in it.
const EventWindows = "windows"

// The same desks, for the two verbs that send something to one instead of going
// there: the focused window, or the whole workspace it is in.
//
// Kinds of their own rather than a field on the picker event, and here the
// reason is sharper than it is for the windows. The rows are identical, so a
// shell that had never heard of a new field would draw a desk picker that looks
// exactly right and switch desk when a row was chosen - the wrong verb, silently,
// on the desk somebody meant to send a window to. Unknown kinds are ignored, so
// an old shell draws nothing, nothing acknowledges, and the key falls back to
// printing the list with the name to type (see Switcher.Shown).
const (
	EventPickerMoveWindow    = "picker.move-window"
	EventPickerMoveWorkspace = "picker.move-workspace"
)

// EventCenter asks it to show the notification center: what arrived, whether
// the mode let it through, and what became of it.
const EventCenter = "notif-center"

// EventAttnPopup is one arrival, the moment it happens: the transient overlay
// that says what came in without being asked (attn.go). The record travels in
// Notifications, one long, because the shell already knows how to read one and a
// field of its own would be a second shape for the same thing.
//
// The one event here that carries no token and waits for no acknowledgement.
// Everything else is a key asking for a surface and wanting to know whether it
// appeared; this is an app calling Notify on the session bus, and a notification
// path that waited on a shell would be a Notify that blocks on a compositor.
const EventAttnPopup = "attn.popup"

// EventAttnReach asks the shell to put the keyboard on the newest popup, and it
// is the only thing that ever does.
//
// A popup appears without asking for the keyboard, which is the whole design.
// Every other surface the shell draws over the bar takes an exclusive grab while
// it is up, niri hands that grab to the oldest of them, and it took a fix to
// keep them to one at a time (shell/shell.qml, present). How many of them there
// are is left uncounted on purpose: it is a number several branches are each
// adding to, and what matters here is that this one is not among them. A surface that grabbed on every arrival
// would be taking the keyboard out of whatever you were typing into, several
// times an hour, unasked - the worst offender on the machine. So a popup's
// buttons are clickable whenever it is up, and reachable from the keyboard only
// after somebody has said so: one deliberate key, this event, and then the same
// one-at-a-time rule everything else obeys.
const EventAttnReach = "attn.reach"

// EventAttnHide takes every popup off the screen, and it is panic's (panic.go).
//
// Display and nothing else, which is the line this whole side of zde is drawn
// on: what was on those cards is in the notification history and on the queue
// exactly as it was, and nothing here tells a sender anything. Quiet mode stops
// the next popup; this is for the one that is already up, drawn over the decoy
// with the name of whoever sent it on it.
//
// No token and no waiting, like the popup event itself: a key that hides
// something is not a key that wants to know whether a surface appeared, and
// there is nothing to fall back to printing.
const EventAttnHide = "attn.hide"

// EventAsk asks the shell to open the ask popup, and EventAskPanel the panel
// that stays open. Two kinds and not one with a flag, for the reason the window
// picker has a kind of its own: a shell that has never heard of the second
// ignores the line rather than drawing the wrong surface.
const (
	EventAsk      = "ask"
	EventAskPanel = "ask.panel"
	// EventAskText is one piece of an answer, and with Done set, the end of one.
	// It goes to the connection that asked and to nobody else (ask.go).
	EventAskText = "ask.text"
)

// EventPalette asks it to show every action by name instead. A kind of its own
// for the reason EventWindows is one: a shell that has never heard of it draws
// nothing, where one that read a picker event with no desks in it would draw an
// empty surface holding the keyboard.
const EventPalette = "palette"

// EventClip asks it to show the clipboard history: what was copied, newest
// first, with a preview of each. A kind of its own for the reason EventWindows
// is one - a shell that has never heard of it draws nothing, where one reading
// another surface's event would draw an empty thing holding the keyboard.
const EventClip = "clip"

// MethodShown is how a listener says it did the thing: the token from the
// event it acted on. Unsolicited ones are ignored, so this cannot be used to
// make a key report success that never happened.
const MethodShown = "shown"

// ackWait is how long the asker waits for that. A shell on the same machine
// answers in about a millisecond, so this is invisible when everything works
// and is the whole cost when the shell is wedged - at which point the key falls
// back to printing, which is the point.
const ackWait = 200 * time.Millisecond

// MethodEvents is the request that turns a connection into a listener. One
// constant because two places name it: the connection loop, which is where it is
// really handled, and the dispatcher, which knows the name so that anything
// checking the protocol from outside does not read it as a method zded lacks.
const MethodEvents = "events"

// errSinkBusy is a line that could not be written because another one was being
// written to the same connection and had not finished inside the caller's
// deadline. Distinct from a write that failed, because the connection is fine:
// it is carrying an answer, and the caller gave up rather than waiting behind
// it. A listener that returns this keeps its place (see broadcast).
//
// That distinction only means anything because every writer is now bounded. A
// caller waiting on the gate learns one thing - somebody else is writing - and
// nothing at all about the client; the writer holding the gate is the one with
// the evidence, because a write deadline that expires is a client that has not
// taken a byte for the whole of it with its own buffer full. So the line
// between a listener that is busy and one that is gone is drawn by the holder
// and never by the queue behind it: the holder closes the connection when its
// own deadline expires (see writeWithin), the read loop ends, and the listener
// slot goes with it.
//
// Until reply took a deadline that sentence was false, and this error was how
// it failed. reply held the gate across a write with no deadline on it, so one
// client that stopped reading made every later caller see "busy" for the rest
// of the session - permanently, with nothing left to tell the two apart (see
// replyWait).
var errSinkBusy = errors.New("zded: the connection is busy with another line")

// sink is one connection, with the lock that keeps a reply and an event from
// interleaving halfway through a line.
type sink struct {
	// gate is that lock, as a one-place channel rather than a sync.Mutex,
	// because a caller has to be able to give up on it.
	//
	// The lock is held across the write, and the write's deadline is as long as
	// the caller can afford: 200ms for a broadcast (sendWait), five seconds for
	// a piece of an answer (askSendWait) or for the answer to a request
	// (replyWait). Every one of them has a deadline, and that is what makes the
	// gate a thing a caller can wait on at all - a holder that could keep it for
	// ever would make "busy" and "gone" the same answer (see errSinkBusy). A
	// mutex has no deadline, so a
	// broadcast allowed to wait 200ms for a listener in fact waited for
	// whatever answer was being pushed down the same connection - and the
	// keypress behind that broadcast waited with it. Measured on a client that
	// subscribed, asked, and then stopped reading: one attn.center took 2.85
	// seconds against 216µs with nothing streaming. sendWait's own comment
	// says a wedged shell must not be what a keypress waits for, and that is
	// the sentence this makes true.
	//
	// Made where it is first wanted rather than in a constructor: a sink is
	// built as a literal in a dozen places, and a zero value that deadlocks
	// would be a worse bug than the one this fixes. sync.Once is the part of
	// the standard library whose zero value is already the answer to that.
	made sync.Once
	gate chan struct{}
	// gone closes when the read loop on this connection has ended, which is how
	// anything working on that connection's behalf finds out the client has left
	// (see end, and ask.go's askRun). Made by the same Once as the gate and for
	// the same reason.
	gone chan struct{}
	// ends is what closes it, once: end is deferred from the read loop, and
	// closing a closed channel is a panic.
	ends sync.Once
	w    io.Writer
	// asking is whether an answer is already on its way down this connection.
	// One at a time, because an ask.text line carries no id of its own: two
	// answers interleaved on one connection would be indistinguishable, and the
	// first "done" would end both. Both clients serialize today - the CLI asks
	// and waits, the window will not take a second question while one is
	// running - and this is what makes that a property of the protocol rather
	// than a habit of the only two callers there happen to be.
	//
	// It is also read by the connection cap, which skips a connection that has
	// a tier running rather than dropping somebody's answer out from under them
	// (server.go, admit). That second reader is why it is set only once the
	// daemon's own place to run in has been claimed and never for a refusal:
	// what admit's comment says about it - that asksMax bounds how many can be
	// exempt - is only true of a flag a running tier sets. Set true by this
	// connection's read loop alone, and cleared by the run it started.
	asking atomic.Bool
	// putting is whether a clipboard write asked for on this connection is still
	// happening. One at a time, and for a plainer reason than asking's: the read
	// loop used to be the thing that serialized these, so answering them off it
	// without this would let one connection queue as many wl-copy processes as it
	// can write lines - and two writes racing for the selection is two answers
	// about which entry is on the clipboard.
	//
	// Claimed on the read loop, like asking and for the second reason it is:
	// a claim taken inside the goroutine bounds the wl-copy calls and not the
	// goroutines (clip.go, clipPutOn).
	putting atomic.Bool
	// pid is the process on the other end, from the credentials the kernel
	// attaches to the socket (server.go, allowPeer). Set once, before this sink
	// is in any table, and never written again - so it needs no lock and is not
	// atomic.
	//
	// It is there because "a flood pays for its own slots" is a sentence about a
	// client, and a client is a process rather than a socket. What a connection
	// has sent can be forged with one byte; how many connections the process
	// behind it is holding cannot be forged at all, only paid for in processes
	// (server.go, admit).
	//
	// Zero for a sink that is not a connection - a test's, or the one Close
	// answers on - which groups them together and is the right answer for a
	// number only used to compare connections in one table.
	pid int32
	// asked is when this connection was last used, as a Unix time in
	// nanoseconds: the last request line that parsed, or its arrival if it has
	// never sent one. It is the tie-break when the daemon is full and one
	// connection has to go (server.go, admit).
	//
	// What this connection last sent, and deliberately not what was last sent to
	// it. The second is the wrong question and would give the wrong answer for
	// the one connection that must never be the answer: a shell holding the
	// event stream sends `events` once at login and correctly sends nothing
	// again, so by anything measured on its own traffic it is the quietest
	// connection zded has.
	//
	// Atomic because it is written by this connection's read loop and read by
	// whichever other connection's goroutine has just found the table full.
	asked atomic.Int64
}

// touch records that this connection has just been used: a request line that
// parsed as one, or the moment it arrived.
//
// A line that parsed, and not any line at all. It was any line, and that made
// the number forgeable by the cheapest thing a socket can do: a connection
// sending "\n" was refreshed exactly as if it had asked something, because the
// touch came before the JSON was looked at. Demonstrated against a running
// daemon - a flood writing one byte per connection kept all 256 of its own
// connections fresh and chose which of the session's connections was dropped,
// naming targets 130, 7 and 255 and getting each of them first try.
//
// Called when the request is read rather than when it is answered, which is the
// other half of what the number means: a request that takes two minutes to
// answer was still asked at the start of it.
func (k *sink) touch() { k.asked.Store(time.Now().UnixNano()) }

// gateOf is the lock, made once. Every path to it goes through here, so no
// caller can meet a nil channel and wait for ever on it.
func (k *sink) gateOf() chan struct{} {
	k.channels()
	return k.gate
}

// endedOf closes when this connection's read loop has ended. A sink that is not
// a connection - a test's, or one nobody ever ends - simply never closes it,
// which is the right answer for a caller that selects on it.
func (k *sink) endedOf() chan struct{} {
	k.channels()
	return k.gone
}

// channels makes both, once. One Once for two channels rather than two, because
// they are made at the same moment for the same reason and a second one would
// only be a second thing to forget.
func (k *sink) channels() {
	k.made.Do(func() {
		k.gate = make(chan struct{}, 1)
		k.gone = make(chan struct{})
	})
}

// end says the client has gone. Called from the read loop on its way out, which
// is the only place that learns it: a connection is found to be closed by a read
// failing, and everything else on this daemon working for that connection - a
// tier that will run for two more minutes, most of all - is otherwise waiting on
// a write it has not tried yet.
func (k *sink) end() {
	gone := k.endedOf()
	k.ends.Do(func() { close(gone) })
}

// lockBefore takes the connection for one line, or gives up at the deadline and
// says so. Every caller has a deadline, an event's being the shortest because
// it has a keypress behind it.
func (k *sink) lockBefore(deadline time.Time) bool {
	gate := k.gateOf()
	// The ordinary case, which is nobody else writing: no timer, no allocation.
	select {
	case gate <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case gate <- struct{}{}:
		return true
	case <-t.C:
		return false
	}
}

func (k *sink) unlock() { <-k.gateOf() }

// replyWait is how long the answer to a request may take to be taken by the
// client that asked for it.
//
// It is the deadline this write did not have. reply took the connection with no
// deadline and held it across a write with none either, which is the one shape
// sendWait exists to end and the one place it was missed - every other write on
// this socket was already bounded. So a client that subscribed, asked, and then
// stopped reading filled its own receive buffer, and the read loop parked in
// that write for as long as the client stayed alive. Measured against a running
// daemon: one such connection left a goroutine sitting in sink.reply, kept its
// listener slot through 257 evictions - a full turnover of a 256-connection
// table, because a listener is exempt from the cap (server.go, admit) and a
// busy one is kept by a broadcast (see broadcast) - and took a keypress from
// 19µs to 200ms, where it stayed for the rest of the session.
//
// Five seconds, and the number is the client's rather than a guess: Call puts a
// five second deadline on the whole round trip (client.go), so a reply written
// after that is a reply nobody is waiting for any more. Shorter would be zded
// giving up on a caller that has not. Longer would be zded holding a connection
// open for a caller that has already gone, which is the shape of the bug this
// ends. It is also the patience one piece of an answer gets (askSendWait), for
// the same reason and against the same kind of client, so the socket has one
// number for "as long as anybody could still want this line" rather than two.
//
// What has to be true for it to expire: the client has not taken a byte for
// five seconds while its own receive buffer was full. That buffer is about
// 200 KB on a unix socket, which is hundreds of unread replies, so it is not a
// client that is slow or drawing or descheduled - it is one that asked and
// stopped reading. Measured on a client that is reading: microseconds.
//
// What expiring costs is the connection, and what not expiring cost was every
// keypress for the rest of the session. The bound this puts back is the one
// sendWait's comment already promises - a keypress costs one sendWait whatever
// is wedged - in place of a keypress that costs it for ever. It is still five
// seconds of 200ms keypresses per wedged client, and a client that reconnects
// can have that again, which is the same honest cost listenersMax already
// names: a subscriber that is not reading is a subscriber a keypress pays for.
const replyWait = 5 * time.Second

// reply answers the request this connection asked, and gives up on a client
// that will not take the answer.
//
// The read loop's own answer, on the connection that asked, so there is nothing
// useful to do with it except send it - and nothing useful to do about a client
// that is not reading it except stop. A write that runs out of time closes the
// connection, which is writeWithin's rule for every write that could not be
// finished, and right here for a second reason: what is on the wire is half a
// line, and the next bytes would be read as the tail of a message nobody can
// parse.
func (k *sink) reply(resp Response) {
	if err := k.writeWithin(responseLine(resp), replyWait); errors.Is(err, errSinkBusy) {
		// The gate was held by another line for the whole of replyWait, so this
		// request has no answer coming. Closed rather than left, because a
		// request that is never answered on a socket that stays open is a client
		// waiting for ever, where an EOF is something it can act on. It takes a
		// write already failing to reach this - every holder gives the gate up
		// inside its own deadline, and the longest of those is this one - so the
		// connection being closed here is one somebody else is closing anyway.
		if c, ok := k.w.(io.Closer); ok {
			c.Close()
		}
	}
}

func (k *sink) send(ev Event) error { return k.sendWithin(ev, sendWait) }

// sendWithin is send with the patience the caller can afford. A broadcast can
// afford almost none (see sendWait): a wedged shell must not be what a keypress
// waits for. One piece of an answer is a different thing - one of thousands
// being pushed at a client that is also drawing the last one - and 200ms of not
// reading is not a client worth giving up on.
//
// wait bounds the whole call and not only the write. Both halves can block, and
// for the same reason: the connection is one line at a time, so a caller can
// wait for the lock as long as somebody else's write may take. That was how the
// promise above failed - 200ms of write deadline behind five seconds of
// somebody else's answer - so the deadline is taken once here and both halves
// are held to it.
//
// A write that fails or falls short closes the connection, and doing that here
// rather than at each caller is the point: a deadline can trip halfway through a
// line, and the next bytes on that connection would be read as the tail of a
// message nobody can parse. Closing is also how the other end finds out - an EOF
// it can act on, rather than a stream that stopped and an end that never came.
//
// Running out of patience waiting for the lock is not that, and does not close
// anything: nothing was written, the connection is whole, and what is wrong with
// it is that it is busy. errSinkBusy says which of the two happened.
func (k *sink) sendWithin(ev Event, wait time.Duration) error {
	line, err := json.Marshal(struct {
		Event Event `json:"event"`
	}{ev})
	if err != nil {
		return err
	}
	return k.writeWithin(append(line, '\n'), wait)
}

// writeWithin puts one whole line on the connection, or gives up. It is the
// body sendWithin's comment above argues for, in its own function because every
// write to a connection now wants exactly that bargain: an event being
// broadcast, the answer to a request (see reply), and the sentence a connection
// is told before it is dropped (server.go, handle). All three are written to a
// client that may not be reading, none of them has anybody who can afford to
// wait for one that is not, and one write left without a deadline is enough to
// make the gate a place a connection can be parked for ever.
func (k *sink) writeWithin(line []byte, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	if !k.lockBefore(deadline) {
		return errSinkBusy
	}
	defer k.unlock()
	// Cleared afterwards so the reply path is not left with a deadline it never
	// asked for.
	if d, ok := k.w.(interface{ SetWriteDeadline(time.Time) error }); ok {
		d.SetWriteDeadline(deadline)
		defer d.SetWriteDeadline(time.Time{})
	}
	n, err := k.w.Write(line)
	if err == nil && n < len(line) {
		// Nothing to retry into: what is on the wire is already half a line.
		err = io.ErrShortWrite
	}
	if err != nil {
		// A connection that cannot take a whole line is finished, whichever of
		// the two ways it failed.
		if c, ok := k.w.(io.Closer); ok {
			c.Close()
		}
	}
	return err
}

// drop says why and then closes the connection.
//
// The sentence comes first and the close is unconditional, which is the whole
// difference between this and a socket that simply ends: a connection closed
// under a client is exactly what a daemon that has crashed looks like, and this
// one has not crashed - it is running, it has just answered the connection that
// took this one's place, and the client's own answer is to dial again.
//
// Bounded, because the client this is being said to may be one that never reads
// anything. A gate held by somebody else costs the sentence and not the close:
// what holds it is a line already being written, and there is nothing useful to
// queue a second one behind.
//
// And said on a goroutine of its own when the connection being told is not the
// one telling it (server.go, handle). The bound is 200ms, and the caller with
// something to lose is the connection that has just arrived - which may be the
// shell, dialling again into a full table. Measured on the arriving connection
// while the victim's gate was held: 200.6 to 200.9ms before this was moved off
// it, which is the whole of the ackWait a keypress has to be acknowledged in.
//
// The other way round it must not be a goroutine at all, and that is the one
// caller this has where the descriptor belongs to the caller: a connection that
// paid for its own slot is told by its own handle, which then returns into a
// deferred Close. Started as a goroutine there, the close won the race - 0 of 20
// dials heard anything (server.go, handle).
func (k *sink) drop(reason string) {
	k.writeWithin(responseLine(Response{Error: reason}), sendWait)
	if c, ok := k.w.(io.Closer); ok {
		c.Close()
	}
}

// listenersMax is how many connections may be listening at once.
//
// sendWait bounds what one listener costs a broadcast, and until this existed
// nothing bounded how many there were - so what a keypress cost was that times
// a number anything running as the user could choose. Measured on a running
// zded: 100 connections that subscribed and then stopped reading made one
// attn.center take 20.03 seconds, and 200 of them made it 40.07. The walk is
// concurrent now (see broadcast), so the population no longer sets the delay;
// what it still sets is the fan-out, one goroutine per listener per keypress,
// and the file descriptors and the sinks behind it. Both want a ceiling.
//
// The arithmetic. Exactly one thing subscribes today: the shell opens one
// events connection for the session (shell/shell.qml, one `{"method":"events"}`
// on the stream socket). The CLI never does - an ask.run's answer comes back on
// the connection that asked it, without subscribing - so a session with a shell
// and four terminals running verbs has one listener, not five. A shell being
// restarted holds two for the moment before the daemon reads EOF on the old
// one. Sixteen is that with room for surfaces nobody has written yet, and it is
// more listeners than any session has had.
//
// At the cap a broadcast fans out to 16 goroutines and costs sendWait, which is
// 200ms, whatever is wedged among them.
//
// A cap can be taken by somebody else, and that is the honest cost of it: 16
// connections that subscribe first are 16 the shell cannot be. It is the true
// sentence about what a flood can still do to a session, and it is worth
// reading beside the connection cap's, which sounds like it says otherwise:
// there, a shell dialling in always gets a connection (server.go, admit). Both
// hold. Getting a connection is not the same as getting a listener slot, and
// this is the cap that can be filled first. That is a
// session with no surfaces rather than a session that hangs on every keypress,
// it is bounded rather than renewable, and `zde status` now reports the count
// rather than a boolean that says a shell is there because something is.
const listenersMax = 16

// listen adds a connection to the listeners and says whether it took. Idempotent,
// so a client that asks twice is listening once - and asking twice at the cap is
// still yes, because it is already one of them.
func (s *Server) listen(k *sink) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = map[*sink]struct{}{}
	}
	if _, already := s.subs[k]; already {
		return true
	}
	if len(s.subs) >= listenersMax {
		return false
	}
	s.subs[k] = struct{}{}
	return true
}

func (s *Server) unlisten(k *sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, k)
}

// broadcast sends an event to every listener and answers how many took it. The
// count is what lets a verb behave differently when nothing is listening: with
// no shell running, `zde desk switcher` has to fall back to printing a list
// rather than asking a surface that does not exist to appear.
//
// A listener whose write fails or stalls is dropped: a connection that keeps its
// place in the list is a slow leak and a broadcast that lies about its reach.
// Dropping a stalled one is also what keeps the answer useful - a shell that has
// stopped reading is a shell that will not draw, and the caller needs to hear
// that nothing was shown so it can print the list itself.
//
// One that was merely busy is not dropped. A connection may subscribe and ask on
// the same socket, and while an answer streams down it there are moments when
// the next line has to queue behind the last (see sink.gate). Waiting is what
// this must not do, so it does not - but a client that is reading perfectly well
// and happens to be receiving an answer is not a client to stop sending events
// to for the rest of the session. It misses this one, and is not counted as
// having taken it, which is the honest answer to "did anything draw it".
//
// Keeping it is safe only because busy is now a state that ends. This walk
// cannot tell a listener that lost a race from one whose client has gone, and
// it should not try: what it sees is a gate, and a gate says who is writing and
// nothing about who is reading. The connection's own writer is what knows - a
// write deadline that expires is a client that has not taken a byte while its
// buffer was full - and it closes the connection when that happens, which ends
// the read loop and takes the listener off this list on the way out (server.go,
// handle). So every "busy" here is at most one write deadline long. It was not:
// reply held the gate with no deadline at all, so one client that stopped
// reading answered busy for the rest of the session, keeping its place through
// a full turnover of the connection table and costing every keypress 200ms
// (see replyWait). A rule here that dropped a listener for answering busy twice
// would have papered over that and would have cost a shell its events for the
// crime of receiving a long answer.
//
// The listeners are written to at once rather than one after another, and that
// is what makes sendWait's promise true a second time. sendWait bounds what one
// listener may cost a keypress; walked in a line, what the keypress actually
// cost was that times however many listeners there were, and how many there are
// is not this daemon's to decide. Measured before this changed: 100 connections
// that subscribed and stopped reading made one attn.center take 20.03 seconds,
// which is 100 x sendWait to the millisecond, and it did not decay - a sink
// whose gate is held answers errSinkBusy, which keeps it, so every later
// keypress cost the same 20 seconds without anyone re-subscribing. Concurrently
// the walk costs sendWait whatever the population is, and a shell that is
// reading perfectly well is no longer behind fifteen that are not.
//
// Nothing is shared across the goroutines but the count and the listener map,
// and each says so: a sink is serialized by its own gate, and one connection is
// never written to by two of these at once.
func (s *Server) broadcast(ev Event) int {
	s.mu.Lock()
	subs := make([]*sink, 0, len(s.subs))
	for k := range s.subs {
		subs = append(subs, k)
	}
	s.mu.Unlock()

	var sent atomic.Int64
	var wg sync.WaitGroup
	wg.Add(len(subs))
	for _, k := range subs {
		go func() {
			defer wg.Done()
			switch err := k.send(ev); {
			case err == nil:
				sent.Add(1)
			case errors.Is(err, errSinkBusy):
			default:
				s.unlisten(k)
			}
		}()
	}
	wg.Wait()
	return int(sent.Load())
}

// await registers a token and returns the channel that closes when somebody
// acknowledges it.
func (s *Server) await(token string) chan struct{} {
	ch := make(chan struct{})
	s.mu.Lock()
	if s.waiting == nil {
		s.waiting = map[string]chan struct{}{}
	}
	s.waiting[token] = ch
	s.mu.Unlock()
	return ch
}

func (s *Server) stopAwaiting(token string) {
	s.mu.Lock()
	delete(s.waiting, token)
	s.mu.Unlock()
}

// acknowledge resolves a token. Unknown ones are ignored rather than refused:
// an event whose asker has already given up is not an error, it is late.
func (s *Server) acknowledge(token string) Response {
	s.mu.Lock()
	ch, known := s.waiting[token]
	if known {
		delete(s.waiting, token)
	}
	s.mu.Unlock()
	if known {
		close(ch)
	}
	return ok("thanks")
}

// nextToken is a counter and not a random string: it never leaves this machine,
// and one that reads 7 is one a person can follow in a log.
func (s *Server) nextToken() string {
	s.mu.Lock()
	s.tokens++
	n := s.tokens
	s.mu.Unlock()
	return strconv.FormatUint(n, 10)
}

// listeners is how many connections are listening. For tests, and for anything
// later that wants to know whether the shell is there.
func (s *Server) listeners() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Switcher is what `desk.switcher` answers: the desks, and whether a surface
// took the job of showing them.
type Switcher struct {
	// Shown is true when a listener said it put the surface up, within ackWait.
	// Not "the bytes were accepted": a shell can read a socket while failing to
	// draw anything - frozen, or stuck on a frame - and that used to count,
	// which made the key do nothing at all and suppress the printed list too.
	//
	// False means nothing showed it in time, and the caller prints the list
	// itself. That is what the key does with no shell running, with a wedged
	// one, and with one that is merely slower than ackWait - the last of which
	// prints a list and then shows a picker, which is untidy and better than
	// silence.
	Shown bool     `json:"shown"`
	Desks []string `json:"desks"`
	On    string   `json:"on,omitempty"`
}

// switcher opens the desk picker, or says that nothing could open it.
func (s *Server) switcher() Response { return s.deskPicker(EventPicker) }

// deskPicker opens the desks for whichever verb wants one named: going there,
// or sending the focused window or workspace there.
//
// One list and one surface, because it is one question - which desk - and the
// only thing that differs is what the answer is spent on, which the shell reads
// off the kind. A verb that takes a desk name has no other way to ask for one
// from a keyboard: the name is the person's own, so it cannot be in a shipped
// keymap, and the chord that would carry it does not exist.
func (s *Server) deskPicker(kind string) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	names := m.DeskNames()
	if kind != EventPicker {
		names = withRegulars(names)
	}
	if len(names) == 0 {
		// A fresh login, before anything is named. Advice rather than a bare
		// refusal, because this is the one time somebody sees it.
		return Response{Error: "no desks yet: write a manifest, or open something and it will be adopted into one"}
	}
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		// Which screen is a detail; not knowing it is not worth refusing over,
		// and the shell falls back to the screen it can see.
		output = ""
	}
	on := s.activeDesk(m)
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	sent := s.broadcast(Event{
		Kind:   kind,
		Desks:  names,
		On:     on,
		Output: output,
		Token:  token,
	})
	if sent == 0 {
		return ok(Switcher{Shown: false, Desks: names, On: on})
	}
	// Somebody took the bytes; now find out whether anything came of them.
	select {
	case <-acked:
		return ok(Switcher{Shown: true, Desks: names, On: on})
	case <-time.After(ackWait):
		return ok(Switcher{Shown: false, Desks: names, On: on})
	}
}

// withRegulars puts the regulars among the destinations, which the desk map has
// only once the band exists.
//
// The one row a picker over what exists could not offer, and the one it must:
// the band is never declared and adoption never names into it, so handing a
// workspace to it is the only way it ever comes into being (docs/roadmap.md).
// Left out, the key could not do the thing the verb is most needed for, and the
// answer to "how do I make my regulars" would still be a terminal.
//
// Not done for the switcher, where the same row would be a desk to go to that
// has nothing in it - niri has no workspace to bring up, so it is a row that
// answers with a refusal.
func withRegulars(names []string) []string {
	for _, n := range names {
		if n == desk.Regulars {
			return names
		}
	}
	// Sorted like DeskNames, so a row does not move between two presses of the
	// key (internal/zded, sortWindows).
	out := append(append([]string{}, names...), desk.Regulars)
	sort.Strings(out)
	return out
}
