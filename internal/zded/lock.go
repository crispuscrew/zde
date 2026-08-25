package zded

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/attn"
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
// tier (nix/home.nix). One directory for zde's config, one file per reader
// (config.go).
const lockFile = "lock.json"

// lockConfig is that file.
type lockConfig struct {
	// Preset is the desk lock-preset switches to. Empty is a machine that has
	// not set one, which is not an error: it locks.
	Preset string `json:"preset"`
}

// lockPreset switches to the preset desk and locks.
//
// Nothing that goes wrong with the switch stops the lock: no preset set, a
// preset naming a desk that is not there or a private one, a lock.json or a
// manifest that will not parse. Locking the screen is what this action is for,
// and a key that refused to lock because a desk was missing would be a screen
// left open over a configuration mistake. Each of them comes back as a note
// beside the answer, so the difference between "it switched" and "it locked
// where you were" is readable rather than guessed at.
//
// Locker resolution and the initial LockedHint reading happen before anything
// moves. A machine that cannot begin a verifiable lock would otherwise be walked
// away from a desk and left unlocked on another one, which is worse than the key
// doing nothing at all. Process start and the final transition can still fail
// after the switch; those cannot be known before it.
func (s *Server) lockPreset() Response {
	return s.lockScreenAfter(s.switchToPreset)
}

// switchToPreset moves to the configured desk, and answers with what it could
// not do rather than with an error, for the reason lockPreset gives.
//
// It returns only once the switch has happened. switchDesk focuses every
// monitor's workspace over niri's socket and answers when niri has taken them
// (server.go, switchFrom), so "switched" is a fact by the time this returns and
// not a request in flight - which is what lets the lock come after it.
func (s *Server) switchToPreset() string {
	var cfg lockConfig
	if err := readConfig(lockFile, &cfg); err != nil {
		return "locking where you were: " + err.Error()
	}
	if cfg.Preset == "" {
		// The option by name, which is the whole of failing loudly: "no preset"
		// leaves somebody reading three documents, and this is a line they can
		// paste into a config.
		return fmt.Sprintf("locking where you were: no preset desk is set, so set zde.lock.preset "+
			"in your home-manager config, which is what writes %s", apps.Path(lockFile))
	}
	// Whether this is a desk to put on a screen somebody else is looking at,
	// which is the same question panic asks of its decoy and fails closed the
	// same way (config.go, canShow). The name in the answer is filtered there;
	// the switch below gets the string as written, so a name that had to be
	// filtered is one niri is asked for and does not have.
	if err := s.canShow(cfg.Preset); err != nil {
		return "locking where you were: " + err.Error()
	}
	if resp := s.switchDesk(cfg.Preset); resp.Error != "" {
		// A preset naming a desk that is gone. Named, because the thing to do
		// about it is to fix the name or make the desk, and neither is
		// discoverable from a screen that just locked.
		return "locking where you were: " + attn.Line(resp.Error)
	}
	return ""
}
