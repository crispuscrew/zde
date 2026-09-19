package zded

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
)

func (s *Server) queueAdd(text string) Response {
	if s.jrn == nil {
		return Response{Error: "no journal, so nothing can be made to wait"}
	}
	text = strings.TrimSpace(text)
	if err := checkQueueText(text); err != nil {
		return Response{Error: err.Error()}
	}
	deskName := s.whereWeAre()
	it, err := s.jrn.Queue(journal.Item{Text: text, Desk: deskName})
	if err != nil {
		return Response{Error: err.Error()}
	}
	return ok(it)
}

// checkQueueText requires visible, printable text in one terminal row and column.
func checkQueueText(text string) error {
	if !attn.Draws(text) {
		return errors.New("nothing to wait for: say what it is")
	}
	if n := utf8.RuneCountInString(text); n > queueTextMax {
		return fmt.Errorf("that is %d characters, and a queue is a list of reminders, not of essays", n)
	}
	for _, r := range text {
		if !unicode.IsPrint(r) {
			return errors.New("one printable line: everything that reads the queue reads it a line and a column at a time")
		}
	}
	return nil
}

const queueTextMax = 300

// queueDone also dismisses history-only notifications using the shared ID space.
func (s *Server) queueDone(id string) Response {
	if s.jrn == nil {
		return Response{Error: "no journal, so nothing is waiting"}
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return Response{Error: "queue.done wants the id from the list, not " + strconv.Quote(id)}
	}
	if err := s.jrn.Done(n); err != nil {
		return Response{Error: err.Error()}
	}
	s.history.Dismiss(n)
	if w := s.watcher(); w != nil {
		w.Dismissed(n)
	}
	return ok([]string{})
}

// queueClear tells each sender and the history about the pre-clear snapshot.
// Arrivals racing that snapshot are not part of its dismissal notifications.
func (s *Server) queueClear() Response {
	if s.jrn == nil {
		return Response{Error: "no journal, so nothing is waiting"}
	}
	waiting := s.jrn.Waiting()
	n, err := s.jrn.Clear()
	if err != nil {
		return Response{Error: err.Error()}
	}
	w := s.watcher()
	for _, it := range waiting {
		s.history.Dismiss(it.ID)
		if w != nil {
			w.Dismissed(it.ID)
		}
	}
	return ok(Cleared{Count: n})
}

type Cleared struct {
	Count int `json:"count"`
}

// queueJump skips vanished desks without finishing the queued work.
func (s *Server) queueJump() Response {
	if s.jrn == nil {
		return Response{Error: "no journal, so nothing is waiting"}
	}
	q := s.jrn.Waiting()
	if len(q) == 0 {
		return Response{Error: "nothing is waiting"}
	}
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	for _, it := range q {
		if it.Desk != "" && len(m.Workspaces(it.Desk)) > 0 {
			return s.switchDesk(it.Desk)
		}
	}
	return Response{Error: "nothing waiting is on a desk that exists: " + strconv.Quote(q[0].Text) + " is first"}
}
