package zded

import (
	"log"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
)

type Attn struct {
	Mode string `json:"mode"`
}

// mode defaults unknown journal values to work; setMode rejects an empty explicit choice.
func (s *Server) mode() attn.Mode {
	if s.jrn == nil {
		return attn.Work
	}
	m, err := attn.ParseMode(s.jrn.Mode())
	if err != nil {
		return attn.Work
	}
	return m
}

// setMode ends a desk's loan so a manually chosen mode follows the person off that desk.
func (s *Server) setMode(name string) Response {
	if name == "" {
		return Response{Error: "no mode given: it is work, focus or quiet"}
	}
	m, err := attn.ParseMode(name)
	if err != nil {
		return Response{Error: err.Error()}
	}
	if s.jrn == nil {
		return Response{Error: "no journal, so a mode could not be remembered - and a mode that is forgotten is worse than none"}
	}
	if err := s.jrn.SetMode(string(m)); err != nil {
		return Response{Error: err.Error()}
	}
	if s.jrn.Borrowed().Desk != "" {
		if err := s.jrn.SetBorrowed(journal.Borrowed{}); err != nil {
			return Response{Error: err.Error()}
		}
	}
	return ok(Attn{Mode: string(m)})
}

func (s *Server) toggleQuiet() Response {
	if s.mode() == attn.Quiet {
		return s.setMode(string(attn.Work))
	}
	return s.setMode(string(attn.Quiet))
}

// enterDesk returns a borrowed mode before applying the destination's policy.
// Re-entering a borrower preserves its original loan; unchanged values avoid fsyncs.
func (s *Server) enterDesk(target string) {
	if s.jrn == nil {
		return // nothing to write a mode into, so nothing can be given back
	}
	was := s.mode()
	now := was
	held := s.jrn.Borrowed()
	if held.Desk != "" && held.Desk != target {
		back, _ := attn.ParseMode(held.Mode)
		now = back
		held = journal.Borrowed{}
	}
	if declared, declares := s.declaredMode(target); declares {
		if held.Desk != target {
			held = journal.Borrowed{Desk: target, Mode: string(now)}
		}
		now = declared
	}
	if held != s.jrn.Borrowed() {
		if err := s.jrn.SetBorrowed(held); err != nil {
			log.Printf("zded: remembering which desk lent the mode: %v", err)
		}
	}
	if now != was {
		if err := s.jrn.SetMode(string(now)); err != nil {
			log.Printf("zded: entering %s in %s: %v", target, now, err)
		}
	}
}

// declaredMode distinguishes no declaration from work; silence must not enable popups.
func (s *Server) declaredMode(target string) (attn.Mode, bool) {
	d := s.manifestFor(target)
	if d == nil || d.Policies.Attn == "" {
		return "", false
	}
	m, err := attn.ParseMode(d.Policies.Attn)
	if err != nil {
		log.Printf("zded: desk %s declares attn %q, which this zde cannot read: %v", target, d.Policies.Attn, err)
		return "", false
	}
	return m, true
}
