package zded

import (
	"fmt"
	"strconv"
	"time"

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
	// that end the session are; the lock and an unblocked suspend are not,
	// because nothing is lost and a question in front of them is a question
	// people learn to press through.
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

// logins is logind, opened the first time something asks and kept. The same
// shape as the network side and for the same reasons (net.go, links): lazily,
// because a machine without it has to be a state rather than a daemon that will
// not start, and dropped when the connection dies, because a closed bus
// connection answers every call with the same error for ever and a daemon
// holding one would refuse every power action for the rest of the session. It
// is the bus connection that is watched and not logind - a logind restart does
// not close one, and does not need to (internal/power, Alive).
//
// What it does not do is remember an absence. The network side has to, because
// the bar asks about the link every five seconds for the life of the session;
// nothing polls this - it is opened when somebody presses a key - so the
// arithmetic that made a memo worth having is not here.
func (s *Server) logins() (power.Manager, error) {
	s.powerMu.Lock()
	defer s.powerMu.Unlock()
	if s.logind != nil {
		if alive, ok := s.logind.(interface{ Alive() bool }); !ok || alive.Alive() {
			return s.logind, nil
		}
		if closer, ok := s.logind.(interface{ Close() error }); ok {
			closer.Close() //nolint:errcheck // it is already the connection that stopped working
		}
		s.logind = nil
	}
	open := s.openPower
	if open == nil {
		open = power.Open
	}
	m, err := open()
	if err != nil {
		return nil, err
	}
	s.logind = m
	return m, nil
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
	}, {
		Name:    string(power.Logout),
		Label:   "log out",
		Desc:    "end this session and go back to the greeter",
		Confirm: true,
		Costs:   ending,
		Why:     why,
	}, {
		Name:    string(power.Suspend),
		Label:   "suspend",
		Desc:    "sleep, and come back to this session",
		Confirm: len(st.Blocking(power.Suspend)) > 0,
		Costs:   held(st, power.Suspend),
		Why:     why,
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
func others(st power.State) []string {
	var out []string
	for _, s := range st.Others() {
		who := s.User
		if who == "" {
			who = "session " + s.ID
		}
		out = append(out, who+" is logged in here as well")
	}
	return out
}

// held is what is holding this off, in the words the program gave logind. It is
// the difference between a suspend that is refused for no visible reason and one
// where a person can go and close the thing that is blocking it.
func held(st power.State, w power.What) []string {
	var out []string
	for _, b := range st.Blocking(w) {
		who := b.Who
		if who == "" {
			who = "something on this machine"
		}
		line := who + " is holding it off"
		if b.Why != "" {
			line += ": " + b.Why
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
		// The lock is not logind's. It is running whatever this machine calls
		// its locker, which `zde system lock` already resolves out of the apps
		// table (cmd/zde, launch) - so this row runs the registry's own
		// system.lock entry, which is the argv the key spawns and the argv the
		// palette's row spawns. One path, because a second answer to "what locks
		// this screen" is how a machine ends up with a menu that locks and a key
		// that does not.
		if resp := s.runAction("system.lock"); resp.Error != "" {
			return resp
		}
		return ok("locking")
	}
	w := power.What(name)
	switch w {
	case power.Logout, power.Suspend, power.Reboot, power.PowerOff:
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
	case power.Suspend:
		return "suspending"
	case power.Reboot:
		return "rebooting"
	default:
		return "powering off"
	}
}
