package zded

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const lockReadyFor = 4 * time.Second
const lockerCommandFor = time.Second
const lockerUnit = "zde-lock.service"

type lockerControl struct {
	active func() (bool, error)
	start  func() error
	stop   func() error
}

func systemdLocker() lockerControl {
	return lockerControl{active: lockerUnitActive, start: startLocker, stop: stopLocker}
}

func startLocker() error {
	return runLockerCommand("systemctl", "--user", "start", lockerUnit)
}

func lockerUnitActive() (bool, error) {
	_, err := lockerOutput("systemctl", "--user", "is-active", "--quiet", lockerUnit)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
		return false, nil
	}
	return false, fmt.Errorf("reading %s state: %w", lockerUnit, err)
}

func stopLocker() error {
	return runLockerCommand("systemctl", "--user", "stop", lockerUnit)
}

func runLockerCommand(name string, args ...string) error {
	out, err := lockerOutput(name, args...)
	if err == nil {
		return nil
	}
	if line := strings.TrimSpace(string(out)); line != "" {
		return fmt.Errorf("%w: %s", err, line)
	}
	return err
}

func lockerOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lockerCommandFor)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("%s timed out after %s: %w", name, lockerCommandFor, ctx.Err())
	}
	return out, err
}

func (s *Server) lockScreen() Response {
	return s.lockScreenAfter(nil)
}

func (s *Server) lockScreenAfter(prepare func() string) Response {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()

	logind, err := s.logins()
	if err != nil {
		return Response{Error: "cannot verify that the screen locked: " + err.Error()}
	}
	locked, err := logind.Locked()
	if err != nil {
		return Response{Error: "cannot read this session's LockedHint before locking: " + err.Error()}
	}
	locker := s.locker
	if locker.active == nil {
		locker = systemdLocker()
	}
	if locked {
		active, activeErr := locker.active()
		if activeErr != nil {
			return Response{Error: "this session is locked, but its managed locker cannot be verified: " + activeErr.Error()}
		}
		if active {
			return s.clearPendingFilm(ok("already locked"))
		}
		return Response{Error: "this session already reports LockedHint=true, so no fresh lock transition can be verified"}
	}
	note := ""
	if prepare != nil {
		if _, err := locker.active(); err != nil {
			return Response{Error: "nothing to lock the screen with, so nothing was switched either: " + err.Error()}
		}
		note = prepare()
	}
	if err := locker.start(); err != nil {
		return Response{Error: "starting the locker: " + err.Error()}
	}
	cleanup, err := s.awaitLocked(logind, locker)
	if err != nil {
		if cleanup && locker.stop != nil {
			locker.stop() //nolint:errcheck // the readiness error is the useful one
		}
		return Response{Error: err.Error()}
	}
	resp := ok("locked")
	resp.Note = note
	return s.clearPendingFilm(resp)
}

func (s *Server) awaitLocked(logind interface{ Locked() (bool, error) }, locker lockerControl) (bool, error) {
	within := s.lockWait
	if within <= 0 {
		within = lockReadyFor
	}
	timer := time.NewTimer(within)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		active, err := locker.active()
		if err != nil {
			return false, fmt.Errorf("reading the locker unit after it started: %w", err)
		}
		if !active {
			return false, errors.New("locker exited before Niri reported the session locked")
		}
		locked, err := logind.Locked()
		if err != nil {
			return false, fmt.Errorf("reading LockedHint after the locker started: %w", err)
		}
		if locked {
			return false, nil
		}
		select {
		case <-tick.C:
		case <-timer.C:
			return true, fmt.Errorf("Niri did not report a fresh LockedHint transition within %s", within)
		}
	}
}
