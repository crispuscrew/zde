package zded

import (
	"sort"
	"strconv"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
)

// Window uses the full zde workspace name so rows identify both desk and monitor.
type Window struct {
	ID        uint64 `json:"id"`
	Title     string `json:"title,omitempty"`
	AppID     string `json:"appId,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

type Jump struct {
	Shown   bool     `json:"shown"`
	Windows []Window `json:"windows"`
}

// windows cleans every externally supplied field before it reaches a terminal or row.
// The compositor returns a caller-owned slice, so cleaning it in place is safe.
func (s *Server) windows() ([]Window, error) {
	windows, err := s.niri.Windows()
	if err != nil {
		return nil, err
	}
	for i := range windows {
		windows[i].Title = attn.Line(windows[i].Title)
		windows[i].AppID = attn.Line(windows[i].AppID)
		windows[i].Workspace = attn.Line(windows[i].Workspace)
	}
	return windows, nil
}

func (s *Server) jumpTo() Response {
	windows, err := s.windows()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if len(windows) == 0 {
		return Response{Error: "nothing is open to jump to"}
	}
	sortWindows(windows)
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	shown := s.showSurface(Event{
		Kind:    EventWindows,
		Windows: windows,
		Output:  output,
	})
	return ok(Jump{Shown: shown, Windows: windows})
}

// sortWindows keeps rows stable across presses; unique window IDs break workspace ties.
func sortWindows(windows []Window) {
	sort.SliceStable(windows, func(i, j int) bool {
		if windows[i].Workspace != windows[j].Workspace {
			return windows[i].Workspace < windows[j].Workspace
		}
		return windows[i].ID < windows[j].ID
	})
}

// focusWindow brings up the whole desk first, then focuses without rearranging windows.
func (s *Server) focusWindow(arg string) Response {
	id, err := strconv.ParseUint(arg, 10, 64)
	if err != nil {
		return Response{Error: "window.jump-to wants the id from the list, not " + strconv.Quote(arg)}
	}
	windows, err := s.windows()
	if err != nil {
		return Response{Error: err.Error()}
	}
	target, found := Window{}, false
	for _, w := range windows {
		if w.ID == id {
			target, found = w, true
			break
		}
	}
	if !found {
		return Response{Error: "no window with id " + arg + ": it is not open any more"}
	}
	landing, parseErr := desk.ParseName(target.Workspace)
	onADesk := parseErr == nil
	if onADesk {
		m, err := s.niri.DeskMap()
		if err != nil {
			return Response{Error: err.Error()}
		}
		if from := s.activeDesk(m); from != landing.Desk {
			if resp := s.switchFrom(landing.Desk, from); resp.Error != "" {
				return resp
			}
		}
	}
	if err := s.niri.FocusWindow(id); err != nil {
		return Response{Error: err.Error()}
	}
	if s.jrn != nil && onADesk {
		s.jrn.SetActive(landing)
	}
	if target.Workspace == "" {
		return ok([]string{})
	}
	return ok([]string{target.Workspace})
}
