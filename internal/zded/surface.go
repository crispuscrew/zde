package zded

import (
	"strconv"
	"time"
)

const ackWait = 200 * time.Millisecond

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

// acknowledge ignores unknown or late tokens and resolves each known token once.
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

func (s *Server) nextToken() string {
	s.mu.Lock()
	s.tokens++
	n := s.tokens
	s.mu.Unlock()
	return strconv.FormatUint(n, 10)
}

// showSurface registers before broadcasting so an immediate acknowledgement is
// not lost. Receiving bytes alone does not mean a shell drew the surface.
func (s *Server) showSurface(event Event) bool {
	event.Token = s.nextToken()
	acked := s.await(event.Token)
	defer s.stopAwaiting(event.Token)
	if s.broadcast(event) == 0 {
		return false
	}
	timer := time.NewTimer(ackWait)
	defer timer.Stop()
	select {
	case <-acked:
		return true
	case <-timer.C:
		return false
	}
}
