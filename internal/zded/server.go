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
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/link"
	"github.com/crispuscrew/zde/internal/manifest"
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
	// asks for without a container runtime under it.
	launch func(address string) error
	// spawn runs what a bind would have run, for the palette. A field for the
	// same reason launch is one: what the palette starts is the thing worth
	// asserting, and a test should not have to start a terminal to see it.
	spawn func(argv []string) error

	notifier Notifier
	// history is what has arrived, whatever the mode did about it. Bounded, and
	// in memory rather than in the journal: see attn.HistoryMax.
	history attn.History

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

	mu       sync.Mutex
	ln       net.Listener
	problems []string
	subs     map[*sink]struct{}
	waiting  map[string]chan struct{}
	tokens   uint64
}

func New(version string, jrn *journal.Journal, compositor Compositor, desks Desks) *Server {
	if desks == nil {
		desks = noDesks{}
	}
	return &Server{
		version:  version,
		jrn:      jrn,
		niri:     compositor,
		desks:    desks,
		launch:   zinc.Run,
		spawn:    spawnDetached,
		openLink: link.Open,
		// Neither radio is dialled here: opening a system bus connection at
		// startup would be zded doing that work on every machine, including the
		// ones that have no radio and never asked for one (bluetooth.go, radio;
		// net.go, links).
		openBluetooth: func() (Bluetooth, error) { return bt.Dial() },
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

// Close stops listening, and gives up the radio with it.
//
// The radio first, and outside s.mu: it is a bus connection with a pairing
// agent exported on it, and one left behind is an agent for a session that has
// ended - bluetoothd would keep calling it and every question would time out
// into a refusal nobody was asked for.
func (s *Server) Close() error {
	s.closeRadio()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	err := s.ln.Close()
	s.ln = nil
	return err
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	if err := allowPeer(conn); err != nil {
		// Say nothing useful to a peer that should not be here.
		writeResponse(conn, Response{Error: "not permitted"})
		return
	}
	// Every write to this connection goes through the sink, because two of them
	// can now happen at once: a reply to something asked, and an event pushed
	// while that reply is being written. Interleaved, they would produce one
	// line that is neither.
	k := &sink{w: conn}
	defer s.unlisten(k)

	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			k.reply(Response{Error: "malformed request"})
			continue
		}
		if req.Method == MethodEvents {
			// The connection stays a connection: it keeps answering requests,
			// and events arrive on it as well. A client that wanted a second
			// socket for them can have one, and one that does not need not.
			if len(req.Args) != 0 {
				k.reply(Response{Error: MethodEvents + " takes no arguments"})
				continue
			}
			s.listen(k)
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
			go s.askRun(k, req.Args)
			continue
		}
		k.reply(s.Dispatch(req))
	}
}

// allowPeer refuses anyone but the user who owns this zded. Peer credentials
// come from the kernel, so a caller cannot claim to be someone else - which is
// the same reasoning as attribution by channel (vision.md, principle 6).
func allowPeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if uint32(os.Getuid()) != cred.Uid {
		return fmt.Errorf("uid %d is not %d", cred.Uid, os.Getuid())
	}
	return nil
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
	case "ask.oneshot", "ask.panel":
		// The surface, and only the surface: the question is typed into it, so
		// there is nothing to pass here. Which one is the kind of the event,
		// because that is the whole difference between them.
		if len(req.Args) != 0 {
			return Response{Error: req.Method + " takes no arguments: the question is typed into the window"}
		}
		if req.Method == "ask.panel" {
			return s.askSurface(EventAskPanel)
		}
		return s.askSurface(EventAsk)
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
	case "net.disconnect":
		if len(req.Args) != 0 {
			return Response{Error: "net.disconnect takes no arguments"}
		}
		return s.netDisconnect()
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
		if len(req.Args) != 1 {
			return Response{Error: "desk.move-window-to takes one desk name"}
		}
		return s.moveWindowTo(req.Args[0])
	case "desk.move-workspace-to":
		if len(req.Args) != 1 {
			return Response{Error: "desk.move-workspace-to takes one desk name"}
		}
		return s.moveWorkspaceTo(req.Args[0])
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
	for _, c := range m.Conflicts() {
		out.Conflict = append(out.Conflict, c.Workspace.Name+": "+c.Reason)
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

	d, err := manifest.FromMap(m, target)
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
	landing, onThisScreen := desk.Landing(m, target, monitor, slots[monitor])
	if !onThisScreen {
		// The desk owns nothing on this screen, so this is the one case where
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
	rec := attn.Record{
		From:    n.From,
		Text:    n.Text,
		Body:    n.Body,
		Urgent:  n.Urgent,
		Actions: n.Actions,
		Extra:   n.Extra,
		At:      time.Now(),
		Desk:    s.whereWeAre(),
	}
	if s.mode().Queues(n.Urgent) {
		it, err := s.jrn.Queue(journal.Item{
			Text:   rec.Text,
			Body:   rec.Body,
			Desk:   rec.Desk,
			From:   rec.From,
			Urgent: rec.Urgent,
		})
		if err != nil {
			return 0, err
		}
		rec.ID, rec.Queued = it.ID, true
	} else {
		id, err := s.jrn.ClaimID()
		if err != nil {
			return 0, err
		}
		rec.ID = id
	}
	// What the history pushed out is what nothing can reach any more: it cannot
	// be listed, dismissed or invoked, so the bus side is told to stop holding
	// the sender's names for it. Without this, the one table in the system with
	// no bound would grow by one for every notification the session ever
	// received - and the modes made that worse, because a notification a mode
	// keeps off the queue is one nobody can finish, so nothing else prunes it.
	if gone := s.history.Add(rec); gone != 0 {
		if w := s.watcher(); w != nil {
			w.Forget(gone)
		}
	}
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
	if text == "" {
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
		// (docs/model.md, section 5). Only on a change of desk: re-entering the
		// desk you are standing on is what half the nav keys do, and each pass
		// would be a launch attempt per declared app.
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
	go func() {
		for _, app := range apps {
			address := zinc.Address(app.App, app.Instance)
			if err := s.launch(address); err != nil {
				// The daemon's log is where this belongs: it is one app on one
				// desk, and taking the switch down over it would make an
				// unbuildable image cost somebody their whole desk. "Already
				// running" arrives here too, which is worth reading rather than
				// filtering - it is how you find out a desk started twice.
				log.Printf("zded: starting %s: %v", address, err)
			}
		}
	}()
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
		st.Compositor = err.Error()
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
	st.Shell = s.listeners() > 0
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
	s.mu.Unlock()
	return st
}

// rememberProblems keeps what the last manifest read could not use, so that
// `zde status` can say so. Replaced rather than accumulated: the list is the
// state of the directory now, and a manifest somebody has since fixed should
// stop being mentioned.
func (s *Server) rememberProblems(problems []manifest.Problem) {
	list := make([]string, 0, len(problems))
	for _, p := range problems {
		list = append(list, p.String())
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

func writeResponse(w interface{ Write([]byte) (int, error) }, resp Response) {
	line, err := json.Marshal(resp)
	if err != nil {
		line = []byte(`{"error":"zded could not encode its own reply"}`)
	}
	w.Write(append(line, '\n'))
}
