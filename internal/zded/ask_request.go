package zded

import (
	"fmt"
	"strings"
)

func (s *Server) claimAsk() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.asks >= asksMax {
		return false
	}
	s.asks++
	return true
}

func (s *Server) releaseAsk() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asks--
}

func (s *Server) asking() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asks
}

// askOn validates and claims on the read loop, so refused requests cost no goroutine.
// The daemon claim precedes asking, keeping admission's exemption bounded by asksMax.
func (s *Server) askOn(k *sink, args []string, requestID string) {
	if len(args) < 2 || len(args)%2 != 0 {
		k.replyTo(requestID, Response{Error: MethodAskRun + " takes a tier, a question, and the turns before it in pairs: what was asked, what came back"})
		return
	}
	tier, question := args[0], strings.TrimSpace(args[1])
	if question == "" {
		k.replyTo(requestID, Response{Error: "nothing to ask: say what the question is"})
		return
	}
	prior := args[2:]
	for _, turn := range prior {
		if strings.TrimSpace(turn) == "" {
			k.replyTo(requestID, Response{Error: "one of the turns before that question is empty: a question nothing answered is not a turn to carry"})
			return
		}
	}
	argv, err := askTier(tier)
	if err != nil {
		k.replyTo(requestID, Response{Error: err.Error()})
		return
	}
	doc := askDoc(prior, question)
	if len(doc) > askContextMax {
		if len(prior) == 0 {
			k.replyTo(requestID, Response{Error: fmt.Sprintf("that question is %d KiB, and one ask carries %d: ask it in fewer words",
				overKiB(len(doc)), askContextMax>>10)})
			return
		}
		k.replyTo(requestID, Response{Error: fmt.Sprintf("this conversation has reached %d KiB, and one ask carries %d: start a fresh one and ask it there",
			overKiB(len(doc)), askContextMax>>10)})
		return
	}
	if k.asking.Load() {
		k.replyTo(requestID, Response{Error: "this connection is still answering the last question: wait for it, or ask on another"})
		return
	}
	if !s.claimAsk() {
		k.replyTo(requestID, Response{Error: fmt.Sprintf("%d tiers are already running, which is every one zded runs at once: wait for one to answer", asksMax)})
		return
	}
	k.asking.Store(true)
	if !s.startRun(k, tier, argv, doc, requestID) {
		k.asking.Store(false)
		s.releaseAsk()
		k.replyTo(requestID, Response{Error: "zded is stopping, so there is nothing to ask it"})
	}
}
