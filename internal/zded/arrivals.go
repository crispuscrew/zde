package zded

import (
	"errors"
	"log"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
)

// Arrived retains the notification regardless of display policy. Privacy and mode
// are sampled once for persistence and popup decisions; journal items omit bodies.
// Self is trusted attribution carried from attn, never derived from sender text.
func (s *Server) Arrived(n attn.Notification) (uint64, error) {
	if s.jrn == nil {
		return 0, errors.New("no journal, so nothing can be kept")
	}
	on := s.whereWeAre()
	rec := attn.Record{
		From:    n.From,
		Self:    n.Self,
		Text:    n.Text,
		Body:    n.Body,
		Urgent:  n.Urgent,
		Actions: n.Actions,
		Extra:   n.Extra,
		At:      time.Now(),
		Desk:    on,
		Private: s.privateArrival(on),
	}
	mode := s.mode()
	if mode.Queues(n.Urgent) {
		it, err := s.jrn.Queue(journal.Item{
			Text:   rec.Text,
			Desk:   rec.Desk,
			From:   rec.From,
			Urgent: rec.Urgent,
			Self:   rec.Self,
		})
		switch {
		case err == nil:
			rec.ID, rec.Queued = it.ID, true
		case errors.Is(err, journal.ErrQueueFull):
			s.saidQueueFull.Do(func() {
				log.Printf("zded: %v. What arrives from now on is shown and recorded, "+
					"and not added to it", err)
			})
		case errors.Is(err, journal.ErrSenderFull):
			s.saidSenderFull.Do(func() {
				log.Printf("zded: %v. The rest of the queue is untouched, and `zde queue` is what it holds", err)
			})
		default:
			return 0, err
		}
	}
	if !rec.Queued {
		id, err := s.jrn.ClaimID()
		if err != nil {
			return 0, err
		}
		rec.ID = id
	}
	var gone []uint64
	if n.Replaces != 0 {
		gone = s.history.Replace(n.Replaces, rec)
	} else {
		gone = s.history.Add(rec)
	}
	if len(gone) > 0 {
		if w := s.watcher(); w != nil {
			for _, id := range gone {
				w.Forget(id)
			}
		}
	}
	s.maybePop(rec, mode)
	return rec.ID, nil
}

// Closed marks history rather than deleting it; attn already checked sender ownership.
func (s *Server) Closed(id uint64) error {
	s.history.Dismiss(id)
	if s.jrn == nil {
		return nil
	}
	return s.jrn.Done(id)
}

// Notifier informs senders about dismissal, actions and records no longer reachable.
type Notifier interface {
	Dismissed(id uint64)
	Invoke(id uint64, key string) error
	Forget(id uint64)
}

func (s *Server) Watching(n Notifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifier = n
}

// watcher copies under mu; bus calls must run after the lock is released.
func (s *Server) watcher() Notifier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notifier
}
