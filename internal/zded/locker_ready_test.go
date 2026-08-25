package zded

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

type deadlineRecorder struct {
	net.Conn
	deadline time.Time
}

func (rec *deadlineRecorder) SetDeadline(deadline time.Time) error {
	rec.deadline = deadline
	return errors.New("stop before writing")
}

func TestLockProducingClientCallsCoverTheReadinessWindow(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		args   []string
		want   time.Duration
	}{
		{"ordinary", "system.film", nil, 5 * time.Second},
		{"lock", "system.lock", nil, 15 * time.Second},
		{"lock preset", "system.lock-preset", nil, 15 * time.Second},
		{"idle lock", "system.idle", []string{"lock"}, 15 * time.Second},
		{"idle display", "system.idle", []string{"display-off"}, 5 * time.Second},
		{"suspend refusal", "system.power", []string{"suspend"}, 5 * time.Second},
		{"reboot", "system.power", []string{"reboot"}, 5 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := &deadlineRecorder{}
			client := &Client{conn: rec}
			if err := client.Call(test.method, nil, test.args...); err == nil {
				t.Fatal("Call continued after the deadline probe stopped it")
			}
			remaining := time.Until(rec.deadline)
			if remaining < test.want-time.Second || remaining > test.want {
				t.Errorf("deadline is %s away, want about %s", remaining, test.want)
			}
		})
	}
}

func TestLockFailsWhenTheLockerExitsBeforeLockedHint(t *testing.T) {
	s, _, _ := powerServer(t)
	s.locker.start = func() error { return nil }
	s.locker.active = func() (bool, error) { return false, nil }

	resp := s.Dispatch(Request{Method: "system.lock"})
	if !strings.Contains(resp.Error, "exited before") {
		t.Fatalf("lock answered %q, want the locker exit", resp.Error)
	}
}

func TestLockFailsClosedWhenLockedHintDoesNotChange(t *testing.T) {
	s, _, _ := powerServer(t)
	s.lockWait = 30 * time.Millisecond
	stopped := false
	s.locker.start = func() error { return nil }
	s.locker.active = func() (bool, error) { return true, nil }
	s.locker.stop = func() error { stopped = true; return nil }

	resp := s.Dispatch(Request{Method: "system.lock"})
	if !strings.Contains(resp.Error, "fresh LockedHint") {
		t.Fatalf("lock answered %q, want the readiness timeout", resp.Error)
	}
	if !stopped {
		t.Error("the unready locker was left running")
	}
}

func TestAStaleLockedHintDoesNotCountAsThisLock(t *testing.T) {
	s, logind, spy := powerServer(t)
	logind.locked = true
	logind.lockedSticky = true

	resp := s.Dispatch(Request{Method: "system.lock"})
	if !strings.Contains(resp.Error, "no fresh lock transition") {
		t.Fatalf("lock answered %q for stale LockedHint=true", resp.Error)
	}
	if len(spy.all()) != 0 {
		t.Error("a second locker was started against an already locked session")
	}
}

func TestManagedLockSurvivesDaemonRestartWithoutStartingAnother(t *testing.T) {
	s, logind, spy := powerServer(t)
	logind.locked = true
	logind.lockedSticky = true
	s.locker.active = func() (bool, error) { return true, nil }
	stopped := false
	s.locker.stop = func() error { stopped = true; return nil }

	resp := s.Dispatch(Request{Method: "system.lock"})
	if resp.Error != "" {
		t.Fatalf("managed already-locked session: %s", resp.Error)
	}
	if len(spy.all()) != 0 {
		t.Error("a daemon restart started a second locker against its active unit")
	}
	if stopped {
		t.Error("a daemon restart stopped the active locker")
	}
}

func TestLockerObservationErrorDoesNotStopAPotentialLock(t *testing.T) {
	s, _, _ := powerServer(t)
	stopped := false
	s.locker.start = func() error { return nil }
	s.locker.active = func() (bool, error) { return false, errors.New("user manager did not answer") }
	s.locker.stop = func() error { stopped = true; return nil }

	resp := s.Dispatch(Request{Method: "system.lock"})
	if !strings.Contains(resp.Error, "did not answer") {
		t.Fatalf("lock answered %q, want the observation error", resp.Error)
	}
	if stopped {
		t.Error("an indeterminate observation stopped a potentially working locker")
	}
}

func TestLockedHintObservationErrorDoesNotStopAPotentialLock(t *testing.T) {
	s, logind, _ := powerServer(t)
	stopped := false
	s.locker.start = func() error { return nil }
	s.locker.active = func() (bool, error) { return true, nil }
	s.locker.stop = func() error { stopped = true; return nil }
	logind.lockedErr = errors.New("session bus did not answer")
	logind.lockedErrAt = 2

	resp := s.Dispatch(Request{Method: "system.lock"})
	if !strings.Contains(resp.Error, "did not answer") {
		t.Fatalf("lock answered %q, want the LockedHint error", resp.Error)
	}
	if stopped {
		t.Error("an indeterminate LockedHint stopped a potentially working locker")
	}
}
