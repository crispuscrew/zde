package zded

import (
	"errors"
	"sync"
	"sync/atomic"
)

// listenersMax bounds broadcast fan-out; subscriber slots can still be exhausted.
const listenersMax = 16

// listen is idempotent, including when the listener cap has been reached.
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

// broadcast sends concurrently so slow listeners cost one sendWait, not one each.
// Busy listeners stay subscribed; failed writes remove them.
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

func (s *Server) listeners() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}
