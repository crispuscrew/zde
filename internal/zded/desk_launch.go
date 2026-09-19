package zded

import (
	"context"
	"errors"
	"log"

	"github.com/crispuscrew/zde/internal/manifest"
	"github.com/crispuscrew/zde/internal/zinc"
)

func (s *Server) startApps(target string) {
	all, _, err := s.desks.All()
	if err != nil {
		return // ensureDeclared already reported this one
	}
	d, ok := all[target]
	if !ok || len(d.Apps) == 0 {
		return
	}
	apps := d.Apps
	if !s.claimLaunch(target) {
		return
	}
	go func() {
		defer s.runs.Done()
		defer s.releaseLaunch(target)
		s.launchApps(s.runCtx, target, apps)
	}()
}

// claimLaunch permits one cancellable launch per declared desk, not per keypress.
// A desk's apps run sequentially; unrelated desks do not wait behind its subprocess.
func (s *Server) claimLaunch(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCtx.Err() != nil {
		return false
	}
	if _, up := s.launching[target]; up {
		return false
	}
	if s.launching == nil {
		s.launching = make(map[string]struct{})
	}
	s.launching[target] = struct{}{}
	s.runs.Add(1)
	return true
}

func (s *Server) releaseLaunch(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.launching, target)
}

func (s *Server) comingUp() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.launching)
}

// launchApps reports real failures once and ignores already-running apps or shutdown.
func (s *Server) launchApps(ctx context.Context, target string, apps []manifest.App) {
	var failed []launchFailure
	for _, app := range apps {
		if ctx.Err() != nil {
			return
		}
		address := zinc.Address(app.App, app.Instance)
		err := s.launch(ctx, address)
		if err == nil {
			continue
		}
		if errors.Is(err, zinc.ErrAlreadyRunning) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("zded: starting %s: %v", address, err)
		failed = append(failed, launchFailure{Address: address, Err: err})
	}
	s.launchesFailed(target, failed)
}
