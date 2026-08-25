package zded

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func filmOf(t *testing.T, resp Response) Film {
	t.Helper()
	if resp.Error != "" {
		t.Fatalf("Film: %s", resp.Error)
	}
	var state Film
	if err := json.Unmarshal(resp.Ok, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestFilmSuppressesOnlyAutomaticIdleActions(t *testing.T) {
	s, _, spy := powerServer(t)
	t.Cleanup(func() { s.Close() })
	film := filmOf(t, s.Dispatch(Request{Method: "system.film", Args: []string{"on"}}))
	if !film.Active || film.Until == "" {
		t.Fatalf("Film on = %+v", film)
	}
	if resp := s.Dispatch(Request{Method: "system.idle", Args: []string{"lock"}}); resp.Error != "" {
		t.Fatalf("idle lock during Film: %s", resp.Error)
	}
	if resp := s.Dispatch(Request{Method: "system.idle", Args: []string{"display-off"}}); resp.Error != "" {
		t.Fatalf("idle display-off during Film: %s", resp.Error)
	}
	if len(spy.all()) != 0 {
		t.Error("Film allowed Hypridle to start the locker")
	}
	if calls := s.niri.(*fakeCompositor).performCalls(); len(calls) != 0 {
		t.Errorf("Film allowed Hypridle to perform %v", calls)
	}
}

func TestFilmExpiryLocksAndClearsTheVisibleState(t *testing.T) {
	s, _, spy := powerServer(t)
	t.Cleanup(func() { s.Close() })
	s.filmFor = 30 * time.Millisecond
	if _, err := s.setFilm(true); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(time.Second)
	for len(spy.all()) == 0 {
		select {
		case <-deadline:
			t.Fatal("Film expired without locking")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if state := s.filmState(); state.Active || state.Pending {
		t.Errorf("Film stayed visible after its verified lock: %+v", state)
	}
}

func TestDisplayPowerOffNeedsBatteryOrOLED(t *testing.T) {
	s, _, _ := powerServer(t)
	compositor := s.niri.(*fakeCompositor)
	dir := t.TempDir()
	s.laptopPath = filepath.Join(dir, "laptop")

	if resp := s.idleAction("display-off"); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if len(compositor.performCalls()) != 0 {
		t.Error("a desktop display was powered off without the OLED opt-in")
	}
	if err := os.WriteFile(s.laptopPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if resp := s.idleAction("display-off"); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if got := compositor.performCalls(); len(got) != 1 || got[0] != "power-off-monitors" {
		t.Errorf("declared laptop display-off performed %v", got)
	}
	if err := os.Remove(s.laptopPath); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "zde", idleFile), idleConfig{OLED: true})
	if resp := s.idleAction("display-off"); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if got := compositor.performCalls(); len(got) != 2 || got[1] != "power-off-monitors" {
		t.Errorf("OLED display-off performed %v", got)
	}
}

func TestFilmLockPendingKeepsTheDisplayOn(t *testing.T) {
	s, _, _ := powerServer(t)
	s.filmUntil = time.Now()
	if resp := s.idleAction("display-off"); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if calls := s.niri.(*fakeCompositor).performCalls(); len(calls) != 0 {
		t.Errorf("Film lock-pending powered the display off with %v", calls)
	}
}

func TestVerifiedLocksCancelPendingFilmRetry(t *testing.T) {
	for _, test := range []struct {
		name string
		req  Request
	}{
		{"explicit", Request{Method: "system.lock"}},
		{"idle", Request{Method: "system.idle", Args: []string{"lock"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _, spy := powerServer(t)
			t.Cleanup(func() { server.Close() })
			server.filmPath = filmTestPath(t)
			until := time.Now().Add(-time.Second)
			if err := writeFilmState(server.filmPath, until); err != nil {
				t.Fatal(err)
			}
			server.filmUntil = until
			server.filmTimer = time.AfterFunc(80*time.Millisecond, func() { server.expireFilm(until) })

			if resp := server.Dispatch(test.req); resp.Error != "" {
				t.Fatalf("verified lock: %s", resp.Error)
			}
			if state := server.filmState(); state.Active || state.Pending {
				t.Fatalf("verified lock left Film %+v", state)
			}
			time.Sleep(120 * time.Millisecond)
			if calls := len(spy.all()); calls != 1 {
				t.Errorf("pending Film relocked %d times after unlock, want one lock", calls)
			}
		})
	}
}
