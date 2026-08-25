package zded

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestObservedLockIsNotRecheckedIntoFailure(t *testing.T) {
	server, logind, _ := powerServer(t)
	activeReads := 0
	server.locker.start = func() error { logind.setLocked(true); return nil }
	server.locker.active = func() (bool, error) {
		activeReads++
		if activeReads > 1 {
			return false, errors.New("unit vanished after lock observation")
		}
		return true, nil
	}

	if resp := server.Dispatch(Request{Method: "system.lock"}); resp.Error != "" {
		t.Fatalf("observed lock failed as %q", resp.Error)
	}
	if activeReads != 1 {
		t.Errorf("locker activity was read %d times after start, want no recheck after LockedHint", activeReads)
	}
}

func TestLockPresetChecksReadinessBeforeSwitching(t *testing.T) {
	server, compositor, spy := lockServer(t, "haven", nil)
	logind := &fakeLogind{locked: true, lockedSticky: true}
	withLogind(server, logind, nil)

	resp := server.Dispatch(Request{Method: "system.lock-preset"})
	if !strings.Contains(resp.Error, "no fresh lock transition") {
		t.Fatalf("lock-preset answered %q for stale LockedHint=true", resp.Error)
	}
	if calls := compositor.focusCalls(); len(calls) != 0 {
		t.Errorf("lock-preset moved to %v before readiness preflight failed", calls)
	}
	if calls := spy.all(); len(calls) != 0 {
		t.Errorf("lock-preset started %v after readiness preflight failed", calls)
	}
}

func TestLockerCommandsHaveAProcessDeadline(t *testing.T) {
	started := time.Now()
	_, err := lockerOutput("sleep", "10")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow locker command returned %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*lockerCommandFor {
		t.Errorf("slow locker command returned after %s", elapsed)
	}
}
