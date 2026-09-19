package zded

import (
	"errors"
	"time"

	"github.com/crispuscrew/zde/internal/power"
)

// noLogindFor avoids redialling on every idle poll, while noticing a newly started logind.
const noLogindFor = time.Minute

// logins shares one dial without holding powerMu across it. Concurrent callers use
// a cached absence if available, otherwise wait for that dial's published result.
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
		err := s.noLogind
		s.powerMu.Unlock()
		return nil, err
	}
	if d := s.dialing; d != nil {
		if s.noLogind != nil {
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
	close(d.done)
	return m, err
}

// dial fields are written before done closes and read only after it closes.
type dial struct {
	done chan struct{}
	m    power.Manager
	err  error
}

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
