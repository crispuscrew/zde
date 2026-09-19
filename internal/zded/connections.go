package zded

// ConnectionsMax bounds descriptors and scanner buffers, with room above listener/run caps.
const ConnectionsMax = 256

// admit evicts from the largest PID holder, oldest parsed request first.
// Listeners and claimed asks are exempt but count toward their PID's holdings.
// The arriving connection is chosen only if every other connection is exempt.
func (s *Server) admit(k *sink) (*sink, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k.touch()
	if s.conns == nil {
		s.conns = map[*sink]struct{}{}
	}
	s.conns[k] = struct{}{}
	if len(s.conns) <= ConnectionsMax {
		return nil, 0
	}
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
		out, held = k, holds[k.pid]
	}
	delete(s.conns, out)
	s.dropped++
	return out, held
}

func (s *Server) forget(k *sink) {
	s.mu.Lock()
	delete(s.conns, k)
	s.mu.Unlock()
}

func (s *Server) held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}
