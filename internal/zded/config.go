package zded

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/plainfile"
)

// What layer 1 writes into zde's config directory, and the one rule the two
// desk names in it share (nix/home.nix).
//
// One directory, one small file per reader: apps.json is what this machine
// calls a terminal, ask.json what it runs for a tier, lock.json which desk an
// unlock shows, panic.json which desk panic switches to. What they have in
// common is not what they say but their shape - a JSON object a home-manager
// switch wrote - and, for the two that name a desk, that the desk is put in
// front of somebody else.

// configBytesMax bounds what is read at one of those paths. Each of these files
// is a name or two; anything at kilobyte scale arrived there by accident and is
// refused rather than parsed.
const configBytesMax = 64 << 10

// readConfig fills v from one of those files.
//
// A missing file is a machine where nothing is configured, the way a missing
// apps.json is (internal/apps, Load), and it is not an error: home-manager
// writes these even when the option is empty, and a zde built by hand has
// neither. Every other failure is worth saying, and what each caller does about
// it is the caller's - lock-preset locks anyway, panic refuses.
func readConfig(file string, v any) error {
	data, err := plainfile.Read(apps.Path(file), configBytesMax)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", apps.Path(file), err)
	}
	return nil
}

// canShow is whether a desk named in one of those files is one zde may put on
// the screen, asked without moving anything.
//
// Two actions name a desk in a config file and then switch to it with somebody
// else looking: lock-preset, which decides what a shoulder reads over the lock
// screen and what an unlock reveals (lock.go), and panic, which decides what a
// person who has just walked in sees (panic.go). A private desk is out of the
// picker, popups off, capture-blocked (docs/vision.md, section 3): switching
// away from one is what both actions are for, and switching to one would put
// the desk with the most to hide on exactly that screen.
//
// Read here rather than through manifestFor, because that one drops the error,
// and a nil from it means both "no manifest" and "the manifests could not be
// read". Those are opposite answers to this question: a desk niri has and
// nothing declares cannot be private, and a directory nobody can read says
// nothing about the desk either way. A check that allowed the switch on the
// second is not a check - neither action needs a manifest, so the switch would
// go through, on exactly the machine where nobody can see that it did. Fail
// closed, the way an arrival on an unreadable desk does (history.go,
// privateArrival; docs/vision.md, principle 9).
//
// Every name in the answer goes through attn.Line. Both callers put it on a
// terminal, and the desk name came out of a config file and the problem out of
// a manifest an agent may have authored (docs/model.md, section 5).
func (s *Server) canShow(name string) error {
	named := attn.Line(name)
	all, problems, err := s.desks.All()
	if err != nil {
		return fmt.Errorf("the desk manifests could not be read, so nothing here can say whether %s is private: %s",
			named, attn.Line(err.Error()))
	}
	s.rememberProblems(problems)
	if len(problems) > 0 {
		return fmt.Errorf("a desk manifest will not parse, so nothing here can say whether %s is private: %s",
			named, attn.Line(problems[0].String()))
	}
	if d := all[name]; d != nil && d.Private {
		return fmt.Errorf("%s declares private, and a private desk is not one to put in front of somebody else", named)
	}
	return nil
}
