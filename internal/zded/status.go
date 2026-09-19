package zded

import (
	"os/exec"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/zinc"
)

// Status is the session diagnostic, including bounded connection populations.
type Status struct {
	Version       string   `json:"version"`
	Compositor    string   `json:"compositor"` // "connected", or why not
	Desks         int      `json:"desks"`
	OnDesk        string   `json:"onDesk,omitempty"`
	LastDesk      string   `json:"lastDesk,omitempty"`
	Skipped       int      `json:"journalSkipped"`
	Shell         bool     `json:"shell"`
	Listeners     int      `json:"listeners"`
	Connections   int      `json:"connections"`
	Dropped       uint64   `json:"dropped"`
	Notifications bool     `json:"notifications"`
	Queued        int      `json:"queued"`
	Mode          string   `json:"mode"`
	Zen           bool     `json:"zen"`
	Zinc          bool     `json:"zinc"`
	BadManifests  []string `json:"badManifests,omitempty"`
	Unplaced      int      `json:"unplaced,omitempty"`
}

// status reports compositor failure rather than failing the diagnostic itself.
func (s *Server) status() Status {
	st := Status{Version: s.version, Compositor: "connected"}
	m, err := s.niri.DeskMap()
	if err != nil {
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
	s.mu.Lock()
	st.Connections = len(s.conns)
	st.Dropped = s.dropped
	s.mu.Unlock()
	st.Notifications = s.watcher() != nil
	st.Mode = string(s.mode())
	st.Zen = s.zenState()
	_, err = exec.LookPath(zinc.Runner)
	st.Zinc = err == nil
	s.mu.Lock()
	st.BadManifests = append([]string(nil), s.problems...)
	st.Unplaced = int(s.unplaced)
	s.mu.Unlock()
	return st
}

// rememberProblems replaces stale diagnostics and keeps each terminal row printable.
func (s *Server) rememberProblems(problems []manifest.Problem) {
	list := make([]string, 0, len(problems))
	for _, p := range problems {
		list = append(list, attn.Line(p.String()))
	}
	s.mu.Lock()
	s.problems = list
	s.mu.Unlock()
}
