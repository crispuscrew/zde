package zded

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestFilmDeadlineSurvivesDaemonRestart(t *testing.T) {
	path := filmTestPath(t)
	first, _, _ := powerServer(t)
	first.filmPath = path
	first.filmFor = time.Hour
	if _, err := first.setFilm(true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("Film state mode = %04o, want 0600", info.Mode().Perm())
	}
	first.closeFilm()

	second, _, _ := powerServer(t)
	t.Cleanup(func() { second.Close() })
	if err := second.restoreFilm(path); err != nil {
		t.Fatal(err)
	}
	if state := second.filmState(); !state.Active || state.Pending {
		t.Fatalf("restored Film = %+v", state)
	}
}

func TestExpiredRestoredFilmLocksImmediately(t *testing.T) {
	path := filmTestPath(t)
	until := time.Now().Add(-time.Minute)
	if err := writeFilmState(path, until); err != nil {
		t.Fatal(err)
	}
	server, _, spy := powerServer(t)
	t.Cleanup(func() { server.Close() })
	if err := server.restoreFilm(path); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, time.Second, func() bool { return len(spy.all()) == 1 })
	if film := server.filmState(); film.Active || film.Pending {
		t.Fatalf("expired restored Film remained %+v after locking", film)
	}
}

func TestFilmExpiryKeepsVisiblePendingStateUntilRetryLocks(t *testing.T) {
	server, _, spy := powerServer(t)
	t.Cleanup(func() { server.Close() })
	server.filmPath = filmTestPath(t)
	server.filmFor = 20 * time.Millisecond
	server.filmRetry = 80 * time.Millisecond
	originalStart := server.locker.start
	var fail atomic.Bool
	fail.Store(true)
	server.locker.start = func() error {
		if fail.Load() {
			return errors.New("locker unavailable")
		}
		return originalStart()
	}
	if _, err := server.setFilm(true); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, time.Second, func() bool {
		untilText, present, err := readFilmState(server.filmPath)
		until, parseErr := time.Parse(time.RFC3339, untilText)
		return err == nil && parseErr == nil && present && !time.Now().Before(until)
	})
	persisted, present, err := readFilmState(server.filmPath)
	if err != nil || !present || persisted == "" {
		t.Fatalf("pending Film deadline on disk = %q, %v, %v", persisted, present, err)
	}
	fail.Store(false)
	waitUntil(t, time.Second, func() bool {
		_, err := os.Stat(server.filmPath)
		return len(spy.all()) == 1 && os.IsNotExist(err)
	})
}

func TestFilmStateRefusesLinksAndLooseModes(t *testing.T) {
	dir := filepath.Dir(filmTestPath(t))
	path := filepath.Join(dir, "film.json")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("not Film"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readFilmState(path); err == nil {
		t.Fatal("Film state followed a symlink")
	}
	if err := writeFilmState(path, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "not Film" {
		t.Fatalf("Film write followed the symlink and changed target to %q", raw)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readFilmState(path); err == nil {
		t.Fatal("Film state accepted a non-private mode")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readFilmState(path); err == nil {
		t.Fatal("Film state accepted a non-private directory")
	}
}

func filmTestPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "zde")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "film.json")
}

func waitUntil(t *testing.T, within time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true before deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
