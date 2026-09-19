package zded

import (
	"strconv"

	"github.com/crispuscrew/zde/internal/attn"
)

type Center struct {
	Shown         bool          `json:"shown"`
	Notifications []attn.Record `json:"notifications"`
}

type Reach struct {
	Reached bool `json:"reached"`
}

func (s *Server) reach() Response {
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	return ok(Reach{Reached: s.showSurface(Event{Kind: EventAttnReach, Output: output})})
}

func (s *Server) center() Response {
	seen := s.history.Recent()
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	shown := s.showSurface(Event{
		Kind:          EventCenter,
		Notifications: seen,
		Output:        output,
	})
	return ok(Center{Shown: shown, Notifications: seen})
}

// invoke validates actions against a live record, never trusting a key from the socket.
func (s *Server) invoke(id, key string) Response {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return Response{Error: "attn.invoke wants the id from the history, not " + strconv.Quote(id)}
	}
	rec, found := s.history.Find(n)
	if !found {
		return Response{Error: "nothing in the history has id " + id + ", so there is nothing to act on"}
	}
	if rec.Restored {
		return Response{Error: strconv.Quote(rec.Text) + " arrived before this session started, so whatever it offered went with the app that sent it"}
	}
	if len(rec.Actions) == 0 {
		return Response{Error: strconv.Quote(rec.Text) + " came with no actions: its app sent a notification, not a button"}
	}
	if !rec.Allows(key) {
		return Response{Error: strconv.Quote(rec.Text) + " never offered " + strconv.Quote(key)}
	}
	w := s.watcher()
	if w == nil {
		return Response{Error: "zded is not the notification server on this session, so there is nobody to tell"}
	}
	if err := w.Invoke(n, key); err != nil {
		return Response{Error: err.Error()}
	}
	return ok([]string{})
}
