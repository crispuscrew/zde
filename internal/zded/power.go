package zded

import (
	"strconv"

	"github.com/crispuscrew/zde/internal/power"
)

const EventPower = "power"

const powerLock = "lock"

// PowerChoice carries concrete costs and the same refusal that execution would return.
type PowerChoice struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Desc    string   `json:"desc"`
	Confirm bool     `json:"confirm,omitempty"`
	Costs   []string `json:"costs,omitempty"`
	Why     string   `json:"why,omitempty"`
}

type Power struct {
	Shown   bool          `json:"shown"`
	Choices []PowerChoice `json:"choices"`
}

// powerMenu still offers locking without logind or a compositor.
func (s *Server) powerMenu() Response {
	choices := s.powerChoices()
	output := ""
	if s.niri != nil {
		if _, place, err := s.niri.FocusedPlace(); err == nil {
			output = place
		}
	}
	shown := s.showSurface(Event{
		Kind:    EventPower,
		Choices: choices,
		Output:  output,
	})
	return ok(Power{Shown: shown, Choices: choices})
}

func (s *Server) powerChoices() []PowerChoice {
	st, err := s.powerState()
	why := ""
	if err != nil {
		why = err.Error()
	}
	windows := s.windowsOpen()
	unqueued := s.unqueuedArrivals()

	ending := append(append([]string{}, closes(windows)...), inMemory(unqueued)...)
	return []PowerChoice{{
		Name:  powerLock,
		Label: "lock",
		Desc:  "lock the screen, and leave everything running",
	}, {
		Name:    string(power.Logout),
		Label:   "log out",
		Desc:    "end this session and go back to the greeter",
		Confirm: true,
		Costs:   ending,
		Why:     why,
	}, {
		Name:    string(power.Suspend),
		Label:   "suspend",
		Desc:    "sleep, and come back to this session",
		Confirm: len(st.Blocking(power.Suspend)) > 0,
		Costs:   held(st, power.Suspend),
		Why:     why,
	}, {
		Name:    string(power.Reboot),
		Label:   "reboot",
		Desc:    "restart the machine",
		Confirm: true,
		Costs:   append(append(append([]string{}, ending...), others(st)...), held(st, power.Reboot)...),
		Why:     why,
	}, {
		Name:    string(power.PowerOff),
		Label:   "power off",
		Desc:    "turn the machine off",
		Confirm: true,
		Costs:   append(append(append([]string{}, ending...), others(st)...), held(st, power.PowerOff)...),
		Why:     why,
	}}
}

// powerRun reuses the palette's configured locker; other actions are logind's.
// Confirmation belongs to the surface; errors must not claim a successful action.
func (s *Server) powerRun(name string) Response {
	if name == powerLock {
		if resp := s.runAction("system.lock"); resp.Error != "" {
			return resp
		}
		return ok("locking")
	}
	w := power.What(name)
	switch w {
	case power.Logout, power.Suspend, power.Reboot, power.PowerOff:
	default:
		return Response{Error: "no power action called " + strconv.Quote(name) +
			": `zde system power` lists them"}
	}
	m, err := s.logins()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if err := m.Do(w); err != nil {
		return Response{Error: err.Error()}
	}
	return ok(started(w))
}

func started(w power.What) string {
	switch w {
	case power.Logout:
		return "logging out"
	case power.Suspend:
		return "suspending"
	case power.Reboot:
		return "rebooting"
	default:
		return "powering off"
	}
}
