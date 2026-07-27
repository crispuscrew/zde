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
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/journal"
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
	// RenameWorkspace corrects a name that no longer tells the truth.
	RenameWorkspace(from, to string) error
	// SetWorkspaceNameByID names a workspace that has no name to be
	// addressed by, which is what adoption claims.
	SetWorkspaceNameByID(id uint64, name string) error
	// FirstApps is the app of the first window on each workspace, by niri's
	// workspace id. It is what an adopted workspace is named after.
	FirstApps() (map[uint64]string, error)
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
	LastDesk   string `json:"lastDesk,omitempty"`
	Skipped    int    `json:"journalSkipped"`
}

// Server answers the zde socket.
type Server struct {
	version string
	jrn     *journal.Journal
	niri    Compositor

	mu sync.Mutex
	ln net.Listener
}

func New(version string, jrn *journal.Journal, compositor Compositor) *Server {
	return &Server{version: version, jrn: jrn, niri: compositor}
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

// Close stops listening.
func (s *Server) Close() error {
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
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			writeResponse(conn, Response{Error: "malformed request"})
			continue
		}
		writeResponse(conn, s.Dispatch(req))
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

	active := ""
	if focused, err := s.niri.FocusedName(); err == nil {
		if n, err := desk.ParseName(focused); err == nil {
			active = n.Desk
		}
	}
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

// switchDesk brings a desk up on every monitor it owns workspaces on, and
// records where it left from so desk.last can come back.
func (s *Server) switchDesk(target string) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	var lastActive map[string]string
	if s.jrn != nil {
		lastActive = s.jrn.State().LastActive[target]
	}
	plan := desk.SwitchPlan(m, target, lastActive)
	if len(plan) == 0 {
		// Nothing to focus is not the same as a failed switch, but it is not
		// a switch either: say so rather than pretending the desk is up.
		return Response{Error: "desk " + target + " has no workspaces"}
	}

	// Where we are now, before anything moves, so desk.last has somewhere to
	// go back to. A failure to read it is not worth refusing the switch over.
	from := ""
	if focused, err := s.niri.FocusedName(); err == nil {
		if n, err := desk.ParseName(focused); err == nil {
			from = n.Desk
		}
	}

	for _, n := range plan {
		if err := s.niri.FocusWorkspace(n.String()); err != nil {
			// Partway through: some monitors have moved. Report it rather
			// than carrying on, because the state is now worth looking at.
			return Response{Error: "switching to " + target + ": " + err.Error()}
		}
	}
	if s.jrn != nil {
		for _, n := range plan {
			s.jrn.SetActive(n)
		}
		if from != "" && from != target {
			s.jrn.SetLastDesk(from)
		}
	}
	return ok(plan)
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
		st.LastDesk = s.jrn.State().LastDesk
		st.Skipped = s.jrn.Skipped()
	}
	return st
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
