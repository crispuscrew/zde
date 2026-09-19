package zded

import (
	"errors"
	"time"

	"github.com/crispuscrew/zde/internal/link"
)

// noManagerFor caches absence across polling, but eventually notices a manager appearing.
const noManagerFor = 5 * time.Minute

// links lazily opens NetworkManager and releases dead connections before redialling.
// Only absence is cached; transient failures are retried.
func (s *Server) links() (link.Manager, error) {
	s.linkMu.Lock()
	defer s.linkMu.Unlock()
	if s.link != nil {
		if alive, ok := s.link.(interface{ Alive() bool }); !ok || alive.Alive() {
			return s.link, nil
		}
		if closer, ok := s.link.(interface{ Close() error }); ok {
			closer.Close() //nolint:errcheck // it is already the connection that stopped working
		}
		s.link = nil
	}
	if !s.noManagerAt.IsZero() && time.Since(s.noManagerAt) < noManagerFor {
		return nil, s.noManager
	}
	open := s.openLink
	if open == nil {
		open = link.Open
	}
	m, err := open()
	if err != nil {
		if errors.Is(err, link.ErrNoManager) {
			s.noManager, s.noManagerAt = err, time.Now()
		}
		return nil, err
	}
	s.noManager, s.noManagerAt = nil, time.Time{}
	s.link = m
	return m, nil
}

func (s *Server) netStatus() Response {
	m, err := s.links()
	if errors.Is(err, link.ErrNoManager) {
		return ok(link.Status{Kind: link.KindAbsent})
	}
	if err != nil {
		return Response{Error: err.Error()}
	}
	st, err := m.Status()
	if errors.Is(err, link.ErrNoManager) {
		return ok(link.Status{Kind: link.KindAbsent})
	}
	if err != nil {
		return Response{Error: err.Error()}
	}
	return ok(st)
}

func (s *Server) netList() Response {
	m, err := s.links()
	if err != nil {
		return Response{Error: err.Error()}
	}
	networks, err := m.List()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if networks == nil {
		networks = []link.Network{}
	}
	return ok(networks)
}
