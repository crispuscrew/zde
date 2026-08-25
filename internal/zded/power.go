package zded

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/power"
)

// The power menu (docs/model.md, section 6: system.power).
//
// Here and not in the shell, which is the rule the whole shell is built on: it
// is a thin adapter over this socket with zero logic inside (docs/vision.md,
// section 2). So the surface draws rows it was handed and sends back a name,
// and everything that decides anything is on this side. There are three things
// to decide and each of them is a fact the shell has no way to reach: what a
// choice is about to cost, whether it is worth asking twice about, and what a
// refusal means. A QML file that shelled out to `systemctl` would have the
// first two wrong and would not see the third at all - logind refuses on the
// wire, and an exit status on a stderr no keypress has is exactly the silence
// this surface exists to break.
//
// The one exception is the lock, and it is an exception on purpose: locking is
// running the program this machine calls its locker, not a logind verb, so this
// row spawns what the lock key spawns (see powerRun).

// EventPower asks the shell to show the power menu. Its own kind, like the
// connections surface: a shell that has never heard of it ignores the line
// rather than drawing a picker with nothing in it.
const EventPower = "power"

// powerLock is the one row that is not logind's. Spelled here rather than in
// internal/power, because that package is logind and the lock is not.
const powerLock = "lock"

const powerSuspend = "suspend"

var errSuspendUnavailable = errors.New("suspend is unavailable in ZDE v0.1; hardware suspend begins in v0.2")

// PowerChoice is one row of the power menu: what it is called on the wire, what
// it says on the screen, what it is about to cost, and whether it is asked
// about twice.
//
// Costs is the half worth having. "Log out" and "power off" are the two keys on
// this desktop that end things nobody can get back, and a confirmation that
// only asks "are you sure" teaches people to say yes without reading - so each
// line here is something true about this machine right now: the windows that
// close, the arrivals the queue never got, the other person logged in.
type PowerChoice struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
	// Confirm is whether this one is asked about before it happens. The three
	// that end the session are; the lock is not, because nothing is lost.
	Confirm bool     `json:"confirm,omitempty"`
	Costs   []string `json:"costs,omitempty"`
	// Why is why this row would do nothing, in the same words the refusal uses
	// when somebody picks it anyway - the palette's bargain (palette.go,
	// whyNot). One field and not two: a row works exactly when there is nothing
	// to say here, and a second flag saying the same thing is a second thing to
	// keep in step.
	Why string `json:"why,omitempty"`
}

// Power is what `system.power` answers with no argument: the choices, and
// whether a surface took the job of showing them. Shown means what it means for
// the desk switcher, and for the same reason (see Switcher).
type Power struct {
	Shown   bool          `json:"shown"`
	Choices []PowerChoice `json:"choices"`
}

// noLogindFor is how long a logind that could not be reached is believed
// absent.
//
// The first version of this remembered nothing, on the argument that nothing
// polls the power key: the bar asks about the link every five seconds, and this
// is opened when somebody presses a chord. The argument was about the wrong
// thing. A person leaning on Mod+Shift+x is not a poll, but against a system
// bus that accepts and then says nothing each press cost the whole two-second
// bound (internal/bus, Within) - so the key did nothing for two seconds, every
// time, for the rest of the session, and the surface it draws is the one whose
// whole job is to still work when the session has gone wrong.
//
// A minute rather than the five the network side keeps, and the arithmetic
// behind that has been rewritten once already because the first version of it
// stopped being true.
//
// It used to read "five is bought there by seventeen thousand dials a day; here
// it buys a handful". That was a sentence about a surface nothing polled, and
// `system.idle` is polled now: the bar asks it on the same five-second clock as
// the link (shell/shell.qml, its own Dialer). So on a machine with no logind the
// dials are 1,440 a day rather than a handful - the poll finds the answer stale
// once a minute and refreshes it - against the 288 the network side's five
// minutes buys, and against the 17,280 either of them saves.
//
// A minute all the same, and now for the only reason left: it is how long
// somebody who has just started logind waits to be believed, and five would be
// five. What a minute no longer costs is a keypress, because the dial is not
// made under the lock any more (see logins) - 1,440 dials a day of a bus that is
// not there is a socket connect that fails, and nothing else waits for it.
const noLogindFor = time.Minute

// logins is logind, opened the first time something asks and kept. The same
// shape as the network side and for the same reasons (net.go, links): lazily,
// because a machine without it has to be a state rather than a daemon that will
// not start, and dropped when the connection dies, because a closed bus
// connection answers every call with the same error for ever and a daemon
// holding one would refuse every power action for the rest of the session. It
// is the bus connection that is watched and not logind - a logind restart does
// not close one, and does not need to (internal/power, Alive).
//
// Only the absence is remembered, and not any other failure: an absence is a
// fact about the machine, and a bus that answered badly once is worth asking
// again. A connect that ran out of time arrives here as an absence too
// (internal/power, Open), which is the same answer for the same reason.
//
// The lock is not held across the dial, and the burst the first version of this
// was written about is still collapsed - by one dial at a time rather than by
// one lock. The difference is who waits for it.
//
// powerMu guards the fields here and nothing else, so its hold time is a few
// field reads. Held across the dial, it was bounded by bus.Within instead -
// two seconds set by something outside this process - and every caller of
// logins() queued behind whichever one happened to dial. That was defensible
// while the only caller was a chord somebody pressed. It is not now: `system.idle`
// is on the bar's five-second poll (shell/shell.qml), so on a machine with no
// logind a poll takes the lock for two seconds every noLogindFor, and the
// surface behind the lock is the power menu - the one whose whole job is to work
// when the session has gone wrong.
//
// So: one dial in flight, in s.dialing, and the callers that arrive while it is
// out do not all wait for it. A caller that has an answer to give takes it - the
// remembered absence, one dial out of date, which is a better answer for a
// keypress than two seconds of nothing and the same answer at the end of them.
// A caller with nothing to give waits on the dial, because there is nothing else
// honest to say. Presses two to forty of a burst still cost one dial between
// them, which is what the lock was there for.
func (s *Server) logins() (power.Manager, error) {
	s.powerMu.Lock()
	if s.logind != nil {
		if alive, ok := s.logind.(interface{ Alive() bool }); !ok || alive.Alive() {
			m := s.logind
			s.powerMu.Unlock()
			return m, nil
		}
		if closer, ok := s.logind.(interface{ Close() error }); ok {
			closer.Close() //nolint:errcheck // it is already the connection that stopped working
		}
		s.logind = nil
	}
	if !s.noLogindAt.IsZero() && time.Since(s.noLogindAt) < noLogindFor {
		// In the words the last dial used, so that a person reading the row
		// gets what actually went wrong rather than a summary of it.
		err := s.noLogind
		s.powerMu.Unlock()
		return nil, err
	}
	if d := s.dialing; d != nil {
		if s.noLogind != nil {
			// One dial out of date and this instant, rather than fresh and two
			// seconds from now (see above).
			err := s.noLogind
			s.powerMu.Unlock()
			return nil, err
		}
		s.powerMu.Unlock()
		<-d.done
		return d.m, d.err
	}
	d := &dial{done: make(chan struct{})}
	s.dialing = d
	open := s.openPower
	if open == nil {
		open = power.Open
	}
	s.powerMu.Unlock()

	m, err := open()

	s.powerMu.Lock()
	if err != nil {
		// Only the absence is remembered (see above), and a dial that failed
		// some other way leaves nothing behind to be believed.
		if errors.Is(err, power.ErrNoLogind) {
			s.noLogind, s.noLogindAt = err, time.Now()
		}
	} else {
		s.noLogind, s.noLogindAt = nil, time.Time{}
		s.logind = m
	}
	d.m, d.err = m, err
	s.dialing = nil
	s.powerMu.Unlock()
	// After the fields, so that a caller woken by this finds the answer already
	// written down rather than a dial that has just finished and no manager.
	close(d.done)
	return m, err
}

// dial is one attempt to reach logind, and what it found. It exists so that the
// callers arriving during an attempt can be told what it found without every one
// of them making an attempt of its own (see logins). Written once, by the
// goroutine that made it, before the channel closes; read only after.
type dial struct {
	done chan struct{}
	m    power.Manager
	err  error
}

// powerMenu opens the surface, or says that nothing could open it - the same
// bargain as the desk switcher, which is what makes the key do something on a
// session whose shell has died.
//
// A machine with no logind still opens it. The lock is on this menu and locking
// needs no logind at all, and the alternative is a key that appears broken on
// the one machine where the reason is worth reading.
func (s *Server) powerMenu() Response {
	choices := s.powerChoices()
	output := ""
	if s.niri != nil {
		if _, place, err := s.niri.FocusedPlace(); err == nil {
			// Which screen is a detail; not knowing it is not worth refusing
			// over, and the shell falls back to the screen it can see.
			output = place
		}
	}
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	sent := s.broadcast(Event{
		Kind:    EventPower,
		Choices: choices,
		Output:  output,
		Token:   token,
	})
	if sent == 0 {
		return ok(Power{Choices: choices})
	}
	select {
	case <-acked:
		return ok(Power{Shown: true, Choices: choices})
	case <-time.After(ackWait):
		return ok(Power{Choices: choices})
	}
}

// powerChoices is the menu, with what each row costs on this machine right now.
//
// Built here and sent with the event rather than fetched by the surface: zded
// knows it already, and a confirmation that had to ask before it could say what
// is about to be lost would be a confirmation that appears empty and then fills
// in, under somebody's finger.
func (s *Server) powerChoices() []PowerChoice {
	st, err := s.powerState()
	why := ""
	if err != nil {
		// The same words on the row and in the refusal: this is a machine where
		// four of the five rows cannot work, and a row that said nothing about
		// it would be four silent keys inside a surface built to explain them.
		why = err.Error()
	}
	windows := s.windowsOpen()
	unqueued := s.unqueuedArrivals()

	// The order is the order somebody reaches for them, and it is also the
	// order of how much they cost: the lock first, because it is the one
	// pressed daily and the one that loses nothing, and the machine going off
	// last.
	ending := append(append([]string{}, closes(windows)...), inMemory(unqueued)...)
	return []PowerChoice{{
		Name:  powerLock,
		Label: "lock",
		Desc:  "lock the screen, and leave everything running",
		Why:   why,
	}, {
		Name:    string(power.Logout),
		Label:   "log out",
		Desc:    "end this session and go back to the greeter",
		Confirm: true,
		Costs:   ending,
		Why:     why,
	}, {
		Name:  powerSuspend,
		Label: "suspend",
		Desc:  "hardware sleep starts in ZDE v0.2",
		Why:   errSuspendUnavailable.Error(),
	}, {
		Name:    string(power.Reboot),
		Label:   "reboot",
		Desc:    "restart the machine",
		Confirm: true,
		Costs:   append(append(append([]string{}, ending...), others(st)...), held(st, power.Reboot)...),
		Why:     why,
	}, {
		Name:    string(power.PowerOff),
		Label:   "power off",
		Desc:    "turn the machine off",
		Confirm: true,
		Costs:   append(append(append([]string{}, ending...), others(st)...), held(st, power.PowerOff)...),
		Why:     why,
	}}
}

// powerState is what logind says, and an empty state with the reason when there
// is nothing to ask. Separated from the menu so that a machine with no logind
// draws the same five rows as one with it, marked rather than shortened.
func (s *Server) powerState() (power.State, error) {
	m, err := s.logins()
	if err != nil {
		return power.State{}, err
	}
	st, err := m.State()
	if err != nil {
		return power.State{}, err
	}
	return st, nil
}

// windowsOpen is how many windows a log out is about to close, and zero when
// niri will not say. Not an error: the menu is most worth having on a session
// that has gone wrong, and refusing to draw it because the compositor is
// unreadable would take the power menu away exactly when somebody is reaching
// for it.
//
// No compositor at all is the far end of the same thing, and it answers the
// same way rather than being left to panic: this is the surface whose whole job
// is to still be there when the session has gone wrong.
func (s *Server) windowsOpen() int {
	if s.niri == nil {
		return 0
	}
	windows, err := s.niri.Windows()
	if err != nil {
		return 0
	}
	return len(windows)
}

func closes(windows int) []string {
	switch {
	case windows <= 0:
		return nil
	case windows == 1:
		return []string{"1 window closes"}
	}
	return []string{fmt.Sprintf("%d windows close", windows)}
}

// unqueuedArrivals is how many things the notification center is holding that
// the queue never got: the ones a mode kept out of it, still unfinished.
//
// Counted rather than taken from the length of the history, which is every
// record it has. Two of those are not a cost. A queued item is in the journal,
// which a log out does not touch, so counting one would tell somebody they are
// about to lose the one thing certain to come back; a dismissed record is
// something already finished with, by the person or by the app taking it back
// (server.go, Closed), and it is not waiting for anybody.
func (s *Server) unqueuedArrivals() int {
	n := 0
	for _, r := range s.history.Recent() {
		if !r.Queued && !r.Dismissed {
			n++
		}
	}
	return n
}

// inMemory is what the notification center is holding on its own.
//
// Said as where they are rather than as what a log out destroys, which is the
// only form of this line that stays true. Today the ring is memory and a log
// out is the end of all two hundred of it (docs/roadmap.md, 0.1); a zded that
// wrote the newest of the history down on the way out would give some of them
// back, shortened, and the sentence "they go with the daemon" would then be
// wrong in the other direction. What does not change either way is the split
// this line is actually about: the queue is what you still owe and it survives,
// and these are the ones it never got.
func inMemory(unqueued int) []string {
	if unqueued <= 0 {
		return nil
	}
	one := "arrivals"
	if unqueued == 1 {
		one = "arrival"
	}
	return []string{fmt.Sprintf("the notification center is holding %d %s the queue never got",
		unqueued, one)}
}

// others is somebody else's session, said before the key is pressed rather than
// after logind has refused it. Both halves are worth reading: their work is
// about to end, and logind will want an administrator's password that nothing
// here can ask for (internal/power, Because).
//
// The name and the session id are logind's, which makes them the machine's
// rather than an app's - and they are still put through the filter below, for
// the reason the next function's are: what decides whether a line is safe to
// print is where it is printed, not how respectable its source sounds.
func others(st power.State) []string {
	var out []string
	for _, s := range st.Others() {
		who := attn.Line(s.User)
		if who == "" {
			who = "session " + attn.Line(s.ID)
		}
		out = append(out, who+" is logged in here as well")
	}
	return out
}

// held is what is holding this off, in the words the program gave logind. It is
// the difference between a power action that is refused for no visible reason
// and one where a person can go and close the thing that is blocking it.
//
// In the program's words, and the program is any program: `systemd-inhibit
// --who=... --why=...` takes two strings from whoever runs it, and every local
// account can run it. So these two are the same kind of thing as a
// notification's summary - somebody else's text, arriving to be shown - and
// they get the same filter (internal/attn, Line). A cost line goes to the power
// menu and, with no shell up, to a terminal, where `--why="$(printf
// '\033[2J')"` would clear the screen the menu was on.
//
// Filtered here rather than in internal/power, which is the logind client and
// has no business knowing where a string is going to be drawn. This is the
// layer that builds the sentence.
func held(st power.State, w power.What) []string {
	var out []string
	for _, b := range st.Blocking(w) {
		who := attn.Line(b.Who)
		if who == "" {
			who = "something on this machine"
		}
		line := who + " is holding it off"
		if why := attn.Line(b.Why); why != "" {
			line += ": " + why
		}
		out = append(out, line)
	}
	return out
}

// powerRun does one of them, and says what it started.
//
// The confirmation is the surface's, not this method's. What arrives here is a
// name on a socket only this user can reach (server.go, allowPeer), sent by a
// surface that asked first or typed by somebody who wrote the word out - and a
// second question here would be a second place for the wording of "what is
// about to be lost" to live, which is the thing that surface exists to say
// well.
func (s *Server) powerRun(name string) Response {
	if name == powerLock {
		// The lock is not logind's. The key, palette and this row all reach this
		// coordinator, so each verifies the same fresh LockedHint transition.
		return s.lockScreen()
	}
	if name == powerSuspend {
		return Response{Error: errSuspendUnavailable.Error()}
	}
	w := power.What(name)
	switch w {
	case power.Logout, power.Reboot, power.PowerOff:
	default:
		return Response{Error: "no power action called " + strconv.Quote(name) +
			": `zde system power` lists them"}
	}
	m, err := s.logins()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if err := m.Do(w); err != nil {
		// logind's refusal, in its own words plus what on this machine is the
		// reason for it. Reported rather than swallowed, which is the whole
		// point of this surface: a power menu that says it worked while nothing
		// happened is worse than one that says it was refused.
		return Response{Error: err.Error()}
	}
	return ok(started(w))
}

// started is what to say about a verb that has been taken. Said in the present
// tense on purpose: logind answers when it has accepted the request, and the
// machine goes down some moments after that, so "rebooting" is true where
// "rebooted" would be a claim about a future nothing here can see.
func started(w power.What) string {
	switch w {
	case power.Logout:
		return "logging out"
	case power.Reboot:
		return "rebooting"
	default:
		return "powering off"
	}
}
