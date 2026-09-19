package zded

import (
	"fmt"
	"sort"
	"strings"

	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/zinc"
)

// DeskApp exposes an instance address and placement; zinc owns its storage location.
type DeskApp struct {
	Address string `json:"address"`
	Place   string `json:"place,omitempty"`
}

func (s *Server) deskApps(args []string) Response {
	target := ""
	switch len(args) {
	case 0:
		focused, err := s.niri.FocusedName()
		if err != nil {
			return Response{Error: err.Error()}
		}
		n, err := desk.ParseName(focused)
		if err != nil {
			return Response{Error: fmt.Sprintf("%q is not a desk workspace, so there is no desk to list: name one", focused)}
		}
		target = n.Desk
	case 1:
		target = args[0]
	default:
		return Response{Error: "desk.apps takes one desk name, or none for the one you are on"}
	}
	all, problems, err := s.desks.All()
	if err != nil {
		return Response{Error: err.Error()}
	}
	s.rememberProblems(problems)
	d, found := all[target]
	if !found {
		declared := make([]string, 0, len(all))
		for name := range all {
			declared = append(declared, name)
		}
		sort.Strings(declared)
		if len(declared) == 0 {
			return Response{Error: fmt.Sprintf("no desk %q is declared, and neither is any other", target)}
		}
		return Response{Error: fmt.Sprintf("no desk %q is declared; there is %s", target, strings.Join(declared, ", "))}
	}
	out := make([]DeskApp, 0, len(d.Apps))
	for _, app := range d.Apps {
		a := DeskApp{Address: zinc.Address(app.App, app.Instance)}
		if app.Monitor != "" && app.Workspace != "" {
			if n, err := desk.NewName(target, app.Monitor, app.Workspace); err == nil {
				a.Place = n.String()
			}
		}
		out = append(out, a)
	}
	return ok(out)
}

// snapshot reads the focused desk from the compositor and preserves declared privacy.
// The manifest store also refuses overwriting an existing declaration.
func (s *Server) snapshot(args []string) Response {
	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	target := ""
	switch len(args) {
	case 0:
		if focused, err := s.niri.FocusedName(); err == nil {
			if n, err := desk.ParseName(focused); err == nil {
				target = n.Desk
			}
		}
		if target == "" {
			return Response{Error: "no desk is focused, so there is none to write down: name one"}
		}
	case 1:
		target = args[0]
	default:
		return Response{Error: "desk.snapshot takes one desk name, or none for the one you are on"}
	}
	if target == desk.Regulars {
		return Response{Error: "the regulars are not a desk, so there is no manifest to write: they are named into, never declared"}
	}

	private := false
	if prev := s.manifestFor(target); prev != nil {
		private = prev.Private
	}
	d, err := manifest.FromMap(m, target, private)
	if err != nil {
		return Response{Error: err.Error()}
	}
	path, err := s.desks.Save(d)
	if err != nil {
		return Response{Error: err.Error()}
	}
	return ok(path)
}
