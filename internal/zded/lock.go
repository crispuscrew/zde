package zded

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/plainfile"
)

// lock-preset: switch to a configured desk, then lock (docs/vision.md, W23;
// docs/model.md, section 6).
//
// The order is the whole feature. What is on the screen when the lock takes it
// is what a shoulder reads at the lock screen and what an unlock puts back, so
// the switch has to have happened before the locker starts - not been asked
// for.

// lockFile is layer 1's answer to which desk an unlock should show, beside its
// answers to what this machine calls a terminal and what it runs for an ask
// tier (nix/home.nix). One directory for zde's config, one reader each.
const lockFile = "lock.json"

// lockBytesMax bounds what is read at that path. This file is one desk name;
// anything at kilobyte scale arrived there by accident and is refused rather
// than parsed.
const lockBytesMax = 64 << 10

// lockConfig is that file.
type lockConfig struct {
	// Preset is the desk lock-preset switches to. Empty is a machine that has
	// not set one, which is not an error: it locks.
	Preset string `json:"preset"`
}

func readLockConfig() (lockConfig, error) {
	var c lockConfig
	data, err := plainfile.Read(apps.Path(lockFile), lockBytesMax)
	if err != nil {
		// A missing file is a machine where nothing is configured, the way a
		// missing apps.json is (internal/apps, Load). Every other failure is
		// worth saying, and none of them stops the lock.
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", apps.Path(lockFile), err)
	}
	return c, nil
}

// lockPreset switches to the preset desk and locks.
//
// Three things can go wrong with the switch and none of them stops the lock:
// nothing configured, a preset naming a desk that is not there, and a preset
// naming a private desk. Locking the screen is what this action is for, and a
// key that refused to lock because a desk was missing would be a screen left
// open over a configuration mistake. Each of them comes back as a note beside
// the answer, so the difference between "it switched" and "it locked where you
// were" is readable rather than guessed at.
//
// The one thing that does stop it is having nothing to lock with, and that is
// checked before anything moves. A machine with no locker configured would
// otherwise be walked away from a desk and left unlocked on another one, which
// is worse than the key doing nothing at all.
func (s *Server) lockPreset() Response {
	if err := s.canLock(); err != nil {
		return Response{Error: err.Error()}
	}
	note := s.switchToPreset()
	if resp := s.runAction("system.lock"); resp.Error != "" {
		return resp
	}
	resp := ok("locking")
	resp.Note = note
	return resp
}

// canLock is whether this machine has a locker at all, asked without running
// anything.
//
// The same table `zde system lock` resolves against and the same name, because
// a second idea of what locks this screen is how a machine ends up with a key
// that locks and a menu that does not (power.go, powerRun). What this cannot
// see is a locker that is configured and fails when it starts: the lock is a
// spawn, and zded learns only that it began.
func (s *Server) canLock() error {
	all, err := apps.Load(apps.DefaultPath())
	if err != nil {
		return err
	}
	if _, err := all.Argv("lock"); err != nil {
		return fmt.Errorf("nothing to lock the screen with, so nothing was switched either: %w", err)
	}
	return nil
}

// switchToPreset moves to the configured desk, and answers with what it could
// not do rather than with an error, for the reason lockPreset gives.
//
// It returns only once the switch has happened. switchDesk focuses every
// monitor's workspace over niri's socket and answers when niri has taken them
// (server.go, switchFrom), so "switched" is a fact by the time this returns and
// not a request in flight - which is what lets the lock come after it.
func (s *Server) switchToPreset() string {
	cfg, err := readLockConfig()
	if err != nil {
		return "locking where you were: " + err.Error()
	}
	if cfg.Preset == "" {
		// The option by name, which is the whole of failing loudly: "no preset"
		// leaves somebody reading three documents, and this is a line they can
		// paste into a config.
		return fmt.Sprintf("locking where you were: no preset desk is set, so set zde.lock.preset "+
			"in your home-manager config, which is what writes %s", apps.Path(lockFile))
	}
	if d := s.manifestFor(cfg.Preset); d != nil && d.Private {
		// A private desk is out of the picker, popups off, capture-blocked
		// (docs/vision.md, section 3). Switching away from one is what this
		// action is for; switching to one would put the desk with the most to
		// hide on the screen an unlock reveals.
		return "locking where you were: " + cfg.Preset + " declares private, and a private desk is not something to unlock onto"
	}
	if resp := s.switchDesk(cfg.Preset); resp.Error != "" {
		// A preset naming a desk that is gone. Named, because the thing to do
		// about it is to fix the name or make the desk, and neither is
		// discoverable from a screen that just locked.
		return "locking where you were: " + resp.Error
	}
	return ""
}
