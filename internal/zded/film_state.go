package zded

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/crispuscrew/zde/internal/plainfile"
)

const filmStateMax = 4 << 10

func readFilmState(path string) (string, bool, error) {
	if err := checkFilmDir(filepath.Dir(path)); err != nil {
		return "", false, err
	}
	raw, err := plainfile.ReadPrivateNoFollow(path, filmStateMax)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var state struct {
		Until string `json:"until"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return "", false, err
	}
	if state.Until == "" {
		return "", false, fmt.Errorf("Film state has no deadline")
	}
	return state.Until, true, nil
}

func writeFilmState(path string, until time.Time) error {
	if path == "" {
		return nil
	}
	if err := checkFilmDir(filepath.Dir(path)); err != nil {
		return err
	}
	raw, err := json.Marshal(struct {
		Until string `json:"until"`
	}{Until: until.Format(time.RFC3339)})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".film-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(append(raw, '\n')); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func removeFilmState(path string) error {
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func checkFilmDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("Film state directory ownership is unavailable")
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("Film state directory must be an owner-controlled 0700 directory, not %s uid %d", info.Mode(), stat.Uid)
	}
	return nil
}

func (s *Server) restoreFilm(path string) error {
	s.filmMu.Lock()
	s.filmPath = path
	untilText, present, err := readFilmState(path)
	if err != nil {
		s.restoreExpiredFilmLocked()
		s.filmMu.Unlock()
		s.broadcast(Event{Kind: EventFilm})
		return err
	}
	if !present {
		s.filmMu.Unlock()
		return nil
	}
	until, parseErr := time.Parse(time.RFC3339, untilText)
	if parseErr != nil || until.After(time.Now().Add(filmMaximum)) {
		s.restoreExpiredFilmLocked()
		s.filmMu.Unlock()
		s.broadcast(Event{Kind: EventFilm})
		return fmt.Errorf("invalid Film deadline %q", untilText)
	}
	if until.IsZero() {
		s.filmMu.Unlock()
		return nil
	}
	s.filmUntil = until
	wait := max(time.Until(until), 0)
	s.filmTimer = time.AfterFunc(wait, func() { s.expireFilm(until) })
	s.filmMu.Unlock()
	s.broadcast(Event{Kind: EventFilm})
	return nil
}

func (s *Server) restoreExpiredFilmLocked() {
	until := time.Now()
	s.filmUntil = until
	s.filmTimer = time.AfterFunc(0, func() { s.expireFilm(until) })
}

func (s *Server) closeFilm() {
	s.filmMu.Lock()
	defer s.filmMu.Unlock()
	if s.filmTimer != nil {
		s.filmTimer.Stop()
	}
	s.filmTimer = nil
	s.filmClosed = true
}
