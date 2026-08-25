package zded

import (
	"errors"
	"fmt"
	"log"
	"time"
)

const filmMaximum = 3 * time.Hour
const filmRetryFor = 30 * time.Second

type Film struct {
	Active  bool   `json:"film"`
	Until   string `json:"until,omitempty"`
	Pending bool   `json:"lockPending,omitempty"`
}

func (s *Server) filmState() Film {
	s.filmMu.Lock()
	defer s.filmMu.Unlock()
	return s.filmStateLocked(time.Now())
}

func (s *Server) filmStateLocked(now time.Time) Film {
	if s.filmUntil.IsZero() {
		return Film{}
	}
	state := Film{Until: s.filmUntil.Format(time.RFC3339)}
	state.Pending = !now.Before(s.filmUntil)
	state.Active = !state.Pending
	return state
}

func (s *Server) film(args []string) Response {
	if len(args) == 0 {
		return ok(s.filmState())
	}
	if len(args) != 1 {
		return Response{Error: "system.film takes on, off or toggle, or nothing to read it back"}
	}
	on := false
	switch args[0] {
	case "on":
		on = true
	case "off":
	case "toggle":
		state := s.filmState()
		on = !state.Active && !state.Pending
	default:
		return Response{Error: "system.film takes on, off or toggle, or nothing to read it back"}
	}
	state, err := s.setFilm(on)
	if err != nil {
		return Response{Error: err.Error()}
	}
	return ok(state)
}

func (s *Server) setFilm(on bool) (Film, error) {
	s.filmMu.Lock()
	current := s.filmStateLocked(time.Now())
	if on && current.Pending {
		s.filmMu.Unlock()
		return current, fmt.Errorf("Film expired and its screen lock is still pending")
	}
	if on && current.Active {
		s.filmMu.Unlock()
		return current, nil
	}
	if !on {
		if err := s.removeFilm(s.filmPath); err != nil {
			s.filmMu.Unlock()
			return current, fmt.Errorf("turning Film off: %w", err)
		}
		if s.filmTimer != nil {
			s.filmTimer.Stop()
		}
		s.filmUntil, s.filmTimer = time.Time{}, nil
		s.filmMu.Unlock()
		s.broadcast(Event{Kind: EventFilm})
		return Film{}, nil
	}
	within := s.filmFor
	if within <= 0 || within > filmMaximum {
		within = filmMaximum
	}
	until := time.Now().Add(within)
	state := Film{Active: true, Until: until.Format(time.RFC3339)}
	if err := writeFilmState(s.filmPath, until); err != nil {
		s.filmMu.Unlock()
		return current, fmt.Errorf("turning Film on: %w", err)
	}
	s.filmUntil = until
	s.filmTimer = time.AfterFunc(within, func() { s.expireFilm(until) })
	s.filmMu.Unlock()
	s.broadcast(Event{Kind: EventFilm})
	return state, nil
}

func (s *Server) expireFilm(until time.Time) {
	s.filmMu.Lock()
	if !s.filmUntil.Equal(until) {
		s.filmMu.Unlock()
		return
	}
	s.filmTimer = nil
	s.filmMu.Unlock()
	s.broadcast(Event{Kind: EventFilm})

	resp := s.lockScreen()
	s.filmMu.Lock()
	pending := s.filmUntil.Equal(until)
	if pending && !s.filmClosed {
		retry := s.filmRetry
		if retry <= 0 {
			retry = filmRetryFor
		}
		s.filmTimer = time.AfterFunc(retry, func() { s.expireFilm(until) })
	}
	s.filmMu.Unlock()
	if resp.Error != "" {
		log.Printf("zded: Film expired but the screen did not lock; retrying: %s", resp.Error)
	} else if pending {
		log.Printf("zded: Film locked but its expired state remains; retrying: %s", resp.Note)
	}
}

func (s *Server) clearPendingFilm(resp Response) Response {
	s.filmMu.Lock()
	if s.filmUntil.IsZero() || time.Now().Before(s.filmUntil) {
		s.filmMu.Unlock()
		return resp
	}
	if err := s.removeFilm(s.filmPath); err != nil {
		if writeErr := writeFilmState(s.filmPath, time.Time{}); writeErr != nil {
			resp.Note = "screen locked, but clearing expired Film state failed: " + errors.Join(err, writeErr).Error()
		} else {
			resp.Note = "screen locked; expired Film state was disabled after removal failed: " + err.Error()
		}
	}
	if s.filmTimer != nil {
		s.filmTimer.Stop()
	}
	s.filmUntil, s.filmTimer = time.Time{}, nil
	s.filmMu.Unlock()
	s.broadcast(Event{Kind: EventFilm})
	return resp
}
