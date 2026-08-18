// Package zded is the daemon: it owns the journal, reads the compositor, and
// answers the one socket everything else in zde talks to (docs/vision.md,
// section 2).
//
// The socket is the zde boundary (vision.md, principle 7). It is never mounted
// into a container, and a connection is accepted only from the user who owns
// it - checked with the kernel's peer credentials, which the peer cannot
// forge, rather than anything it tells us about itself.
package zded

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/bt"
	"github.com/crispuscrew/zde/internal/clip"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/link"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/power"
	"github.com/crispuscrew/zde/internal/zinc"
)

// Compositor is what the daemon needs from niri. An interface because zded
// must run, and answer, while the compositor is not there: it is what gets
// asked why the session is broken.
type Compositor interface {
	DeskMap() (*desk.Map, error)
	// FocusedName is the focused workspace, empty if none is.
	FocusedName() (string, error)
	// FocusWorkspace focuses one by name, which is what makes its monitor
	// show it.
	FocusWorkspace(name string) error
	// FocusedOutput is the monitor the focused workspace is on. A window
	// carried to another desk stays on the screen it was on.
	FocusedOutput() (string, error)
	// FocusedPlace is that workspace's name and output together, from one
	// reply, so the two cannot describe different moments.
	FocusedPlace() (name, output string, err error)
	// FocusedWindow is the focused window's id, 0 when none is. Whether it
	// changes is how nav tells a window below from the end of the stack.
	FocusedWindow() (uint64, error)
	// MoveWindowToWorkspace carries the focused window to a workspace by
	// name, leaving focus where it was.
	MoveWindowToWorkspace(name string) error
	// FocusWindowVertically moves focus one window along the stack, and does
	// nothing at the end of it.
	FocusWindowVertically(down bool) error
	// Windows is every open window with the workspace each one is on, from one
	// reading: a list that showed windows from one moment and workspace names
	// from another would offer a row that was never true.
	Windows() ([]Window, error)
	// FocusWindow focuses one by id, wherever it is - which is what makes a
	// jump a journey and not a rearrangement.
	FocusWindow(id uint64) error
	// RenameWorkspace corrects a name that no longer tells the truth.
	RenameWorkspace(from, to string) error
	// SetWorkspaceNameByID names a workspace that has no name to be
	// addressed by, which is what adoption claims.
	SetWorkspaceNameByID(id uint64, name string) error
	// FirstApps is the app of the first window on each workspace, by niri's
	// workspace id. It is what an adopted workspace is named after.
	FirstApps() (map[uint64]string, error)
	// EmptyByOutput is the unnamed, empty workspaces per output - what a
	// declared workspace gets made out of.
	EmptyByOutput() (map[string][]uint64, error)
	// Perform runs one of niri's own actions by the name a bind gives it. Half
	// the keymap is niri's own, and the palette has to be able to run those the
	// same way it runs everything else.
	Perform(action string) error
}

// Desks is where manifests live. An interface rather than a loaded map,
// because a desk written while the session runs should be usable without
// restarting the daemon - and because snapshot writes one back.
type Desks interface {
	// All returns what it could read, what it could not, and an error only if
	// the directory itself is unreadable. A broken manifest is one desk's
	// problem (internal/manifest, LoadDir).
	All() (map[string]*manifest.Desk, []manifest.Problem, error)
	Save(*manifest.Desk) (string, error)
}

// noDesks is a machine with nothing declared, which is where everyone starts.
type noDesks struct{}

func (noDesks) All() (map[string]*manifest.Desk, []manifest.Problem, error) { return nil, nil, nil }
func (noDesks) Save(*manifest.Desk) (string, error) {
	return "", fmt.Errorf("no desks directory to write to")
}

// Request is one line in.
type Request struct {
	Method string   `json:"method"`
	Args   []string `json:"args,omitempty"`
}

// Response is one line back. Exactly one of Ok and Error is set, which is the
// shape niri uses too - one protocol idiom for the whole system.
type Response struct {
	Ok    json.RawMessage `json:"ok,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Status is what `zde status` prints: enough to tell a working session from a
// broken one without reading a log.
type Status struct {
	Version    string `json:"version"`
	Compositor string `json:"compositor"` // "connected", or why not
	Desks      int    `json:"desks"`
	OnDesk     string `json:"onDesk,omitempty"`
	LastDesk   string `json:"lastDesk,omitempty"`
	Skipped    int    `json:"journalSkipped"`
	// Whether a shell is listening for events, which is what draws the picker
	// and the bar. A session where this is false is one where Mod+Tab prints a
	// list and nothing appears - and knowing that from the machine you are
	// sitting at beats guessing at it (docs/install.md, when it breaks).
	Shell bool `json:"shell"`
	// Listeners is how many connections are listening, out of listenersMax.
	// Shell alone was the whole answer and it was the wrong one under load: a
	// session flooded with connections that subscribe and never read has a
	// picker that never appears and a status that says a shell is there, which
	// is the one diagnostic a person runs saying the thing is fine. One shell is
	// one; a number climbing towards the cap is somebody else's process.
	Listeners int `json:"listeners"`
	// Connections is how many connections are open, out of ConnectionsMax. The
	// listener count answered the same question one connection at a time and
	// only for the ones that had subscribed: a flood that opens sockets and
	// sends nothing never reaches subs at all, so `zde status` said "shell no,
	// 0 listening" on a daemon a second away from being killed by its own
	// descriptor limit. Five is a session with a shell in it; a number near the
	// cap is somebody else's program.
	Connections int `json:"connections"`
	// Dropped is how many connections have been closed to make room for a newer
	// one since this daemon started (see admit). Zero on any session nothing is
	// doing this to, and the only evidence left once a flood has stopped.
	Dropped uint64 `json:"dropped"`
	// Notifications says whether zded took the bus name. Without it every
	// notification the session receives goes to whoever did, or nowhere.
	Notifications bool `json:"notifications"`
	// Queued is how many things are waiting, which is the other half of the
	// bar: if the bar is not up, this is the only way to see them.
	Queued int `json:"queued"`
	// Mode is the attn mode: work, focus or quiet (internal/attn). It is here
	// because "why has nothing arrived for an hour" is a question with exactly
	// one cheap answer, and a person who cannot see the bar has only this one.
	Mode string `json:"mode"`
	// Zinc says whether layer 2's runner is on the session's PATH (docs/
	// delivery.md). A zde machine without it can run nothing sandboxed, which
	// is most of what a zde machine is for - and the session's PATH is not the
	// one a terminal has, so this is the daemon's answer and not the CLI's.
	// Both `zde status` and `zde doctor` print it: status is what a person runs
	// first, and one line there is cheaper than the wrong diagnosis.
	Zinc bool `json:"zinc"`
	// Manifests that could not be read, most recently seen. A desk quietly
	// missing is the failure this exists to stop being quiet: the daemon
	// carries on with the manifests that do work, and says here which ones it
	// gave up on.
	BadManifests []string `json:"badManifests,omitempty"`
	// Unplaced is how many arrivals were kept in memory and drew no card only
	// because nothing could say which desk they came in on while a desk here is
	// declared private. It is the fail-closed answer being loud about what it
	// costs: on that machine the alternative is a notification history that
	// empties itself and a session that quietly stops showing anything, neither
	// of them ever saying why (internal/zded, privateArrival).
	Unplaced int `json:"unplaced,omitempty"`
}

// DeskApp is one app a desk declares (docs/model.md, section 5), as the two
// things a manifest turns it into: the address a person and zcr both use for
// it, and the workspace it is pinned to.
//
// Where that instance keeps its state is deliberately not here. It is zinc's
// answer, given by `zcr where`, and asking it means running a program - which
// the daemon that answers every keybind should not be doing on a socket call
// it cannot time out. The client asks (cmd/zde).
type DeskApp struct {
	Address string `json:"address"`
	// Place is the workspace name it is pinned to, empty when it is not:
	// adoption places an unpinned app wherever it opens.
	Place string `json:"place,omitempty"`
}

// Server answers the zde socket.
type Server struct {
	version string
	jrn     *journal.Journal
	niri    Compositor
	desks   Desks
	// launch starts one app instance. A field so a test can watch what a switch
	// asks for without a container runtime under it. It takes the run's context,
	// because what it starts is a subprocess and the daemon has to be able to
	// end it (internal/zinc, Run).
	launch func(ctx context.Context, address string) error
	// spawn runs what a bind would have run, for the palette. A field for the
	// same reason launch is one: what the palette starts is the thing worth
	// asserting, and a test should not have to start a terminal to see it.
	spawn func(argv []string) error

	notifier Notifier
	// history is what has arrived, whatever the mode did about it. A bounded
	// ring per sender, and in memory rather than in the journal: see
	// attn.PerSenderMax. The short end of it is written to a file of its own,
	// which written keeps track of (history.go).
	history attn.History
	written written

	// The popup path (attn.go). A buffered channel and one goroutine, because
	// an arrival comes off the bus with an app blocked on the reply and must
	// never wait for a shell to draw: see pop. popupStop is what ends the pump,
	// closed once by Close.
	popups    chan Event
	startPump sync.Once
	popupStop chan struct{}
	stopPump  sync.Once
	// saidQueueFull keeps the queue's ceiling to one line a session. The thing
	// that reaches it is a flood, so a message per arrival would be the flood
	// again in the log - and the state it describes is visible in `zde queue`
	// for as long as it lasts, which is where somebody would look anyway.
	saidQueueFull sync.Once

	// The clipboard side (clip.go). clips is what was copied - bounded, in
	// memory, and expiring on its own, for the reasons internal/clip gives at
	// length.
	//
	// clipboard is how the session's clipboard is reached, and it is nil until
	// somebody says otherwise (see UseClipboard). Deliberately not defaulted to
	// the real thing: this one spawns processes against whatever Wayland session
	// the machine happens to have, and the 129 servers the tests build would
	// then be 129 daemons reading the developer's own clipboard. The compositor
	// is a constructor argument for the same reason; this is a setter only
	// because New already has four.
	clips     clip.History
	clipboard Clipboard
	// clipWhy is why nothing is watching, when nothing is - a machine with no
	// wl-clipboard, or a compositor that will not have it. Kept because an empty
	// history looks the same either way from a keyboard (see Clips.Why).
	clipWhy string

	// The network side (net.go). openLink is a field for the same reason launch
	// is: the tests need a manager without a system bus under them, and the
	// machine this runs on may have no NetworkManager at all.
	linkMu   sync.Mutex
	link     link.Manager
	openLink func() (link.Manager, error)
	// What the last dial said when there was no manager to reach, and when it
	// said it. A machine with no NetworkManager is the ordinary desktop, and the
	// bar asks every five seconds (net.go, noManagerFor).
	noManager   error
	noManagerAt time.Time

	// The radio, opened on first use and kept (bluetooth.go). openBluetooth is
	// a field so a test can drive the verbs without a system bus under them.
	//
	// Two locks, and which one covers what is the difference between a daemon
	// that stops when it is told to and one that does not. bluetoothDial is held
	// across the dial, because two callers that both dialled would both register
	// a pairing agent and the survivor can be the one BlueZ is not calling.
	// bluetoothMu covers the field alone and is never held across anybody's
	// round trip, so Close can give the radio up while a dial is still in flight
	// (bluetooth.go, radio and closeRadio).
	bluetoothDial sync.Mutex
	bluetoothMu   sync.Mutex
	bluetooth     Bluetooth
	// bluetoothGone counts the times the radio has been given up, so a dial
	// that was in flight when the session ended can tell that what it is
	// holding belongs to nobody.
	bluetoothGone uint64
	openBluetooth func() (Bluetooth, error)

	// logind, for the power menu (power.go). Opened on first use and kept, like
	// the two above, and for the third time for the same reason: a machine that
	// has none is a state rather than a daemon that will not start. openPower is
	// a field so a test can drive a log out without ending the machine it runs
	// on.
	powerMu   sync.Mutex
	logind    power.Manager
	openPower func() (power.Manager, error)
	// What the last dial said when there was no logind to reach, and when it
	// said it. Somebody leaning on the power key is not a poll, but it is
	// enough dials to be worth not making (power.go, noLogindFor).
	noLogind   error
	noLogindAt time.Time

	// The subprocess runs in flight: the tiers (ask.go) and the desk launches
	// (startApps). A run is a subprocess in a process group of its own,
	// deliberately, so that stopping it stops what it started - and that same
	// choice is why the session's own signal never reaches it. Without something
	// here, `systemctl --user stop zded` left the tier and everything it forked
	// running under pid 1: measured, a shell tier and its `sleep 600` still
	// there eighteen seconds after the daemon exited, and a local tier is a
	// model that can be holding a GPU.
	//
	// The launches were outside all of it until they were put here, and on a
	// path that starts more processes than a tier does: measured, Close returned
	// in 0s with a launch subprocess still running, and the subprocess survived
	// the daemon.
	//
	// runCtx is the parent of every run's context, so cancelling it runs each
	// cmd.Cancel and kills each group. runs is how Close knows when they have
	// gone. Both are touched under mu, which is what keeps a run being added
	// from racing the wait for them.
	runCtx  context.Context
	runStop context.CancelFunc
	runs    sync.WaitGroup

	mu       sync.Mutex
	ln       net.Listener
	problems []string
	// unplaced counts the arrivals this session refused to write down and drew
	// no card for, only because nothing could say which desk they were on, on a
	// machine that declares a private desk (history.go, couldNotPlace).
	unplaced uint64
	// asks is how many tiers are running right now, which is the count asksMax
	// is a ceiling on (ask.go, claimAsk). Separate from runs, which is a wait
	// group and cannot be read: what Close needs is to know when they have all
	// gone, and what a new run needs is to know how many there are.
	asks int
	// launching is the desks whose apps are being started right now, and it is
	// the whole of what bounds the launches (startApps says why one per desk is
	// the right unit and why there is no number beside it).
	launching map[string]struct{}
	// conns is every connection this daemon is holding, which is the population
	// ConnectionsMax is a ceiling on (see admit). Separate from subs, because
	// the two questions are different: subs is who is being broadcast to, and
	// this is who is costing a descriptor.
	conns map[*sink]struct{}
	// dropped counts the connections closed to make room for a newer one. Kept
	// for the same reason unplaced is: it is the only evidence left after a
	// flood has ended that a session's own connections were being churned, and
	// the count is on `zde status` where somebody will meet it.
	dropped uint64
	subs    map[*sink]struct{}
	waiting map[string]chan struct{}
	tokens  uint64
}

// runStopWait is how long Close waits for the tiers and the desk launches to
// go. They are sent a kill to the whole process group rather than asked
// politely, so this is the time it takes a dead process to be reaped and a
// goroutine to unwind, which is milliseconds - and it is a ceiling rather than a
// delay. Bounded at all because the alternative is a logout that waits on a
// model, or on podman: whatever a subprocess does with a signal, the session
// ends.
const runStopWait = 2 * time.Second

// New builds the daemon.
//
// The compositor is required, and deliberately not guarded for at the dispatch
// boundary. cmd/zded makes one and hands it over, so the only way a nil reaches
// a method is code that was wired up wrong, and a Dispatch that turned that into
// "the compositor is not connected" would report a wiring mistake as a session
// problem - on every surface, for the rest of the run, with the real cause a
// stack frame nobody ever sees. The panic names the line instead. A daemon that
// cannot reach a niri that is really there is a different thing and already has
// an answer: `zde status` says so on the compositor line.
func New(version string, jrn *journal.Journal, compositor Compositor, desks Desks) *Server {
	if desks == nil {
		desks = noDesks{}
	}
	runCtx, runStop := context.WithCancel(context.Background())
	return &Server{
		version:  version,
		jrn:      jrn,
		niri:     compositor,
		desks:    desks,
		launch:   zinc.Run,
		spawn:    spawnDetached,
		openLink: link.Open,
		runCtx:   runCtx,
		runStop:  runStop,
		// Neither radio is dialled here: opening a system bus connection at
		// startup would be zded doing that work on every machine, including the
		// ones that have no radio and never asked for one (bluetooth.go, radio;
		// net.go, links).
		openBluetooth: func() (Bluetooth, error) { return bt.Dial() },
		// Made here rather than beside the pump it stops, because Close may run
		// on a daemon that never received a notification and closing a nil
		// channel is a panic on the way out of a session (attn.go, pop).
		popupStop: make(chan struct{}),
	}
}

// DefaultSocket is where the socket lives: the runtime directory, which the
// kernel clears between logins and which is already private to the user.
func DefaultSocket() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", errors.New("zded: XDG_RUNTIME_DIR is not set, and the zde socket belongs nowhere else")
	}
	return filepath.Join(dir, "zde", "zded.sock"), nil
}

// Listen binds the socket. A stale socket from a crashed zded is removed, but
// only after a dial proves nobody is answering on it - two daemons quietly
// fighting over one socket is worse than refusing to start.
func (s *Server) Listen(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return fmt.Errorf("zded: another zded is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// Belt and braces with the 0700 directory: the socket itself is the
	// boundary, so it says so too.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	return nil
}

// Serve answers connections until the listener is closed.
func (s *Server) Serve() error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return errors.New("zded: Serve before Listen")
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

// Close stops listening, gives up the radio, and stops the tiers and the
// launches.
//
// The radio first, and outside s.mu: it is a bus connection with a pairing
// agent exported on it, and one left behind is an agent for a session that has
// ended - bluetoothd would keep calling it and every question would time out
// into a refusal nobody was asked for.
//
// The subprocesses last, and after the listener rather than before it, so that
// nothing new can be asked while this waits for what is already running.
// Bounded (see runStopWait), and safe to call twice: the whole of what this
// daemon can leave behind is a subprocess, and the caller that ends the process
// has to be able to end them too whichever way it got here.
func (s *Server) Close() error {
	s.closeRadio()
	// And the popup pump, once and never twice: Close is reached from a signal
	// handler and from the ordinary way out, and closing a closed channel is a
	// panic on the last line of a session. It is not under s.mu either, because
	// the pump broadcasts and broadcast takes that lock.
	s.stopPump.Do(func() { close(s.popupStop) })
	s.mu.Lock()
	ln := s.ln
	s.ln = nil
	s.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
	}
	s.stopRuns()
	return err
}

// startRun runs a tier in its own goroutine and counts it as in flight, or
// refuses because this daemon is stopping.
//
// Counted under mu, and refused once runCtx is cancelled, which together are
// what keep the count from being raised while stopRuns is waiting on it: after
// the cancel there is no path that adds another.
func (s *Server) startRun(k *sink, args []string) {
	s.mu.Lock()
	if s.runCtx.Err() != nil {
		s.mu.Unlock()
		k.reply(Response{Error: "zded is stopping, so there is nothing to ask it"})
		return
	}
	s.runs.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.runs.Done()
		s.askRun(k, args)
	}()
}

// stopRuns ends every subprocess this daemon started - the tiers and the desk
// launches - and waits, briefly, to see them go.
//
// The cancel is what does it: each run's context has runCtx as its parent, and
// cancelling reaches cmd.Cancel, which kills the process group rather than the
// one pid - which is the whole reason the group exists (see askRun, and
// internal/zinc Run). The wait is only so that the process does not exit out
// from under the kill it has just sent; a group that has been killed is gone, so
// the ceiling is there for the case that is not true rather than for the
// ordinary one.
func (s *Server) stopRuns() {
	s.mu.Lock()
	s.runStop()
	s.mu.Unlock()

	gone := make(chan struct{})
	go func() {
		s.runs.Wait()
		close(gone)
	}()
	t := time.NewTimer(runStopWait)
	defer t.Stop()
	select {
	case <-gone:
	case <-t.C:
		// Said rather than swallowed: what is left is a tier or a launch that
		// did not die when its group was killed, which is a thing worth finding
		// in a log.
		fmt.Fprintf(os.Stderr, "zded: something it started was still running %v after being stopped\n", runStopWait)
	}
}

// requestMax is how long one request line may be. It is the socket's answer to
// a caller that opens a connection and never sends a newline.
//
// Without it the read grows a buffer to hold whatever arrives, so the daemon is
// an allocator anything on this machine can drive. Measured on a running zded:
// four connections each pushing 200 MB with no newline in them took RSS from
// 8 MB to 2856 MB, which is about twice the bytes sent - a grow allocates the
// bigger buffer and copies, so both are live for the length of the copy - and
// none of it came back when the connections closed. `zde status` answered
// instantly throughout, so the one diagnostic a person would run said the
// daemon was fine.
//
// The arithmetic, and it is set by the largest thing a legitimate caller sends,
// which is an ask.run carrying a conversation:
//
//   - askContextMax bounds the document one run may read at 64 KiB (ask.go).
//     Every argument of that request ends up in that document, so nothing a
//     caller may usefully send is outside this number.
//   - The request's framing is the smaller of the two. `{"method":"ask.run",
//     "args":[]}` is 29 bytes and each argument costs a comma and two quotes,
//     against 25 or 27 bytes of `{"who":...,"text":...}` per turn in the
//     document. So framing never makes the request the bigger side.
//   - The escaping can, and by a lot. askDoc encodes with HTML escaping off on
//     purpose, so that a pasted patch is weighed at what it is. A caller has no
//     such duty and the Go client has no such setting: Client.Call uses
//     json.Marshal, which writes "<", ">" and "&" as six-byte \u escapes. A
//     conversation made of those and nothing else is six times its own length
//     on the wire, so 64 KiB of document is 384 KiB of request.
//
// So 384 KiB is the ceiling for a caller doing nothing wrong, and 1 MiB is that
// with room to spare. It is also the number internal/journal already picked for
// the same job on a replayed line (journal.go, replay), and one bound for "a
// line zde will read" beats two. At the cap, the four connections above cost
// 4 MiB of buffer rather than 2856 MB.
const requestMax = 1 << 20

// ConnectionsMax is how many connections zded keeps open at once.
//
// requestMax bounds what one connection may spend and listenersMax bounds how
// many of them may subscribe, so what a flood costs is now linear in
// connections rather than in bytes - and nothing bounded the connections.
// Measured on a running zded over its real socket, connections opened and then
// held idle:
//
//	idle          rss=9 MB    fds=7
//	10,000 open   rss=99 MB   fds=10,007
//	100,000 open  rss=882 MB  fds=100,007
//
// About 9 KB and one descriptor each, opened at 65,000 a second by a program
// that does nothing but dial, and none of the memory comes back: closing all
// 100,000 left RSS at 905 MB.
//
// The end of that line is not a slow daemon, it is no daemon. accept4 returns
// EMFILE at RLIMIT_NOFILE, which is 524,288 here; Serve returns that error, run
// returns it, and the process exits and removes its own socket on the way out
// (cmd/zded, run). Measured at a lowered limit, because reaching the real one
// costs 4.6 GB: 262,138 connections in 4.3 seconds, then "accept4: too many
// open files", then no zded and no socket file for anything to dial. That is
// the session, from a program that opens sockets and sends nothing.
//
// The arithmetic, and it is set by what a session really opens:
//
//   - The shell holds four, one per Dialer in shell/shell.qml: the bar's poll,
//     the event stream, the ask window's, and the network surface's. They are
//     four on purpose - an answer arriving in pieces must not sit in front of
//     the acknowledgement a picker is waiting for.
//   - A shell being restarted holds eight for the moment before the daemon
//     reads EOF on the old four, and every home-manager switch restarts it.
//   - A `zde` verb dials, asks and exits, so a terminal costs one connection for
//     the length of one call and a keybind costs the same. Half a dozen
//     terminals and a leader key is another handful.
//
// So twenty is a session being worked hard, and the floor is not much below it
// either: every listener is a connection too, and listenersMax allows 16.
//
// Two hundred and fifty-six is that with an order of magnitude of room, and at
// the cap the daemon holds about 2.3 MB of connections and 263 descriptors -
// against 105 MB and 10,007 for a flood a tenth the size of the one that ends
// the daemon.
//
// Generous rather than tight, because the two ways of being wrong do not cost
// the same. Too high and zded holds a couple of megabytes it did not need. Too
// low and a connection somebody wanted is the one closed to make room, and the
// connections a session wants are the shell's.
const ConnectionsMax = 256

// admit adds a connection and answers the one that has to go to make room for
// it, and how many of the table the process that owned it was holding. Nil when
// there was room.
//
// Dropping rather than refusing the newest, and that choice is the whole of why
// a cap on connections is safe to have at all. Refusing means the connection
// refused may be the shell's, dialling again after a switch restarted zded -
// and a shell that cannot reconnect is the failure fix/bar-redial exists to
// prevent, arrived at from the other end. Dropping means a connection dialling
// into a full table always gets in.
//
// What has to go is chosen by how much of the table the process on the other
// end is holding, and only then by how long this connection has gone without
// asking anything. That order is the fix for what the first version of this
// measured, and it is worth setting out what went wrong with the obvious
// answer.
//
// The obvious answer is "the longest idle goes", and it inverts under one byte.
// Idle was time since the last line arrived, stamped before the line was
// parsed, so a connection sending "\n" - a malformed request, answered with an
// error - counted as one that had just asked something. Measured against a
// running daemon: a flood holding 256 connections and writing one byte on each
// of them kept every one of its own, chose which of the session's connections
// was dropped (targets 130, 7 and 255, each first try), and dropped a shell
// three rounds running before its `{"method":"events"}` could reach listen. The
// connections that had genuinely asked nothing were the session's own: a `zde`
// verb sitting inside its 200ms ackWait, the bar between polls.
//
// Making the metric "last request that parsed" (events.go, sink.touch) raises
// that price from one byte to one well-formed line, and no further. So the
// first key is not about what a connection sent at all. A process holding 200
// of 256 connections is holding them however quiet or noisy it is, and the only
// way to hold that many while looking thin is to spread them over processes -
// which costs a process each, not a byte each.
//
// What that buys, said plainly. The shell holds four connections in one process
// (shell/shell.qml, one Dialer each) and a `zde` verb holds one for the length
// of a call. A flood in one process is the largest holder in the table from its
// fifth connection onwards, so every connection it opens past the cap takes out
// one of its own - "a flood pays for its own slots", now a statement about the
// flood rather than about how quiet it is. To take a session connection instead
// it must hold the table with processes that each hold fewer than the shell's
// four, which is 84 processes for 252 slots, forked and kept alive, and it must
// keep them asking. That is the price, and it is the honest ceiling on this: a
// program that can fork 84 processes as this user can do worse things to the
// session than close a socket.
//
// A listener is not a candidate at all. Measured on its own traffic the event
// stream is the quietest connection zded has - it says `events` once at login
// and then reads for the rest of the session - so it is precisely what "longest
// without asking anything" would find first, and it is the one connection whose
// loss is the shell going dark. The exemption cannot be turned into a way of
// filling the table, because listenersMax already bounds the listeners at 16
// and a seventeenth is refused in a sentence (events.go, listen). That separate
// cap is what makes this one affordable.
//
// Nor is a connection with a tier running on it. An ask.run takes as long as a
// model takes, up to askTimeout of two minutes, and for all of it the
// connection has nothing more to send and is idle by this measure - while a
// person is sitting in front of the answer. Bounded the same way: asksMax
// allows four across the whole daemon (ask.go, claimAsk).
//
// The exempt connections still count towards what their process is holding.
// They are connections it holds, and a process that parks sixteen listeners is
// exactly the one whose other connections should go first - which is also what
// keeps the two exemptions from being a way to look thin.
//
// The connection that has just arrived is not a candidate while any other one
// is, and that is deliberate in two directions. It is what makes the guarantee
// above true through the gap between being accepted and saying what it is: a
// shell's event loop takes a moment to get to its `events` line, and for that
// moment the connection is a candidate like any other. And it closes a race the
// timestamps left open - k used to be stamped before this lock was taken, so a
// flood that kept every one of its own connections freshly stamped could make
// the connection waiting on the lock the oldest thing in the table by the time
// it got in, and be answered with its own eviction. Now k is stamped under the
// lock and skipped in the walk, so it is chosen only when there is nothing else
// to choose.
//
// A linear walk rather than anything ordered, twice over: once to count what
// each process holds and once to choose. It is 512 map operations on 256
// entries under a lock a keypress also takes, which is a few microseconds, and
// it is paid by the goroutine of the connection that arrived - so under a
// flood, the flood is what waits for it.
//
// The honest cost, because dropping is what buys the guarantee. A flood that
// keeps dialling rather than holding what it has turns the table over in
// milliseconds, and a session connection can still be chosen once the flood is
// spread thin enough - a `zde` verb inside ackWait, the bar between polls. It is
// dropped, and it is told so in a sentence it can print. What no flood can do is
// what refusing would have let it do, which is keep a shell out: the connection
// arriving is never the one chosen while anything else can be, and the event
// stream, once it exists, is exempt. Keeping a shell out of the listeners is a
// different thing and remains possible - sixteen connections that subscribe
// first are sixteen the shell cannot be (events.go, listenersMax).
func (s *Server) admit(k *sink) (*sink, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Under the lock, so that an arriving connection is the most recently used
	// thing in the table at the moment the table is walked (see above).
	k.touch()
	if s.conns == nil {
		s.conns = map[*sink]struct{}{}
	}
	s.conns[k] = struct{}{}
	if len(s.conns) <= ConnectionsMax {
		return nil, 0
	}
	// How much of the table each process on the other end is holding. Counted
	// here rather than kept as a running tally, because the table is walked
	// anyway and a tally is a second thing to keep true through every way a
	// connection can leave.
	holds := make(map[int32]int, len(s.conns))
	for c := range s.conns {
		holds[c.pid]++
	}
	var out *sink
	var held int
	var idle int64
	for c := range s.conns {
		if c == k {
			continue
		}
		if _, listening := s.subs[c]; listening {
			continue
		}
		if c.asking.Load() {
			continue
		}
		n, last := holds[c.pid], c.asked.Load()
		if out == nil || n > held || (n == held && last < idle) {
			out, held, idle = c, n, last
		}
	}
	if out == nil {
		// Every other connection in the table is exempt, so the one that has
		// just arrived pays for its own slot. It is told why, like any other,
		// and the alternative is a cap that silently is not one. At today's
		// numbers this needs the table to be 256 listeners and tiers, against
		// the 20 those two caps allow between them.
		out, held = k, holds[k.pid]
	}
	delete(s.conns, out)
	s.dropped++
	return out, held
}

// forget takes a connection out of the table when its read loop ends. Deleting
// one that has already gone - the connection admit dropped, which is the same
// map entry - is what a delete of a missing key does, which is nothing.
func (s *Server) forget(k *sink) {
	s.mu.Lock()
	delete(s.conns, k)
	s.mu.Unlock()
}

// held is how many connections are open. For tests; `zde status` reads the
// count under the same lock as the drop count, because the two are one
// sentence (see status). Named for what it counts rather than what it counts
// of, because Server.connections is already the network surface (net.go).
func (s *Server) held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	// Every write to this connection goes through the sink, because two of them
	// can now happen at once: a reply to something asked, and an event pushed
	// while that reply is being written. Interleaved, they would produce one
	// line that is neither.
	//
	// Made before the peer is checked so that the refusal goes through it too. A
	// caller that is refused is by definition one this daemon knows nothing
	// about, and a bare write to it is a write with no deadline to a client that
	// need not ever read - the last of which took a fix (events.go, replyWait).
	k := &sink{w: conn}
	pid, err := allowPeer(conn)
	if err != nil {
		// Say nothing useful to a peer that should not be here.
		k.reply(Response{Error: "not permitted"})
		return
	}
	// Who is on the other end, which is how the connection cap tells a session's
	// four connections from somebody's four hundred (see admit). Set before this
	// sink is in any table, which is what makes it safe to read without a lock.
	k.pid = pid
	defer s.unlisten(k)
	// And the run this connection started, if it started one, is told the
	// connection has gone (ask.go, askRun). Deferred here because this is the
	// one place that finds out: the read ends when the peer closes, and nothing
	// else on the daemon notices a tier whose asker has left.
	defer k.end()
	// And counted, because connections are the last thing about this socket that
	// nothing bounded (see admit). Deferred before the count is taken so that a
	// connection dropped for its own slot leaves the table on its way out too.
	defer s.forget(k)
	if out, held := s.admit(k); out != nil {
		// Told what happened rather than just closed (events.go, sink.drop). The
		// numbers are in the sentence because they are what make the rest of it
		// make sense: a person who reads this wants to know whether zded is
		// broken or busy, and 256 open connections says which - and whether the
		// program that lost this connection was holding one of them or two
		// hundred says whose fault it was.
		//
		// On a goroutine of its own, because the sentence is bounded at sendWait
		// and the connection that has just arrived is the one that would pay it.
		// That connection may be the shell dialling again into a full table, and
		// 200ms is the whole of ackWait. The goroutine writes one line to a
		// connection already out of the table and ends inside sendWait.
		go out.drop(fmt.Sprintf(
			"zded closed this connection to make room: it was holding %d, which is every one it keeps, and of the %d open from this process this was the one that had gone longest without asking anything. zded is running - dial again",
			ConnectionsMax, held))
		if out == k {
			// Every other connection in the table was exempt, so this one paid
			// for its own slot (see admit). It needs 256 listeners and tiers to
			// happen, against the 20 those caps allow, and it is handled rather
			// than asserted because the alternative if that ever stops being
			// true is a cap that silently is not one.
			return
		}
	}

	// A scanner rather than a reader, for the cap: bufio.Scanner is what takes
	// a maximum, and it is the same mechanism and the same ceiling the journal
	// replays lines with. Started at 4 KiB, which is what bufio.NewReader
	// allocated here before, so an ordinary request costs exactly what it used
	// to and only a caller heading for the cap pays for the growth.
	r := bufio.NewScanner(conn)
	r.Buffer(make([]byte, 0, 4<<10), requestMax)
	for r.Scan() {
		var req Request
		// Valid until the next Scan, and json.Unmarshal copies what it keeps
		// into the request's own strings, so nothing below outlives the buffer.
		if err := json.Unmarshal(r.Bytes(), &req); err != nil {
			k.reply(Response{Error: "malformed request"})
			continue
		}
		// This connection is being used, which is part of what keeps it out of
		// the way when something has to be dropped to make room (see admit).
		// After the parse and not before it: this used to be recorded for
		// anything that arrived, on the reasoning that a client sending
		// malformed requests is a client sending - and that made the measure
		// forgeable with one byte, by a flood that then chose which of the
		// session's connections was dropped (events.go, touch).
		k.touch()
		if req.Method == MethodEvents {
			// The connection stays a connection: it keeps answering requests,
			// and events arrive on it as well. A client that wanted a second
			// socket for them can have one, and one that does not need not.
			if len(req.Args) != 0 {
				k.reply(Response{Error: MethodEvents + " takes no arguments"})
				continue
			}
			if !s.listen(k) {
				k.reply(Response{Error: fmt.Sprintf(
					"zded is already listening for %d connections, which is every one it keeps: close one before opening another",
					listenersMax)})
				continue
			}
			k.reply(ok("listening"))
			continue
		}
		if req.Method == MethodAskRun {
			// Answered by the connection, like events, and for a stronger
			// reason: the answer is a stream of lines to this connection alone,
			// and it takes as long as a model takes. In a goroutine so that this
			// read loop keeps answering meanwhile - the shell acknowledges a
			// picker on the connection it asks on, and a twenty second answer
			// must not be what Mod+Tab waits for.
			//
			// Through startRun rather than a bare go, so that the daemon knows
			// what it has started: a tier is a subprocess in a process group of
			// its own, and nothing else would stop it when the session ends.
			s.startRun(k, req.Args)
			continue
		}
		if req.Method == MethodClip && len(req.Args) == 1 {
			// Putting an entry back spawns wl-copy, which internal/clip bounds
			// at three seconds and nothing bounds faster. On this loop those are
			// three seconds in which nothing else on this connection is read -
			// and the shell acknowledges every surface on the connection it asks
			// on, within ackWait, which is 200ms. So pressing Enter on a row and
			// then reaching for another key would let that key's acknowledgement
			// sit unread, and zde would take the shell-is-dead path and print the
			// list to a terminal. For Mod+v that means printing the clipboard
			// history, which is the one place it should not go.
			//
			// The same reasoning ask.run is off this loop for, at a smaller size:
			// the answer takes as long as something outside zde takes, and a
			// keypress must not be what waits for it.
			go s.clipPutOn(k, req.Args[0])
			continue
		}
		k.reply(s.Dispatch(req))
	}
	// Why the reading stopped. An ordinary end is the peer closing, and there is
	// nobody left to tell; a line past the cap is the one case where somebody is
	// still there and owed a sentence, so it is said before the deferred Close
	// ends the connection. Ended rather than resynchronised on purpose: what is
	// still in the socket is the tail of something this daemon has already
	// refused, and reading on would answer whatever happened to follow the next
	// newline in it as though it were a request of its own.
	if errors.Is(r.Err(), bufio.ErrTooLong) {
		k.reply(Response{Error: fmt.Sprintf(
			"that request is longer than %d KiB, which is more than any zde request carries: the largest is an ask.run at %d KiB of conversation",
			requestMax>>10, askContextMax>>10)})
	}
}

// allowPeer refuses anyone but the user who owns this zded, and answers which
// process is on the other end. Peer credentials come from the kernel, so a
// caller cannot claim to be someone else - which is the same reasoning as
// attribution by channel (vision.md, principle 6).
//
// The pid comes back with the same credentials the uid is checked from, so it
// costs nothing extra and cannot be forged either. What it is for is the
// connection cap, which has to tell a session's handful of connections from one
// program's several hundred, and cannot do it by anything the connections say
// (see admit). Pids are reused by the kernel, and that is harmless here: it is
// only ever compared with the pids of other connections in the table, so the
// worst a reused one can do is group two connections that are not related.
func allowPeer(conn net.Conn) (int32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	if uint32(os.Getuid()) != cred.Uid {
		return 0, fmt.Errorf("uid %d is not %d", cred.Uid, os.Getuid())
	}
	return cred.Pid, nil
}

// Dispatch answers one request. Exported so the methods can be tested without
// a socket in the way.
func (s *Server) Dispatch(req Request) Response {
	switch req.Method {
	case "status":
		return ok(s.status())
	case MethodShown:
		if len(req.Args) != 1 {
			return Response{Error: MethodShown + " takes the token from the event"}
		}
		return s.acknowledge(req.Args[0])
	case MethodEvents:
		// Handled by the connection rather than here, because subscribing is a
		// thing a connection becomes and not a question with an answer (see
		// handle). Named here so it is a method zded has rather than one it has
		// never heard of - which is what anything checking the protocol from
		// outside will ask.
		return Response{Error: "events is asked of a connection, and this one is not keeping it open"}
	case "desk.switcher":
		if len(req.Args) != 0 {
			return Response{Error: "desk.switcher takes no arguments"}
		}
		return s.switcher()
	case "ask.oneshot":
		// The surface, and only the surface. A question typed at a terminal is
		// answered at that terminal (cmd/zde, ask), which is what a oneshot is,
		// so one never arrives here to be drawn.
		if len(req.Args) != 0 {
			return Response{Error: "ask.oneshot takes no arguments: the question is typed into the window"}
		}
		return s.askSurface(EventAsk, "")
	case "ask.panel":
		// The panel, optionally with the first question already in it. That is
		// the difference between the two verbs and not a convenience: `zde ask
		// panel <question>` names a window that stays open, so the question
		// goes to the window and the answer arrives there, where the next
		// question can build on it.
		if len(req.Args) > 1 {
			return Response{Error: "ask.panel takes the question to open with, or nothing to open a panel to type into"}
		}
		question := ""
		if len(req.Args) == 1 {
			// Trimmed here rather than in the window, so that what the surface
			// draws and what a tier is handed are the same string: askRun trims
			// too, and a question that arrived on stdin brings a newline with
			// it. Refused when that leaves nothing, in the words askRun uses -
			// an empty question is the one thing no tier can be asked.
			question = strings.TrimSpace(req.Args[0])
			if question == "" {
				return Response{Error: "nothing to ask: say what the question is"}
			}
		}
		return s.askSurface(EventAskPanel, question)
	case MethodAskRun:
		// Handled by the connection rather than here (see handle), for the same
		// reason events are: an answer is not one reply. Named here so it is a
		// method zded has rather than one it has never heard of.
		return Response{Error: MethodAskRun + " is asked of a connection that keeps it open, and this one is not"}
	case "window.jump-to":
		// One verb, two arities. The picker and the choice are the same
		// question asked twice - which window - and with none to ask it of, the
		// id printed by the first form is what the second one takes, so
		// `zde window jump-to $(zde window jump-to | ... )` needs no second name
		// to learn.
		switch len(req.Args) {
		case 0:
			return s.jumpTo()
		case 1:
			return s.focusWindow(req.Args[0])
		default:
			return Response{Error: "window.jump-to takes one window id, or none to open the picker"}
		}
	case "net.connections":
		// The surface, and the list for whoever has no surface. One verb for
		// both, the way desk.switcher is one verb for both.
		if len(req.Args) != 0 {
			return Response{Error: "net.connections takes no arguments"}
		}
		return s.connections()
	case "system.power":
		// One verb, two arities, for the reason window.jump-to has two: the
		// menu and the choice are the same question - which one - asked twice,
		// and with no surface to ask it of, the name printed by the first form
		// is what the second one takes.
		switch len(req.Args) {
		case 0:
			return s.powerMenu()
		case 1:
			return s.powerRun(req.Args[0])
		default:
			return Response{Error: "system.power takes one action name, or none to open the menu"}
		}
	case "system.idle":
		// No arity, unlike system.power: there is nothing to do about an idle
		// hold from here. zde cannot drop somebody else's inhibitor and would
		// not want a key that did - this is a reading, and the thing to do about
		// it is to go and close what is holding it.
		if len(req.Args) != 0 {
			return Response{Error: "system.idle takes no arguments"}
		}
		return s.idleHold()
	case "net.status":
		if len(req.Args) != 0 {
			return Response{Error: "net.status takes no arguments"}
		}
		return s.netStatus()
	case "net.list":
		if len(req.Args) != 0 {
			return Response{Error: "net.list takes no arguments"}
		}
		return s.netList()
	case "net.connect":
		// One argument for a network that needs no password from anybody - an
		// open one, or one NetworkManager has a profile for - and two when the
		// password is being given. Never three: there is nothing else to say
		// about joining a network, and an argument nobody reads is a place for
		// a secret to end up.
		if len(req.Args) != 1 && len(req.Args) != 2 {
			return Response{Error: "net.connect takes a network name, and a password when it needs one"}
		}
		return s.netConnect(req.Args)
	case "net.forget":
		if len(req.Args) != 1 {
			return Response{Error: "net.forget takes one network name"}
		}
		return s.netForget(req.Args[0])
	case "net.disconnect":
		if len(req.Args) != 0 {
			return Response{Error: "net.disconnect takes no arguments"}
		}
		return s.netDisconnect()
	case MethodClip:
		// One verb, two arities, the way window.jump-to has them: the list and
		// the choice are the same question - which entry - and with no surface
		// to ask it of, the id printed by the first form is what the second one
		// takes.
		switch len(req.Args) {
		case 0:
			return s.clipHistory()
		case 1:
			return s.clipPut(req.Args[0])
		default:
			return Response{Error: "clip.history takes one entry id, or none to open the history"}
		}
	case "clip.clear":
		if len(req.Args) != 0 {
			return Response{Error: "clip.clear takes no arguments: it forgets the lot"}
		}
		return s.clipClear()
	case "palette.list":
		if len(req.Args) != 0 {
			return Response{Error: "palette.list takes no arguments"}
		}
		return s.palette()
	case "palette.run":
		// One name, and the name is the whole of it. A palette that took a
		// method and its arguments would be a way to ask zded for anything at
		// all from a surface, and what this runs is what a key already runs.
		if len(req.Args) != 1 {
			return Response{Error: "palette.run takes one action name"}
		}
		return s.runAction(req.Args[0])
	case "desk.list":
		m, err := s.niri.DeskMap()
		if err != nil {
			return Response{Error: err.Error()}
		}
		return ok(m.DeskNames())
	case "desk.switch":
		if len(req.Args) != 1 {
			return Response{Error: "desk.switch takes one desk name"}
		}
		return s.switchDesk(req.Args[0])
	case "nav.down", "nav.up":
		if len(req.Args) != 0 {
			return Response{Error: req.Method + " takes no arguments"}
		}
		return s.nav(req.Method == "nav.down")
	case "desk.move-window":
		if len(req.Args) != 1 || (req.Args[0] != "next" && req.Args[0] != "prev") {
			return Response{Error: "desk.move-window takes next or prev"}
		}
		if req.Args[0] == "next" {
			return s.moveWindow(desk.Next)
		}
		return s.moveWindow(desk.Prev)
	case "desk.move-window-to":
		// A verb of its own rather than another word this one accepts,
		// because "next" and "prev" are desk names anybody may use, and a
		// desk you cannot reach because of what you called it is a trap.
		//
		// Two arities, the way window.jump-to has them: the picker and the
		// choice are one question - which desk - and with no surface to ask it
		// of, what the first form printed is what the second one takes. It is
		// what gives a chord something to do at all, since the argument is a
		// name only the person standing there knows.
		switch len(req.Args) {
		case 0:
			return s.deskPicker(EventPickerMoveWindow)
		case 1:
			return s.moveWindowTo(req.Args[0])
		default:
			return Response{Error: "desk.move-window-to takes one desk name, or none to pick one"}
		}
	case "desk.move-workspace-to":
		switch len(req.Args) {
		case 0:
			return s.deskPicker(EventPickerMoveWorkspace)
		case 1:
			return s.moveWorkspaceTo(req.Args[0])
		default:
			return Response{Error: "desk.move-workspace-to takes one desk name, or none to pick one"}
		}
	case "workspace.next", "workspace.prev":
		if len(req.Args) != 0 {
			return Response{Error: req.Method + " takes no arguments"}
		}
		if req.Method == "workspace.next" {
			return s.scroll(1)
		}
		return s.scroll(-1)
	case "queue.add":
		if len(req.Args) != 1 {
			return Response{Error: "queue.add takes one line of text"}
		}
		return s.queueAdd(req.Args[0])
	case "queue.list":
		if len(req.Args) != 0 {
			return Response{Error: "queue.list takes no arguments"}
		}
		if s.jrn == nil {
			return Response{Error: "no journal, so nothing is waiting"}
		}
		// The queue on its own, not the whole of what the journal remembers cut
		// down to it: this is the bar's question and it is asked every two
		// seconds (internal/journal, Waiting).
		return ok(s.jrn.Waiting())
	case "queue.done":
		if len(req.Args) != 1 {
			return Response{Error: "queue.done takes one id"}
		}
		return s.queueDone(req.Args[0])
	case "attn.mode":
		// One verb, read and write, for the reason window.jump-to has two
		// arities: it is the same question - what is the mode - asked and
		// answered, and a second name to learn buys nothing.
		switch len(req.Args) {
		case 0:
			return ok(Attn{Mode: string(s.mode())})
		case 1:
			return s.setMode(req.Args[0])
		default:
			return Response{Error: "attn.mode takes one mode name, or none to read it back"}
		}
	case "attn.quiet":
		if len(req.Args) != 0 {
			return Response{Error: "attn.quiet takes no arguments: it is a toggle"}
		}
		return s.toggleQuiet()
	case "attn.center":
		if len(req.Args) != 0 {
			return Response{Error: "attn.center takes no arguments"}
		}
		return s.center()
	case "attn.reach":
		if len(req.Args) != 0 {
			return Response{Error: "attn.reach takes no arguments: it puts the keyboard on the newest popup"}
		}
		return s.reach()
	case "attn.invoke":
		if len(req.Args) != 2 {
			return Response{Error: "attn.invoke takes a notification id and the key of the action to press"}
		}
		return s.invoke(req.Args[0], req.Args[1])
	case "desk.queue-jump":
		if len(req.Args) != 0 {
			return Response{Error: "desk.queue-jump takes no arguments"}
		}
		return s.queueJump()
	case "desk.regulars":
		if len(req.Args) != 0 {
			return Response{Error: "desk.regulars takes no arguments"}
		}
		return s.regulars()
	case "desk.next", "desk.prev":
		if len(req.Args) != 0 {
			return Response{Error: req.Method + " takes no arguments"}
		}
		if req.Method == "desk.next" {
			return s.rotate(desk.Next)
		}
		return s.rotate(desk.Prev)
	case "desk.apps":
		return s.deskApps(req.Args)
	case "desk.snapshot":
		return s.snapshot(req.Args)
	case "desk.reconcile":
		return s.reconcile()
	case "desk.last":
		if s.jrn == nil {
			return Response{Error: "no journal, so no desk to go back to"}
		}
		prev := s.jrn.State().LastDesk
		if prev == "" {
			return Response{Error: "no desk to go back to yet"}
		}
		return s.switchDesk(prev)
	default:
		if bluetoothMethods[req.Method] {
			// One line here and the rest in bluetooth.go: the radio has ten
			// verbs of its own and they have nothing to say to the desks.
			return s.bluetoothCall(req)
		}
		return Response{Error: fmt.Sprintf("unknown method %q", req.Method)}
	}
}

// Reconciled is what a reconcile did, so the caller can see whether anything
// was wrong rather than only that it ran.
type Reconciled struct {
	Renamed  []string `json:"renamed"`
	Adopted  []string `json:"adopted"`
	Conflict []string `json:"conflicts,omitempty"`
}

// reconcile makes the names true again: it corrects the ones a monitor move
// left lying, and claims the workspaces that are nobody's into the active desk
// (invariants 1 and 3). It is the same mechanism a snapshot uses, run by hand
// (vision.md, principle 8) - and it is what stops the naming model drifting
// away from what niri actually has.
func (s *Server) reconcile() Response {
	// The manifests are being read anyway, and this is the verb for "make what
	// is written down true again" - which includes what niri was told about
	// where things open.
	s.SyncRules()

	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	out := Reconciled{Renamed: []string{}, Adopted: []string{}}

	// Renames first: adoption picks free ordinals, and it should pick them
	// against corrected names rather than stale ones.
	for _, r := range m.Renames() {
		if err := s.niri.RenameWorkspace(r.From.String(), r.To.String()); err != nil {
			return Response{Error: "renaming " + r.From.String() + ": " + err.Error()}
		}
		if s.jrn != nil {
			s.jrn.Renamed(r)
		}
		out.Renamed = append(out.Renamed, r.From.String()+" -> "+r.To.String())
	}

	active := s.activeDesk(m)
	// With no active desk, adoption would have to guess which desk owns a new
	// workspace, and guessing puts windows somewhere the user never chose.
	if active != "" {
		firstApp, err := s.niri.FirstApps()
		if err != nil {
			return Response{Error: "reading windows: " + err.Error()}
		}
		for _, a := range desk.AdoptPlan(m, active, firstApp) {
			if err := s.niri.SetWorkspaceNameByID(a.ID, a.Name.String()); err != nil {
				return Response{Error: "adopting into " + active + ": " + err.Error()}
			}
			out.Adopted = append(out.Adopted, a.Name.String())
		}
	}
	// The one line in this answer whose subject is a name zde did not mint. A
	// rename and an adoption both say a name the model built out of a desk, a
	// connector and a slot (internal/desk, Name.String); this one says the name
	// niri had, and `zde desk reconcile` prints it to a terminal.
	//
	// Safe today by accident rather than by rule, which is why the filter is
	// here. Every conflict Rebuild can report is about a workspace whose name
	// parsed, so the name is letters, digits, dots and dashes and can hold
	// nothing (internal/desk, ParseName); the reasons are built out of parsed
	// names too, or out of an error that formats niri's own strings with %q,
	// which escapes what a terminal would act on. Both of those are decisions
	// made two packages away for other purposes. The obvious next conflict to
	// report is the one this code cannot report yet - a workspace whose name did
	// not parse at all, which is the case a person most wants named - and
	// whoever adds it should not also have to know that this line is printed
	// raw (internal/attn, Line).
	for _, c := range m.Conflicts() {
		out.Conflict = append(out.Conflict, attn.Line(c.Workspace.Name)+": "+attn.Line(c.Reason))
	}
	return ok(out)
}

// ensureDeclared makes the workspaces a manifest declares exist before the
// switch focuses them. Without it, switching to a desk that has never been
// launched is a refusal, which makes a manifest a description of the past
// rather than something you can ask for.
//
// niri keeps one empty workspace per strip, so each pass can create one per
// monitor: name it, let niri open the next, go round again. The loop is bounded
// by what is declared, so a compositor that stops producing empties ends it
// rather than spinning.
func (s *Server) ensureDeclared(target string) error {
	all, problems, err := s.desks.All()
	if err != nil {
		return fmt.Errorf("reading manifests: %w", err)
	}
	s.rememberProblems(problems)
	declared, ok := all[target]
	if !ok {
		return nil // no manifest: the desk is whatever is already named into it
	}
	want := declared.Workspaces()
	for range want {
		m, err := s.niri.DeskMap()
		if err != nil {
			return err
		}
		empty, err := s.niri.EmptyByOutput()
		if err != nil {
			return err
		}
		plan := desk.MissingPlan(m, want, empty)
		if len(plan) == 0 {
			return nil
		}
		// One per pass: naming this one is what makes niri open the next
		// empty workspace for the one after it.
		a := plan[0]
		if err := s.niri.SetWorkspaceNameByID(a.ID, a.Name.String()); err != nil {
			return fmt.Errorf("creating %s: %w", a.Name, err)
		}
	}
	return nil
}

// snapshot writes down a desk that exists, so it can be asked for again. It is
// the other end of adoption: arrange a desk by hand, let the names settle,
// then make it something a manifest declares.
// deskApps answers what a desk is made of before any of it is running.
//
// The manifests are files and a client could read them, but which directory
// they are in is the daemon's answer: zded is started with one (-desks) and a
// client working it out separately is a second rule about where desks live.
func (s *Server) deskApps(args []string) Response {
	target := ""
	switch len(args) {
	case 0:
		// The desk you are on, from the compositor rather than the journal, for
		// the reason snapshot has: this is about what is on the screen.
		focused, err := s.niri.FocusedName()
		if err != nil {
			return Response{Error: err.Error()}
		}
		n, err := desk.ParseName(focused)
		if err != nil {
			return Response{Error: fmt.Sprintf("%q is not a desk workspace, so there is no desk to list: name one", focused)}
		}
		target = n.Desk
	case 1:
		target = args[0]
	default:
		return Response{Error: "desk.apps takes one desk name, or none for the one you are on"}
	}
	all, problems, err := s.desks.All()
	if err != nil {
		return Response{Error: err.Error()}
	}
	s.rememberProblems(problems)
	d, found := all[target]
	if !found {
		// Naming what is declared: the usual way to land here is a desk that
		// exists on the screen and was never written down, and the answer to
		// that is a list of the ones that were.
		declared := make([]string, 0, len(all))
		for name := range all {
			declared = append(declared, name)
		}
		sort.Strings(declared)
		if len(declared) == 0 {
			return Response{Error: fmt.Sprintf("no desk %q is declared, and neither is any other", target)}
		}
		return Response{Error: fmt.Sprintf("no desk %q is declared; there is %s", target, strings.Join(declared, ", "))}
	}
	out := make([]DeskApp, 0, len(d.Apps))
	for _, app := range d.Apps {
		a := DeskApp{Address: zinc.Address(app.App, app.Instance)}
		if app.Monitor != "" && app.Workspace != "" {
			// check() proved this parses when the manifest was read.
			if n, err := desk.NewName(target, app.Monitor, app.Workspace); err == nil {
				a.Place = n.String()
			}
		}
		out = append(out, a)
	}
	return ok(out)
}

func (s *Server) snapshot(args []string) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	target := ""
	switch len(args) {
	case 0:
		// The desk you are on is the one you just arranged. Deliberately not
		// the journal's answer: snapshot writes a file, and writing down the
		// wrong desk is worse than asking which one.
		if focused, err := s.niri.FocusedName(); err == nil {
			if n, err := desk.ParseName(focused); err == nil {
				target = n.Desk
			}
		}
		if target == "" {
			return Response{Error: "no desk is focused, so there is none to write down: name one"}
		}
	case 1:
		target = args[0]
	default:
		return Response{Error: "desk.snapshot takes one desk name, or none for the one you are on"}
	}
	if target == desk.Regulars {
		// Standing on the regulars is one key away now, so this is a thing
		// people will do by accident. The manifest layer refuses it too, but
		// in words about a manifest nobody asked for.
		return Response{Error: "the regulars are not a desk, so there is no manifest to write: they are named into, never declared"}
	}

	// A desk that is already declared private stays private. The screen cannot
	// say so - the flag is a declaration and lives only in the manifest this
	// desk already has - and a snapshot that dropped it would write a second
	// manifest for that desk which says it is ordinary. That file is how a
	// private desk stops being one: the manifests are keyed on the name inside
	// them, so the two would collide, and until this fix whichever the
	// directory listed first decided whether anything arriving there could be
	// written to disk (history.go, privateArrival).
	private := false
	if prev := s.manifestFor(target); prev != nil {
		private = prev.Private
	}
	d, err := manifest.FromMap(m, target, private)
	if err != nil {
		return Response{Error: err.Error()}
	}
	path, err := s.desks.Save(d)
	if err != nil {
		return Response{Error: err.Error()}
	}
	return ok(path)
}

// nav is the vertical axis, and it is one key for two jobs because that is how
// the axis reads: down is down. Inside a stack it is the window below; at the
// end of one it is the next desk (docs/model.md, section 6).
//
// Which of the two it is stays niri's to decide, since the layout is niri's.
// zde asks for the window move and looks at whether anything moved, rather
// than working out from a layout dump whether a window is there. That costs
// two round trips and buys never having to model niri's stacking to be right
// about it - and it is right about the cases a model would get wrong: a
// floating window, an empty workspace, a single-window column.
//
// Two things it cannot see, both worth knowing before trusting it. A window
// that closes between the two reads can make a move look like an edge, and
// then the desk turns when all the user asked for was the window below. And a
// layer-shell surface holding keyboard focus reads as nothing focused at all,
// so a press spends itself putting focus back on a window instead of going
// anywhere. Neither is reachable from what zde ships today - the desk switcher
// is the surface that will reach the second one (docs/roadmap.md).
//
// The answer is the workspaces a desk rotation focused, or nothing at all when
// focus only moved inside the workspace: what changed about the desk, which is
// the part a bar has to know.
func (s *Server) nav(down bool) Response {
	before, err := s.niri.FocusedWindow()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if err := s.niri.FocusWindowVertically(down); err != nil {
		return Response{Error: err.Error()}
	}
	after, err := s.niri.FocusedWindow()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if after != before {
		return ok([]string{})
	}
	if down {
		return s.rotate(desk.Next)
	}
	return s.rotate(desk.Prev)
}

// moveWindow carries the focused window to the desk beside this one and goes
// with it. Shift on the nav axis means "bring the focused window along"
// (common/keymap/keymap.yaml, the grid), so this is the rotation with cargo
// rather than a verb of its own - which is also why focus follows: you are
// moving, and the window is coming.
//
// The window lands on the screen it was already on. A desk is not a monitor
// (docs/model.md, section 1), so changing desks should not move anyone's work
// to another display; where the target desk owns nothing on that screen, its
// own first workspace takes it, because a window with nowhere to land is worse
// than one that landed somewhere the desk actually owns.
//
// Nothing focused is not a refusal. There is no window to bring, so the key
// means what it does without one: go to the desk beside this one.
func (s *Server) moveWindow(step func(rotation []string, from string) string) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	rotation := m.Rotation()
	if len(rotation) == 0 {
		return Response{Error: "no desks to move a window between yet"}
	}
	from := s.activeDesk(m)
	return s.carryTo(step(rotation, from), from)
}

// moveWindowTo carries the focused window to a desk by name. Same journey as
// the rotation makes, with the destination said outright rather than counted
// to - which is how a window reaches a desk that is not beside this one, and
// the only way one gets into the regulars or back out of them.
func (s *Server) moveWindowTo(target string) Response {
	if !desk.ValidDesk(target) {
		return Response{Error: "not a desk name: " + target}
	}
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	return bandAdvice(target, s.carryTo(target, s.activeDesk(m)))
}

// moveWorkspaceTo hands the focused workspace, and everything in it, to another
// band. This is how the regulars come into being and how work comes back out of
// them, which were the same missing verb: a manifest cannot declare the band
// (the manifest layer refuses the reserved name) and adoption names into the
// desk you are standing on, so until now the only way to have regulars at all
// was to name a workspace by hand through niri, and nothing brought one back.
//
// It is a rename, because the name is the ownership record. Nothing else has to
// change hands, and you do not go anywhere: you were standing on that workspace
// and you still are - what changed is which band it belongs to, which is why
// the journal is told you are now on the target.
func (s *Server) moveWorkspaceTo(target string) Response {
	if !desk.ValidDesk(target) {
		return Response{Error: "not a desk name: " + target}
	}
	name, _, err := s.niri.FocusedPlace()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if name == "" {
		return Response{Error: "no workspace is focused, so there is none to move"}
	}
	from, err := desk.ParseName(name)
	if err != nil {
		// An unnamed workspace has no label to keep and no band to leave. niri
		// keeps an empty one at the end of every strip and adoption will not
		// claim it, so this is also what someone gets for trying to move the
		// scratch space - and the way out of both is the same.
		return Response{Error: "the focused workspace is " + strconv.Quote(name) +
			", which is not a zde name: put something in it so it is adopted, and move that"}
	}
	if from.Desk == target {
		return Response{Error: name + " is already in " + target}
	}
	// A slot its own manifest declares would come straight back: ensureDeclared
	// makes a declared workspace exist on the next switch or reconcile, so the
	// move would look undone by something the person cannot see. Refusing names
	// the file to edit instead.
	if d := s.manifestFor(from.Desk); d != nil && declaresSlot(d, from.Monitor, from.Slot) {
		return Response{Error: "desk " + from.Desk + " declares " + from.Slot + " on " + from.Monitor +
			": remove it from that manifest first, or it comes back on the next switch"}
	}
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	to, made := desk.MoveTo(m, from, target)
	if !made {
		return Response{Error: "no workspace name can be made for " + from.Slot + " in " + target}
	}
	if err := s.niri.RenameWorkspace(from.String(), to.String()); err != nil {
		return Response{Error: err.Error()}
	}
	if s.jrn != nil {
		s.jrn.Renamed(desk.Rename{From: from, To: to})
		// You are standing in the target band now, and desk.last should come
		// back to where you were. The desk you left keeps a last-active slot
		// naming a workspace it no longer owns, which needs no correcting: a
		// switch falls back to the first workspace of the band when the
		// remembered one is not there (desk.Landing).
		s.jrn.SetOnDesk(target)
		s.jrn.SetActive(to)
		if from.Desk != target {
			s.jrn.SetLastDesk(from.Desk)
		}
	}
	return ok([]string{to.String()})
}

// declaresSlot reports whether a manifest puts this slot on this monitor.
func declaresSlot(d *manifest.Desk, monitor, slot string) bool {
	for _, w := range d.Monitors[monitor].Workspaces {
		if w == slot {
			return true
		}
	}
	return false
}

// carryTo takes the focused window to a desk and follows it there.
func (s *Server) carryTo(to, from string) Response {
	if to == from {
		// Already there: nothing to carry it to, and nowhere to follow.
		return ok([]string{})
	}
	// Before the window moves, not after. ensureDeclared is what makes a
	// manifest's workspaces exist, so running it later would let the window
	// land by the map as it was and the switch arrive by the map as it became
	// - and its errors would strand a window that had already left, on a desk
	// nothing could then reach.
	if err := s.ensureDeclared(to); err != nil {
		return Response{Error: err.Error()}
	}
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	landed, err := s.carry(m, to)
	if err != nil {
		return Response{Error: err.Error()}
	}
	// from is the desk this started on, asked for before the window moved.
	// Afterwards the answer could be the destination, and then desk.last would
	// take the user back to where they already are.
	resp := s.switchFrom(to, from)
	if resp.Error != "" && landed != "" {
		// The window has already gone. Saying where turns a dead end into
		// something the user can act on.
		resp.Error = "the window is on " + landed + ", but " + resp.Error
	}
	return resp
}

// landingSlots is where a desk's monitors are entered: the workspace the
// journal remembers for each, and for a monitor it has never seen, the order
// the manifest was written in. A manifest lists workspaces in the order the
// person who wrote it wanted them, and the map cannot know that - it sorts
// names, because niri's strip order is niri's to say and does not reach us
// yet.
//
// One answer for the switch and for a window being carried, deliberately.
// Reading it in two places is how a window comes to land on a workspace the
// switch is not looking at, which is a window nobody can find (docs/model.md,
// invariant 2).
func (s *Server) landingSlots(target string) map[string]string {
	slots := map[string]string{}
	if s.jrn != nil {
		for monitor, slot := range s.jrn.State().LastActive[target] {
			slots[monitor] = slot
		}
	}
	if d := s.manifestFor(target); d != nil {
		for _, n := range d.Workspaces() {
			if _, remembered := slots[n.Monitor]; !remembered {
				slots[n.Monitor] = n.Slot
			}
		}
	}
	return slots
}

// carry moves the focused window onto the target desk and answers with where
// it put it. Empty, and no error, when there was no window to move.
func (s *Server) carry(m *desk.Map, target string) (string, error) {
	window, err := s.niri.FocusedWindow()
	if err != nil {
		return "", err
	}
	if window == 0 {
		return "", nil
	}
	monitor, err := s.niri.FocusedOutput()
	if err != nil {
		return "", err
	}
	slots := s.landingSlots(target)
	// The whole slot map, not the one entry for this screen: the slots are
	// keyed by the monitor in a workspace's name, and a workspace parked here
	// by an unplug carries a different monitor in its name than the screen it
	// is sitting on. Landing matches them up.
	landing, onThisScreen := desk.Landing(m, target, monitor, slots)
	if !onThisScreen {
		// The desk has nothing on this screen, so this is the one case where
		// a window does change monitors. It goes where a switch would enter
		// the desk, by the same slots, because landing anywhere else would put
		// it off the screen the switch is about to show.
		plan := desk.SwitchPlan(m, target, slots)
		if len(plan) == 0 {
			// The same refusal a switch would give, in the same words: both
			// mean the desk is not there, and one of them gets translated for
			// the band that cannot be declared.
			return "", errors.New(noSuchBand(target))
		}
		landing = plan[0]
	}
	if err := s.niri.MoveWindowToWorkspace(landing.String()); err != nil {
		return "", err
	}
	return landing.String(), nil
}

// queueAdd puts something on the queue, on the desk it was put there from.
//
// The desk is the point. A queue that only knew what was waiting would be a
// list; knowing where each thing waits is what lets a desk show its own and
// queue-jump go anywhere.
func (s *Server) queueAdd(text string) Response {
	if s.jrn == nil {
		return Response{Error: "no journal, so nothing can be made to wait"}
	}
	text = strings.TrimSpace(text)
	if err := checkQueueText(text); err != nil {
		return Response{Error: err.Error()}
	}
	// Where from, not where to. A compositor that cannot be read is not a
	// reason to refuse - the thing still waits, it just waits nowhere in
	// particular, and nothing can give it a desk afterwards, so it is worth
	// asking twice. The map takes two questions of niri and the focused name
	// takes one, and the second alone answers this whenever the workspace has
	// a name.
	deskName := s.whereWeAre()
	it, err := s.jrn.Queue(journal.Item{Text: text, Desk: deskName})
	if err != nil {
		return Response{Error: err.Error()}
	}
	return ok(it)
}

// Arrived is attn.Sink: a notification becomes a queue item on the desk it
// arrived on, which is what makes it something you can come back to rather
// than something you caught or missed.
//
// The mode decides only whether it reaches the queue (internal/attn, Mode).
// Everything that arrives is recorded either way, which is principle 3 - display
// policy, never data policy - and it is what makes quiet mode something a person
// can afford to leave on: the things that happened while it was on are in the
// notification center, in the order they happened, with what they said.
//
// The id it answers with is the journal's, narrowed to what the notification
// spec has room for. Nothing else in zde uses the narrow one, and the numbers
// would have to pass four billion notifications in one journal's life to
// disagree. A notification the mode kept out of the queue is given one too, from
// the same counter, because the app that sent it still addresses it by that
// number on the bus.
func (s *Server) Arrived(n attn.Notification) (uint64, error) {
	if s.jrn == nil {
		return 0, errors.New("no journal, so nothing can be kept")
	}
	on := s.whereWeAre()
	rec := attn.Record{
		From: n.From,
		// Carried, never worked out from the name above. What arrived on the bus
		// cannot set this - there is no argument to Notify it could come from -
		// and what zde sent itself always does (internal/attn, Local). It is the
		// whole of what tells a person which of the two is in front of them, so
		// re-deriving it here from a string would put a lookalike back in the
		// desktop's chair (internal/attn, Notification.Self).
		Self:    n.Self,
		Text:    n.Text,
		Body:    n.Body,
		Urgent:  n.Urgent,
		Actions: n.Actions,
		Extra:   n.Extra,
		At:      time.Now(),
		Desk:    on,
		// Decided here, while the desk it arrived on is known. Asking the
		// manifests again when the snapshot is written would be asking about a
		// file that can have changed since - and a desk that stopped being
		// private in the meantime would take the bodies that arrived while it
		// was private to disk with it (history.go, privateArrival).
		//
		// The other direction is what that costs, and it is chosen rather than
		// overlooked: declaring a desk private now does not retract what is
		// already in the file. The flag records what was true when the record
		// arrived, and marking a desk private is a statement about what happens
		// from here. A retraction would also be a promise this cannot keep: it
		// cannot reach a snapshot a backup has already copied, so it would clean
		// one file and read as a promise about the disk. The summary and the
		// sender of anything the mode queued are still in the journal either
		// way, which a retraction would not touch and is not trying to - what
		// stays out of that file is the body, and it stays out for every desk
		// rather than for the private ones (internal/journal, Item).
		// What removes what is already there is removing it: stop zded, delete
		// ~/.local/state/zde/history.json, start it again. In that order,
		// because the records are still in the daemon's memory until it goes,
		// and the next write would put them back (docs/verify.md, section 5).
		//
		// Once, and not once per reader: the popup path used to ask again for
		// itself, which was a second read and parse of every manifest on the
		// machine for every notification - on the far end of a Notify the
		// sending app is blocked on - and two reads can disagree if a manifest
		// is saved between them (attn.go, maybePop).
		Private: s.privateArrival(on),
	}
	// One reading of the mode for both decisions. Asked twice it could answer
	// twice - `zde attn quiet` lands between them - and a notification that was
	// queued but not shown, or shown but not queued, would be a session in two
	// modes at once for one arrival.
	mode := s.mode()
	if mode.Queues(n.Urgent) {
		it, err := s.jrn.Queue(journal.Item{
			// No body. The record above keeps the whole message in memory for
			// the center to show; the journal is a file, and a file is not
			// where somebody's mail goes (internal/journal, Item).
			Text:   rec.Text,
			Desk:   rec.Desk,
			From:   rec.From,
			Urgent: rec.Urgent,
			// And the badge with the name, or `zde queue` would be the one
			// surface left where the desktop's own row and an app drawing itself
			// like the desktop read the same (internal/journal, Item.Self).
			Self: rec.Self,
		})
		switch {
		case err == nil:
			rec.ID, rec.Queued = it.ID, true
		case errors.Is(err, journal.ErrQueueFull):
			// Not a failure of the arrival. The queue has a ceiling now
			// (internal/journal, queueMax) and this session is at it, which is
			// a statement about how much is already waiting and not about this
			// notification: it is recorded, it draws its popup, and it takes an
			// id below exactly as one a mode kept off the queue does. What it
			// does not get is a place in a list of a thousand things nobody is
			// going to read.
			s.saidQueueFull.Do(func() {
				log.Printf("zded: %v. What arrives from now on is shown and recorded, "+
					"and not added to it", err)
			})
		default:
			return 0, err
		}
	}
	// The id, for everything the mode did not queue and for what the queue had
	// no room for. An arrival with no id is one the sending app cannot close
	// and the notification center cannot dismiss, so this is not optional
	// (internal/journal, ClaimID).
	if !rec.Queued {
		id, err := s.jrn.ClaimID()
		if err != nil {
			return 0, err
		}
		rec.ID = id
	}
	// A replacement lands on top of the record it supersedes rather than beside
	// it. replaces_id is a sender saying this is the same notification with
	// something new to say, and appending was what turned one download into a
	// hundred rows of history (internal/attn, Replace).
	var gone []uint64
	if n.Replaces != 0 {
		gone = s.history.Replace(n.Replaces, rec)
	} else {
		gone = s.history.Add(rec)
	}
	// What the history pushed out is what nothing can reach any more: it cannot
	// be listed, dismissed or invoked, so the bus side is told to stop holding
	// the sender's names for it. Without this, the one table in the system with
	// no bound would grow by one for every notification the session ever
	// received - and the modes made that worse, because a notification a mode
	// keeps off the queue is one nobody can finish, so nothing else prunes it.
	//
	// Every one of them, not the first. One arrival can push a record out of
	// its own sender's ring and, when the name is a new one, take a whole other
	// sender's ring with it (internal/attn, SendersMax) - so a loop that
	// stopped at one id would leave the rest of that ring remembered here for
	// the life of the session, which is the leak this call exists to stop.
	if len(gone) > 0 {
		// Read once and outside the loop: watcher takes the daemon's lock, and
		// every method on what it answers puts a message on the session bus.
		if w := s.watcher(); w != nil {
			for _, id := range gone {
				w.Forget(id)
			}
		}
	}
	// And in front of the person, last: after the record is kept and the queue
	// has it, and never instead of either. What decides whether anything is
	// drawn is the session's mode and the desk's own privacy, both of them
	// display and neither of them touching what is above (attn.go, maybePop).
	s.maybePop(rec, mode)
	return rec.ID, nil
}

// Closed is attn.Sink: an app taking its own notification back. attn has
// already checked the item was that sender's to close.
func (s *Server) Closed(id uint64) error {
	// The history keeps it and marks it, rather than dropping it. An app that
	// takes a notification back has told the person nothing, and "the download
	// finished and then the row vanished" is the kind of thing that makes
	// somebody distrust the whole list.
	s.history.Dismiss(id)
	if s.jrn == nil {
		return nil
	}
	return s.jrn.Done(id)
}

// Notifier is told when something leaves the queue by a route the sender did
// not ask for, so it can say so on the bus. A client blocked on a
// notification's closure has no other way to learn it is gone.
//
// Invoke is the other direction: the person pressing one of a notification's
// actions, and the app hearing about it. Which action is a key the record
// declared, checked before it gets here. It answers an error because the ways
// it can fail are ones the person has to be told about - an app that has since
// exited hears nothing at all.
type Notifier interface {
	Dismissed(id uint64)
	Invoke(id uint64, key string) error
	// Forget says a notification has fallen out of the history, so nothing can
	// address it any more and nothing should be remembering who sent it.
	Forget(id uint64)
}

// Watching sets who to tell.
//
// Under the lock, because there is no moment when nothing else is looking. It
// was written without one on the reasoning that this happens at startup before
// anything serves, and neither half of that was true: attn.Serve exports the
// interface and takes org.freedesktop.Notifications before it returns, so from
// that moment any app on the session bus can call Notify, which arrives at
// Arrived and reads this field. The daemon answers its own socket by then too
// (cmd/zded). An interface value is two words and a torn read of one is not a
// nil check that fails, it is a call into an address that was never a method
// table.
func (s *Server) Watching(n Notifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifier = n
}

// watcher is who to tell, or nil when nothing took the bus name.
//
// Read out from under the lock and then used, never called with the lock held:
// every method on it puts a message on the session bus, and holding s.mu across
// that would queue every event broadcast behind whatever the bus is doing.
func (s *Server) watcher() Notifier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notifier
}

// whereWeAre is the desk to file something arriving against, and never an
// error: a notification with no desk still waits, and one refused because niri
// was unreadable is gone for good.
//
// This is a read that writes: it goes through deskOf, which records the desk
// when the journal is behind the compositor. Anything calling this from a
// goroutine of its own should read the note there first - it says why that is
// safe, and what it costs.
func (s *Server) whereWeAre() string {
	if m, err := s.niri.DeskMap(); err == nil {
		return s.activeDesk(m)
	}
	if focused, err := s.niri.FocusedName(); err == nil {
		if n, perr := desk.ParseName(focused); perr == nil {
			return n.Desk
		}
	}
	return ""
}

// checkQueueText keeps the queue printable. Every reader of it is line-based -
// the CLI, and the bar after it - so a newline would turn one item into two,
// and the second would have no id.
func checkQueueText(text string) error {
	// Empty and drawing-nothing are one refusal, because they are one thing to
	// whoever comes back to the queue: a row with nothing in the column they
	// read. A bare combining mark is printable by every test Go has and draws
	// no character of its own, and that is what this catches that the loop
	// below does not.
	//
	// The floor the notification door stands on as well, asked with the same
	// function so that the two cannot drift (internal/attn, Draws). It is only
	// the floor. Above it this door is the stricter of the two on purpose: it
	// refuses a tab where oneLine folds one, because a person typing a reminder
	// can be told to try again and an app's notification is the only copy there
	// will ever be of something that already happened (internal/attn, oneLine).
	if !attn.Draws(text) {
		return errors.New("nothing to wait for: say what it is")
	}
	// Runes, not bytes: a reminder written in Cyrillic is not half a reminder.
	if n := utf8.RuneCountInString(text); n > queueTextMax {
		return fmt.Errorf("that is %d characters, and a queue is a list of reminders, not of essays", n)
	}
	for _, r := range text {
		// Printable covers it: a tab is the CLI's own column separator, so one
		// inside the text would print an item with more columns than it has
		// fields, and a line separator would split it in two.
		if !unicode.IsPrint(r) {
			return errors.New("one printable line: everything that reads the queue reads it a line and a column at a time")
		}
	}
	return nil
}

const queueTextMax = 300

func (s *Server) queueDone(id string) Response {
	if s.jrn == nil {
		return Response{Error: "no journal, so nothing is waiting"}
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return Response{Error: "queue.done wants the id from the list, not " + strconv.Quote(id)}
	}
	if err := s.jrn.Done(n); err != nil {
		return Response{Error: err.Error()}
	}
	// The same id addresses the history, whether or not this ever reached the
	// queue: the center dismisses a notification a mode kept out with exactly
	// this call, and finishing a queue item should stop the center showing it
	// as still waiting.
	s.history.Dismiss(n)
	if w := s.watcher(); w != nil {
		// Whoever sent it may be waiting to hear that it is gone.
		w.Dismissed(n)
	}
	return ok([]string{})
}

// queueJump goes to where the oldest thing waiting is waiting.
//
// Oldest, because a queue is a queue: the thing that has been waiting longest
// is the one being kept waiting. Jumping does not finish it - arriving
// somewhere is not doing the thing - so the item stays until it is done.
func (s *Server) queueJump() Response {
	if s.jrn == nil {
		return Response{Error: "no journal, so nothing is waiting"}
	}
	q := s.jrn.Waiting()
	if len(q) == 0 {
		return Response{Error: "nothing is waiting"}
	}
	// A desk the journal remembers may not be there any more: the queue
	// outlives the compositor, and a fresh session starts with nothing named.
	// Stepping over those matters more than it looks - the alternative is one
	// stale reminder holding the key down for every reminder behind it.
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	for _, it := range q {
		if it.Desk != "" && len(m.Workspaces(it.Desk)) > 0 {
			return s.switchDesk(it.Desk)
		}
	}
	return Response{Error: "nothing waiting is on a desk that exists: " + strconv.Quote(q[0].Text) + " is first"}
}

// regulars brings up the band that belongs to no desk: comms, music, the
// personal browser - the windows you want from wherever you are, rather than
// on the desk you happen to be on (docs/model.md, section 3).
//
// It is a switch like any other, which is the whole point of the regulars
// being a desk key rather than a special case in the map. What makes them
// regulars is the two things around it: rotation steps over them, so they are
// never scrolled into by accident, and desk.last brings you back, so reaching
// for music does not cost you your place.
//
// They come into being by being named into, and only that way: a manifest
// named regulars is refused where manifests are read, because the regulars are
// not a desk. Saying that when there are none beats a refusal that reads like
// a desk went missing - and beats the manifest advice this used to give, which
// the manifest layer would have rejected.
func (s *Server) regulars() Response {
	return bandAdvice(desk.Regulars, s.switchDesk(desk.Regulars))
}

// bandAdvice replaces the refusal for a desk that is not there with the one
// that says how to have it, and only for the regulars: the generic answer
// offers a manifest, and the regulars are the one band a manifest cannot
// declare, so it is advice that cannot be taken.
//
// Only that refusal. A manifest that will not parse, a compositor that cannot
// be read, a focus that failed partway: those keep their own words, because
// this advice would send the people who hit them looking in the wrong place.
func bandAdvice(target string, resp Response) Response {
	if target != desk.Regulars || resp.Error != noSuchBand(target) {
		return resp
	}
	return Response{Error: "no regulars yet: stand on a workspace you want to keep and run `zde desk move-workspace-to " + desk.Regulars + "`"}
}

// noSuchBand is the refusal for a desk that has no workspaces and nothing
// declaring any. One place, so that reading it back to decide what a failure
// meant cannot drift from writing it.
func noSuchBand(target string) string {
	return "desk " + target + " has no workspaces and no manifest that declares any"
}

// scroll moves one workspace along the band of the desk you are on, on the
// screen you are looking at, and stops at its ends.
//
// This is invariant 4 made of code: the band is the whole range a scroll can
// reach, so leaving a desk is always a desk-level action - next, prev, a name,
// the regulars - and never something you arrive at by holding a key down.
//
// niri's own workspace scrolling would cross into another desk's workspaces,
// which is why zde works out the destination and focuses it by name rather
// than asking niri to move one along.
//
// The answer is where it went, or nothing when the band ended there. A caller
// that cannot tell those apart would show a workspace change that never
// happened.
func (s *Server) scroll(by int) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	// One reply for both, and the desk read off the same one. Asked
	// separately, the name could be from before a drag to another monitor and
	// the output from after it, and the pair would describe nowhere - which
	// lands outside every band, and sends the step to an end of one.
	focused, monitor, err := s.niri.FocusedPlace()
	if err != nil {
		return Response{Error: err.Error()}
	}
	from, _ := desk.ParseName(focused)
	on := s.deskOf(m, focused)
	if on == "" {
		return Response{Error: "no desk to scroll inside: nothing here belongs to one yet"}
	}
	band := m.Band(on, monitor)
	if len(band) == 0 {
		// Not the same as the end of a band, which is silent because nothing
		// is wrong with it. There is nothing here to walk at all.
		return Response{Error: "desk " + on + " has no workspaces on this screen"}
	}
	to, moved := desk.BandStep(band, from, by)
	if !moved {
		return ok([]string{})
	}
	if err := s.niri.FocusWorkspace(to.String()); err != nil {
		return Response{Error: err.Error()}
	}
	if s.jrn != nil {
		// Where the desk was left, so coming back lands here (invariant 5).
		s.jrn.SetActive(to)
	}
	return ok([]string{to.String()})
}

// rotate switches to the desk beside the one you are on. Which way is the
// caller's to say; everything else the two directions do is the same, down to
// the rotation being a loop with no ends to fall off.
//
// Where you are is the same question adoption asks, and gets the same answer:
// on a workspace nothing has named yet, the journal knows which desk you took
// it out of. From outside the rotation entirely - the regulars, or a session
// with nothing named - the step lands on the first desk or the last one, so
// the key still goes somewhere it can explain.
func (s *Server) rotate(step func(rotation []string, from string) string) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	rotation := m.Rotation()
	if len(rotation) == 0 {
		return Response{Error: "no desks to rotate through yet"}
	}
	from := s.activeDesk(m)
	to := step(rotation, from)
	if to == from {
		// A rotation of one: there is no desk beside this one. Switching to it
		// anyway is not the no-op it looks like - a switch restores the desk's
		// last-active workspace, so it would scroll off the workspace you are
		// on and then remember the wrong one as where you were.
		return ok([]string{})
	}
	return s.switchDesk(to)
}

// activeDesk is the desk whose band new workspaces belong to.
//
// The focused workspace answers it when it has a name, and that answer is
// written down: the fallback below is only ever as good as how recently it was
// true, so every observation refreshes it.
//
// It often has no name. Open a window past the end of the strip and niri makes
// a fresh workspace, focus follows it there, and that workspace is unnamed
// precisely because nothing has claimed it yet. Requiring a name to decide who
// claims it would mean the one case adoption exists for is the one it cannot
// handle, so the journal answers instead - but only under two conditions.
//
// The name must be absent rather than merely unreadable. A workspace somebody
// else named is not unclaimed, it is theirs, and renaming it into a desk is
// not adoption.
//
// And the desk must still be there. A journal outlives the compositor it was
// written under: log out on vshop, log back in, and niri starts with nothing
// named. Spending the remembered desk then would file the first window of a
// fresh session into a desk that does not exist, which is a stronger claim
// than adoption has any business making.
func (s *Server) activeDesk(m *desk.Map) string {
	focused, err := s.niri.FocusedName()
	if err != nil {
		// Unreadable is not the same as unnamed, but it leads to the same
		// place: the journal is the only other answer either way.
		focused = ""
	}
	return s.deskOf(m, focused)
}

// deskOf is activeDesk with the focused workspace already read. A caller that
// needs the name for something else too asks once and passes it here, rather
// than asking again and getting an answer from a different moment.
//
// It writes, and a reader can be on any goroutine. Where you are is what
// adoption spends on a workspace that has no name yet, and the compositor is
// the authority on it - so a read that finds the journal disagreeing corrects
// it, which is also how a jump records the desk it landed on (see focusWindow).
// That means a notification arriving on the bus - Arrived, whereWeAre, here -
// writes a line to the journal, on whatever goroutine the bus handed it to. It
// is safe, and this is the whole of why:
//
//   - The journal is one mutex over the file and the state (internal/journal),
//     so two writers cannot tear an entry or lose a queue id.
//   - The value is not this caller's opinion. It is the desk the focused
//     workspace names, read from niri a moment earlier, so two goroutines that
//     race here write the same answer rather than fighting over two.
//   - It converges. The read and the write are not one atomic step, so a switch
//     that lands between them can be followed by a write of the desk that was
//     focused just before it - and the watcher reconciles after every burst of
//     compositor events (watch.go), which reads the compositor again and puts
//     the truth back. The window is one niri round trip wide and it costs a
//     stale OnDesk until the next event, never a wrong desk on the screen.
//
// The cost that is worth knowing about is the other one: this path asks niri
// before it writes anything, so posting a notification from a goroutine that
// cannot afford to block is posting it behind a compositor round trip.
func (s *Server) deskOf(m *desk.Map, focused string) string {
	if focused != "" {
		n, err := desk.ParseName(focused)
		if err != nil {
			return "" // named, but not by us
		}
		if s.jrn != nil && s.jrn.State().OnDesk != n.Desk {
			s.jrn.SetOnDesk(n.Desk)
		}
		return n.Desk
	}
	if s.jrn == nil {
		return ""
	}
	on := s.jrn.State().OnDesk
	if on == "" || len(m.Workspaces(on)) == 0 {
		return ""
	}
	return on
}

// manifestFor is the desk's manifest, or nil if it has none. A desk without
// one is not an error: it is whatever has been named into it.
func (s *Server) manifestFor(target string) *manifest.Desk {
	// A manifest that will not parse is not a reason to treat every desk as
	// undeclared, which is what returning nothing here used to mean: the error
	// was dropped, the map with it, and adoption quietly stopped placing
	// workspaces anywhere.
	all, problems, err := s.desks.All()
	if err != nil {
		return nil
	}
	s.rememberProblems(problems)
	return all[target]
}

// switchDesk brings a desk up on every monitor it owns workspaces on, and
// records where it left from so desk.last can come back.
func (s *Server) switchDesk(target string) Response { return s.switchFrom(target, "") }

// switchFrom is a switch that already knows the desk it is leaving. Empty
// means work it out, which is what every caller wants except move-window: that
// one has to ask before it moves the window, because a moved window can be
// what the answer is read off afterwards.
func (s *Server) switchFrom(target, from string) Response {
	// The desk zde thinks you are on, read before anything moves. Not `from`
	// below and not the compositor: ensureDeclared names this desk's workspaces
	// into existence, one of them may be the focused one, and the watcher
	// records that as arriving on the desk - so by the time the switch is done,
	// every other answer to "where were you" is the desk you were going to.
	// That made the first entry to a declared desk - a desk that until now
	// existed only as a manifest - the one switch that started nothing.
	was := ""
	if s.jrn != nil {
		was = s.jrn.State().OnDesk
	}
	if err := s.ensureDeclared(target); err != nil {
		return Response{Error: err.Error()}
	}
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	plan := desk.SwitchPlan(m, target, s.landingSlots(target))
	if len(plan) == 0 {
		// Nothing to focus is not the same as a failed switch, but it is not
		// a switch either: say so rather than pretending the desk is up.
		return Response{Error: noSuchBand(target)}
	}

	// Where we are now, before anything moves, so desk.last has somewhere to
	// go back to. A failure to read it is not worth refusing the switch over.
	if from == "" {
		from = s.activeDesk(m)
	}

	for _, n := range plan {
		if err := s.niri.FocusWorkspace(n.String()); err != nil {
			// Partway through: some monitors have moved. Report it rather
			// than carrying on, because the state is now worth looking at.
			return Response{Error: "switching to " + target + ": " + err.Error()}
		}
	}
	if s.jrn != nil {
		// Only now, with every monitor moved. A switch that failed partway is
		// not a desk you are on, and this is the answer adoption spends on
		// workspaces that have no name of their own yet.
		s.jrn.SetOnDesk(target)
		for _, n := range plan {
			s.jrn.SetActive(n)
		}
		if from != "" && from != target {
			s.jrn.SetLastDesk(from)
		}
	}
	if was != target {
		// A desk is what its manifest declares, so entering one brings it up
		// and puts the session in the mode it asks for (docs/model.md, section
		// 5). Only on a change of desk: re-entering the desk you are standing
		// on is what half the nav keys do, and each pass would be a launch
		// attempt per declared app and a mode set by hand overwritten.
		s.enterDesk(target)
		s.startApps(target)
	}
	// Names as strings, not as their parts. A workspace name is one thing
	// everywhere else in zde - in niri, on the bar, in the journal - and the
	// socket is the wire a shell adapter will read, so it says the same thing.
	focused := make([]string, 0, len(plan))
	for _, n := range plan {
		focused = append(focused, n.String())
	}
	return ok(focused)
}

// startApps runs what the desk declares, in the background.
//
// Behind the answer, not in front of it: a launch is podman work measured in
// seconds, and the switch it belongs to is a keypress. The person is already
// looking at the desk while these arrive.
//
// Counted and cancellable, through claimLaunch and s.runs, which is the same
// machinery a tier goes through (startRun) and for the same reason: what this
// starts is a subprocess in a process group of its own, so nothing the session
// does on the way out reaches it except this daemon deciding to. It used to be a
// bare `go func` - not in s.runs, not under s.runCtx, waited for by nobody -
// and measured, Close returned in 0s with a launch still in flight and its
// subprocess outlived the daemon.
//
// Where a pinned window lands is niri's, from the rules written just before
// (rules.go). An app the manifest does not pin, or one whose window cannot be
// recognised before it exists, still lands where niri opens windows.
func (s *Server) startApps(target string) {
	all, _, err := s.desks.All()
	if err != nil {
		return // ensureDeclared already reported this one
	}
	d, ok := all[target]
	if !ok || len(d.Apps) == 0 {
		return
	}
	apps := d.Apps
	if !s.claimLaunch(target) {
		return
	}
	go func() {
		defer s.runs.Done()
		defer s.releaseLaunch(target)
		s.launchApps(s.runCtx, target, apps)
	}()
}

// claimLaunch takes the desk's one place to be launching in, counts it as a run
// in flight, or says no.
//
// Two refusals in one, and each answers a different question:
//
//   - The daemon is stopping. Same as startRun: refused once runCtx is
//     cancelled, and claimed under the same mu, which together are what keep the
//     count from being raised while stopRuns is waiting on it.
//   - This desk is already coming up. A second entry has nothing to do - the run
//     in flight is walking that same list of apps, and zinc refuses a second
//     launch of an app that is already up - so it is the same "nothing changed"
//     the caller's own `was != target` check is making one level up.
//
// And that per-desk claim is the whole of the bound on launches, deliberately.
// The unbounded thing was never the apps: launchApps walks a desk's apps one at
// a time on one goroutine, so a desk declaring twenty of them is twenty launches
// in a row rather than twenty at once, and a desk may legitimately declare
// twenty. What was unbounded was desk entries - one goroutine per entry, so a
// key repeating was as many as it repeated. Measured, 200 entries put 200
// launches in flight at once, where asksMax caps a tier at four.
//
// With one run per desk, what can be in flight is one per desk this machine
// declares: a number set by the files somebody wrote, like the number of apps
// in one of them, and 2 rather than 200 for the measured case. A constant beside
// it would have to be a queue - a launch past the cap cannot be refused, because
// a refused launch is a window that never appears and nothing said about it -
// and a queue is where one wedged podman becomes every later desk not starting
// anything. Nothing here can tell a stuck image pull from a slow one, so the
// ceiling is left where a person set it rather than where zde guessed.
func (s *Server) claimLaunch(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCtx.Err() != nil {
		return false
	}
	if _, up := s.launching[target]; up {
		return false
	}
	if s.launching == nil {
		s.launching = make(map[string]struct{})
	}
	s.launching[target] = struct{}{}
	s.runs.Add(1)
	return true
}

// releaseLaunch gives the desk's place back. Deleted rather than left false, so
// the map holds the desks coming up and not every desk ever entered.
func (s *Server) releaseLaunch(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.launching, target)
}

// comingUp is how many desks are launching their apps right now. For the tests,
// for the same reason asking is there for them: a bound nobody can count is a
// bound nobody can check.
func (s *Server) comingUp() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.launching)
}

// launchApps starts one desk's apps, one after another, and says once what did
// not start.
func (s *Server) launchApps(ctx context.Context, target string, apps []manifest.App) {
	var failed []launchFailure
	for _, app := range apps {
		if ctx.Err() != nil {
			// The daemon is stopping, so the rest of this desk is not going to
			// start and nobody is there to be told: the apps left would each
			// fail instantly against a cancelled context, and a logout is not
			// the moment to post "4 apps did not start" about a desk nobody is
			// looking at any more.
			return
		}
		address := zinc.Address(app.App, app.Instance)
		err := s.launch(ctx, address)
		if err == nil {
			continue
		}
		if errors.Is(err, zinc.ErrAlreadyRunning) {
			// The ordinary case, and the reason this is a check rather than
			// a line in the log: zinc refuses a second launch of an app that
			// is up, so every switch back to a desk you were on this session
			// refuses once per app it declares. Counting that as a failure
			// made a healthy machine say "3 apps did not start" for pressing
			// a key twice, and the notification people learn to ignore is the
			// one that cries wolf. Nothing to say about it either: the desk
			// declares the app and the app is running, which is the state the
			// switch was asking for.
			continue
		}
		if ctx.Err() != nil {
			// The launch this daemon just killed on its way out. It failed
			// because it was stopped, which is not a failure to report.
			return
		}
		// Every real one in the daemon's log, whole: it is one app on one
		// desk, and taking the switch down over it would make an unbuildable
		// image cost somebody their whole desk.
		log.Printf("zded: starting %s: %v", address, err)
		failed = append(failed, launchFailure{Address: address, Err: err})
	}
	// And once, in front of the person, because a log is not somewhere
	// anybody looks while they are working (launch.go).
	s.launchesFailed(target, failed)
}

// SyncRules puts the desks' placement into niri's dynamic config (rules.go).
//
// At startup and on reconcile, which is when the manifests are the question -
// not on every switch. The rules are a function of the files, so a switch that
// rewrote them would be asking niri to reload its config for something that
// had not changed, and a reload re-evaluates the rules for every window
// already open. A manifest edited mid-session takes effect at the next
// `zde desk reconcile`, which is the verb for making things true again.
func (s *Server) SyncRules() {
	all, _, err := s.desks.All()
	if err != nil {
		return
	}
	if err := writeRules(dynamicPath(), placementRules(all)); err != nil {
		log.Printf("zded: writing niri's placement rules: %v", err)
	}
}

func (s *Server) status() Status {
	st := Status{Version: s.version, Compositor: "connected"}
	m, err := s.niri.DeskMap()
	if err != nil {
		// Not an error to the caller: a zded that cannot reach niri is
		// exactly the situation someone is running `zde status` to find out
		// about, so it reports rather than refuses.
		//
		// Cleaned here, where niri's words enter this daemon, because from here
		// they are a row: `zde status` prints them after "compositor" and the
		// report doctor makes prints them after "fail compositor" (cmd/zde,
		// status; internal/doctor). A row is where a newline is a second row
		// with nothing in column one.
		st.Compositor = attn.Line(err.Error())
	} else {
		st.Desks = len(m.DeskNames())
	}
	if s.jrn != nil {
		js := s.jrn.State()
		st.OnDesk = js.OnDesk
		st.LastDesk = js.LastDesk
		st.Skipped = s.jrn.Skipped()
		st.Queued = len(js.Queue)
	}
	st.Listeners = s.listeners()
	st.Shell = st.Listeners > 0
	// Under one lock with the drop count, because the two are one sentence:
	// a number at the cap with a count beside it is a flood happening now, and
	// a number a session's size with a count beside it is one that has ended.
	s.mu.Lock()
	st.Connections = len(s.conns)
	st.Dropped = s.dropped
	s.mu.Unlock()
	st.Notifications = s.watcher() != nil
	st.Mode = string(s.mode())
	// Looked up per call rather than remembered from startup. PATH points at
	// profile directories whose contents change under a running daemon, and
	// zded outlives the switch that installs zinc - so asking every time is
	// the difference between an answer about this machine and one about this
	// machine at the last login.
	_, err = exec.LookPath(zinc.Runner)
	st.Zinc = err == nil
	s.mu.Lock()
	st.BadManifests = append([]string(nil), s.problems...)
	st.Unplaced = int(s.unplaced)
	s.mu.Unlock()
	return st
}

// rememberProblems keeps what the last manifest read could not use, so that
// `zde status` can say so. Replaced rather than accumulated: the list is the
// state of the directory now, and a manifest somebody has since fixed should
// stop being mentioned.
//
// One row each, cleaned here where the parser's words enter the daemon, because
// a row is what every reader of this list draws: a line under "manifest" in
// `zde status`, a failing check in doctor's report, and whatever the shell puts
// it on next. What the parser says about a hand-edited file is several lines
// with the file's own text quoted in them (`field <key> not found`), so this is
// the one place in zde where a newline and an ESC arrive out of a YAML file.
func (s *Server) rememberProblems(problems []manifest.Problem) {
	list := make([]string, 0, len(problems))
	for _, p := range problems {
		list = append(list, attn.Line(p.String()))
	}
	s.mu.Lock()
	s.problems = list
	s.mu.Unlock()
}

func ok(v any) Response {
	raw, err := json.Marshal(v)
	if err != nil {
		return Response{Error: err.Error()}
	}
	return Response{Ok: raw}
}

// responseLine is one answer as it goes on the wire, newline and all. The
// fallback matters more than it looks: a reply that cannot be encoded is a bug
// in zded, and a caller left waiting for a line that was never written would
// meet it as a daemon that hangs rather than one that says something.
//
// A line rather than a write, because there is exactly one place that writes to
// a connection now and it takes bytes and a deadline (events.go, writeWithin).
// A second way to write here would be a second way to write with no deadline on
// it, which is the bug this branch exists to end.
func responseLine(resp Response) []byte {
	line, err := json.Marshal(resp)
	if err != nil {
		line = []byte(`{"error":"zded could not encode its own reply"}`)
	}
	return append(line, '\n')
}
