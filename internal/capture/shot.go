package capture

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Shot takes one of niri's two automatic screenshots into a file this package
// names, and answers with the path or with what stopped it.
//
// Both of these render niri's ScreenCapture frame, so a window carrying
// `block-out-from "screen-capture"` is already black in them and nothing here
// has to do anything about it. The region is the one that is not, and it goes
// another way entirely (region.go).
//
// The directory is made first because niri does not make one for an explicit
// path. The event subscription comes before the request because the event is
// sent once. The public name is reserved separately from the private staging
// path, so a timeout cannot publish a fragment or let a late writer recreate a
// public file with the session umask.
func Shot(s Shooter, k Kind, dir string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("nowhere to put a capture: %w", err)
	}
	captured, err := s.Captured()
	if err != nil {
		return "", fmt.Errorf("cannot hear what niri does with a capture: %w", err)
	}
	path, pending, err := claimPending(dir, now)
	if err != nil {
		return "", err
	}
	if err := ask(s, k, pending); err != nil {
		// A complete refusal means no writer exists. A transport error does not
		// say whether niri accepted the action, so keep the private staging file
		// in case its writer is already running.
		os.Remove(path) //nolint:errcheck // only the empty reservation is removed
		var refusal interface{ Refused() bool }
		if errors.As(err, &refusal) && refusal.Refused() {
			os.Remove(pending) //nolint:errcheck // niri confirmed it did not start a writer
		}
		return "", err
	}
	return settle(captured, pending, path, k, promptWait)
}

func ask(s Shooter, k Kind, path string) error {
	if k == Window {
		return s.ScreenshotWindow(path)
	}
	return s.ScreenshotScreen(path)
}

// settle needs both signals: niri's exact-path event says its asynchronous
// writer is done, and the PNG suffix says the write reached the end. A null
// event is global and names no request, so it cannot confirm this one.
func settle(captured <-chan string, pending, path string, k Kind, wait time.Duration) (string, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	var confirmed bool
	for waiting := true; waiting; {
		select {
		case got, open := <-captured:
			switch {
			case !open:
				waiting = false
			case got == pending:
				confirmed, waiting = true, false
			}
		case <-deadline.C:
			waiting = false
		}
	}
	if confirmed && completePNG(pending) {
		if err := os.Rename(pending, path); err != nil {
			os.Remove(path) //nolint:errcheck // the reservation is not a capture
			return "", fmt.Errorf("publish capture %s: %w", path, err)
		}
		return path, nil
	}
	os.Remove(path) //nolint:errcheck // only the empty reservation is removed
	if confirmed {
		// No writer remains after the matching event. Without it, the staging
		// path stays private for any asynchronous writer that has not opened it.
		os.Remove(pending) //nolint:errcheck // incomplete and no writer remains
		return "", fmt.Errorf("niri said it wrote %s, but the file is not a complete PNG", path)
	}
	return "", fmt.Errorf("niri took a %s capture but did not confirm %s within %s", k, path, wait)
}
