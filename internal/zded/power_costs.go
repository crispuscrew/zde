package zded

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/power"
)

// windowsOpen degrades to zero so a failed compositor cannot take away the power menu.
func (s *Server) windowsOpen() int {
	if s.niri == nil {
		return 0
	}
	windows, err := s.niri.Windows()
	if err != nil {
		return 0
	}
	return len(windows)
}

func closes(windows int) []string {
	switch {
	case windows <= 0:
		return nil
	case windows == 1:
		return []string{"1 window closes"}
	}
	return []string{fmt.Sprintf("%d windows close", windows)}
}

// unqueuedArrivals excludes persisted queue entries and already-dismissed history.
func (s *Server) unqueuedArrivals() int {
	n := 0
	for _, r := range s.history.Recent() {
		if !r.Queued && !r.Dismissed {
			n++
		}
	}
	return n
}

func inMemory(unqueued int) []string {
	if unqueued <= 0 {
		return nil
	}
	one := "arrivals"
	if unqueued == 1 {
		one = "arrival"
	}
	return []string{fmt.Sprintf("the notification center is holding %d %s the queue never got",
		unqueued, one)}
}

// others filters logind strings because these cost lines may be printed in a terminal.
func others(st power.State) []string {
	var out []string
	for _, s := range st.Others() {
		who := attn.Line(s.User)
		if who == "" {
			who = "session " + attn.Line(s.ID)
		}
		out = append(out, who+" is logged in here as well")
	}
	return out
}

// held filters inhibitor strings supplied by arbitrary local programs before display.
func held(st power.State, w power.What) []string {
	var out []string
	for _, b := range st.Blocking(w) {
		who := attn.Line(b.Who)
		if who == "" {
			who = "something on this machine"
		}
		line := who + " is holding it off"
		if why := attn.Line(b.Why); why != "" {
			line += ": " + why
		}
		out = append(out, line)
	}
	return out
}
