package zded

import (
	"errors"
	"testing"
	"time"
)

func TestFilmOffKeepsStateAndTimerWhenRemovalFails(t *testing.T) {
	server, _, _ := powerServer(t)
	t.Cleanup(func() { server.Close() })
	server.filmPath = filmTestPath(t)
	server.filmFor = time.Hour
	if _, err := server.setFilm(true); err != nil {
		t.Fatal(err)
	}
	timer := server.filmTimer
	server.removeFilm = func(string) error { return errors.New("state directory is read-only") }

	state, err := server.setFilm(false)
	if err == nil {
		t.Fatal("Film off succeeded without removing its persisted state")
	}
	if !state.Active || server.filmTimer != timer {
		t.Fatalf("failed Film off changed state to %+v or replaced its timer", state)
	}
	if _, present, readErr := readFilmState(server.filmPath); readErr != nil || !present {
		t.Fatalf("failed Film off lost persisted state: present=%v err=%v", present, readErr)
	}
	server.removeFilm = removeFilmState
	if _, err := server.setFilm(false); err != nil {
		t.Fatal(err)
	}
}

func TestVerifiedLockCannotRelockWhenFilmRemovalFails(t *testing.T) {
	path := filmTestPath(t)
	server, _, spy := powerServer(t)
	t.Cleanup(func() { server.Close() })
	until := time.Now().Add(-time.Second)
	if err := writeFilmState(path, until); err != nil {
		t.Fatal(err)
	}
	server.filmPath = path
	server.filmUntil = until
	server.filmTimer = time.AfterFunc(50*time.Millisecond, func() { server.expireFilm(until) })
	server.removeFilm = func(string) error { return errors.New("remove failed") }

	if resp := server.Dispatch(Request{Method: "system.lock"}); resp.Error != "" {
		t.Fatalf("verified lock: %s", resp.Error)
	}
	time.Sleep(100 * time.Millisecond)
	if state := server.filmState(); state.Active || state.Pending {
		t.Fatalf("verified lock left Film %+v", state)
	}
	if calls := len(spy.all()); calls != 1 {
		t.Fatalf("Film relocked %d times after its verified lock", calls)
	}

	restarted, _, restartedSpy := powerServer(t)
	t.Cleanup(func() { restarted.Close() })
	if err := restarted.restoreFilm(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if calls := len(restartedSpy.all()); calls != 0 {
		t.Fatalf("disabled Film state relocked %d times after daemon restart", calls)
	}
}
