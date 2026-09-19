package zded

import "github.com/crispuscrew/zde/internal/attn"

// popupBacklog bounds display backlog only; records are retained before offering a popup.
const popupBacklog = 16

// maybePop uses the arrival's privacy and mode snapshot; neither gate deletes history.
func (s *Server) maybePop(rec attn.Record, mode attn.Mode) {
	if !mode.Pops(rec.Urgent) {
		return
	}
	if rec.Private {
		return
	}
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	s.pop(Event{
		Kind:          EventAttnPopup,
		Notifications: []attn.Record{rec},
		Output:        output,
	})
}

// pop never waits on a shell from the notification bus call. One pump preserves order.
func (s *Server) pop(ev Event) {
	s.startPump.Do(func() {
		s.popups = make(chan Event, popupBacklog)
		go s.pumpPopups(s.popups)
	})
	select {
	case s.popups <- ev:
	default:
	}
}

func (s *Server) pumpPopups(in <-chan Event) {
	for {
		select {
		case <-s.popupStop:
			return
		case ev := <-in:
			s.broadcast(ev)
		}
	}
}
