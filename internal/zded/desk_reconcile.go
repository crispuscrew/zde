package zded

import (
	"fmt"
	"log"
	"strings"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/manifest"
)

type Reconciled struct {
	Renamed  []string `json:"renamed"`
	Adopted  []string `json:"adopted"`
	Conflict []string `json:"conflicts,omitempty"`
}

// reconcile corrects names before adoption so free ordinals reflect the corrected map.
func (s *Server) reconcile() Response {
	s.SyncRules()

	m, err := s.niri.DeskMap()
	if err != nil {
		return Response{Error: err.Error()}
	}
	out := Reconciled{Renamed: []string{}, Adopted: []string{}}

	for _, r := range m.Renames() {
		if err := s.niri.RenameWorkspace(r.From.String(), r.To.String()); err != nil {
			return Response{Error: "renaming " + r.From.String() + ": " + err.Error()}
		}
		if s.jrn != nil {
			s.jrn.Renamed(r)
		}
		out.Renamed = append(out.Renamed, r.From.String()+" -> "+r.To.String())
	}

	active := s.activeDesk(m)
	if active != "" {
		firstApp, err := s.niri.FirstApps()
		if err != nil {
			return Response{Error: "reading windows: " + err.Error()}
		}
		for _, a := range desk.AdoptPlan(m, active, firstApp) {
			if err := s.niri.SetWorkspaceNameByID(a.ID, a.Name.String()); err != nil {
				return Response{Error: "adopting into " + active + ": " + err.Error()}
			}
			out.Adopted = append(out.Adopted, a.Name.String())
		}
	}
	for _, c := range m.Conflicts() {
		out.Conflict = append(out.Conflict, attn.Line(c.Workspace.Name)+": "+attn.Line(c.Reason))
	}
	return ok(out)
}

// SyncRules updates placement and zen at startup or reconciliation, not every switch.
func (s *Server) SyncRules() {
	all, _, err := s.desks.All()
	if err != nil {
		return
	}
	if err := writeRules(dynamicPath(), dynamicKDL(s.zenState(), all)); err != nil {
		log.Printf("zded: writing niri's dynamic config: %v", err)
	}
}

// ensureDeclared names compositor empties one at a time, bounded by declared slots.
// Missing monitors are returned as a note; other failures stop the switch.
func (s *Server) ensureDeclared(target string) ([]desk.Name, error) {
	all, problems, err := s.desks.All()
	if err != nil {
		return nil, fmt.Errorf("reading manifests: %w", err)
	}
	s.rememberProblems(problems)
	declared, ok := all[target]
	if !ok {
		return nil, nil // no manifest: the desk is whatever is already named into it
	}
	want := declared.Workspaces()
	var waiting []desk.Name
	for range want {
		m, err := s.niri.DeskMap()
		if err != nil {
			return waiting, err
		}
		empty, err := s.niri.EmptyByOutput()
		if err != nil {
			return waiting, err
		}
		plan, nowhere := desk.MissingPlan(m, want, empty)
		waiting = nowhere
		if len(plan) == 0 {
			return waiting, nil
		}
		a := plan[0]
		if err := s.niri.SetWorkspaceNameByID(a.ID, a.Name.String()); err != nil {
			return waiting, fmt.Errorf("creating %s: %w", a.Name, err)
		}
	}
	return waiting, nil
}

// waitingNote is printed raw by clients, so names pass through the terminal filter.
func waitingNote(waiting []desk.Name) string {
	if len(waiting) == 0 {
		return ""
	}
	names := make([]string, 0, len(waiting))
	for _, n := range waiting {
		names = append(names, n.String())
	}
	return attn.Line("not made, because their monitor is not a screen right now: " + strings.Join(names, ", "))
}

func (s *Server) manifestFor(target string) *manifest.Desk {
	all, problems, err := s.desks.All()
	if err != nil {
		return nil
	}
	s.rememberProblems(problems)
	return all[target]
}
