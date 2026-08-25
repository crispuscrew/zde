package zded

import (
	"os"
)

const idleFile = "idle.json"

type idleConfig struct {
	OLED bool `json:"oled"`
}

// idleAction is Hypridle's narrow entry point. Film may suppress automatic
// actions; hardware sleep is not a ZDE v0.1 action.
func (s *Server) idleAction(action string) Response {
	if action == "display-off" || action == "display-on" {
		s.idleDisplayMu.Lock()
		defer s.idleDisplayMu.Unlock()
	}
	switch action {
	case "lock":
		if film := s.filmState(); film.Active {
			return ok("idle lock suppressed by Film until " + film.Until)
		}
		return s.lockScreen()
	case "display-off":
		if film := s.filmState(); film.Active || film.Pending {
			return ok("display power-off suppressed by Film until " + film.Until)
		}
		allowed, err := s.idleDisplayAllowed()
		if err != nil {
			return Response{Error: err.Error()}
		}
		if !allowed {
			return ok("display stays on: zde.laptop.enable and zde.idle.oled are off")
		}
		if err := s.niri.Perform("power-off-monitors"); err != nil {
			return Response{Error: err.Error()}
		}
		return ok("display off")
	case "display-on":
		if err := s.niri.Perform("power-on-monitors"); err != nil {
			return Response{Error: err.Error()}
		}
		return ok("display on")
	default:
		return Response{Error: "system.idle takes lock, display-off or display-on, or no argument to list logind idle holds"}
	}
}

func (s *Server) idleDisplayAllowed() (bool, error) {
	if _, err := os.Stat(s.laptopPath); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	var cfg idleConfig
	if err := readConfig(idleFile, &cfg); err != nil {
		return false, err
	}
	return cfg.OLED, nil
}
