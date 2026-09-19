package zded

import (
	"os"
	"os/exec"
	"strconv"

	"github.com/crispuscrew/zde/internal/keymap"
)

// Action keeps unavailable bindings visible and explains why choosing one cannot work.
type Action struct {
	Name  string `json:"name"`
	Group string `json:"group"`
	Desc  string `json:"desc"`
	Key   string `json:"key,omitempty"`
	Live  bool   `json:"live"`
	Why   string `json:"why,omitempty"`
}

type Palette struct {
	Shown   bool     `json:"shown"`
	Actions []Action `json:"actions"`
}

func (s *Server) palette() Response {
	list := s.actions()
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	shown := s.showSurface(Event{
		Kind:    EventPalette,
		Actions: list,
		Output:  output,
	})
	return ok(Palette{Shown: shown, Actions: list})
}

func (s *Server) actions() []Action {
	known := known()
	out := make([]Action, 0, len(known))
	for _, a := range known {
		why := whyNot(a)
		out = append(out, Action{
			Name:  a.Name,
			Group: a.Group,
			Desc:  a.Desc,
			Key:   a.Key,
			Live:  why == "",
			Why:   why,
		})
	}
	return out
}

// known rereads the bounded regular keymap file to follow configuration changes.
// Read failures leave actions available with unknown keys; never block on a FIFO.
func known() []keymap.Action {
	data, err := keymap.ReadText()
	if err != nil {
		data = nil
	}
	return keymap.Actions(data)
}

// whyNot is shared by rows and refusals; executable availability uses the daemon's PATH.
func whyNot(a keymap.Action) string {
	if a.Native != "" {
		if !a.Performs {
			return "niri's own, and the key is the only way to ask for it"
		}
		return ""
	}
	if !a.Live || len(a.Spawn) == 0 {
		return "nothing is written behind it yet"
	}
	if _, err := exec.LookPath(a.Spawn[0]); err != nil {
		return a.Spawn[0] + " is not installed"
	}
	return ""
}

func (s *Server) runAction(name string) Response {
	for _, a := range known() {
		if a.Name != name {
			continue
		}
		if why := whyNot(a); why != "" {
			return Response{Error: name + ": " + why}
		}
		if a.Native != "" {
			if err := s.niri.Perform(a.Native); err != nil {
				return Response{Error: err.Error()}
			}
			return ok([]string{})
		}
		if err := s.spawn(a.Spawn); err != nil {
			return Response{Error: err.Error()}
		}
		return ok([]string{})
	}
	return Response{Error: "no action called " + strconv.Quote(name) + ": `zde palette` lists them"}
}

// spawnDetached reaps in the background; its child still belongs to the daemon's unit.
func spawnDetached(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
