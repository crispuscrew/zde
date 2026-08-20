package zded

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
)

// Sound is the machine's output, as panic needs it: whether it is off already,
// and turning it off or on (panic.go).
//
// zde has no audio of its own. The mixer, the per-app volume and the device
// switch are 0.2's media work (docs/roadmap.md), and until they land every
// audio key on this machine spawns wpctl (internal/keymap, the audio group),
// which layer 1 installs with wireplumber (nix/home.nix). So this is one verb
// over the same program and not an audio layer to be replaced later by a
// different idea of what mutes this machine.
//
// What it reaches is the default sink, which is what @DEFAULT_AUDIO_SINK@
// names: the speakers the machine is playing out of. An app pointed at a second
// sink by hand keeps playing, and nothing here says otherwise - reaching one
// app's stream is the mixer's job, and the answers below say what was muted
// rather than that the machine has gone quiet.
type Sound interface {
	// Muted is whether the output is off already. Its error is "cannot tell",
	// which is a third answer and not a false: panic gives back only what it
	// took, and that turns on knowing what it found (panic.go, panicHold).
	Muted() (bool, error)
	// Mute puts it either way. Written out rather than toggled, unlike the key:
	// panic pressed on a machine that is already muted must leave it muted, and
	// a toggle there is sound coming back on in the room somebody just walked
	// into.
	Mute(on bool) error
}

// Wpctl is that, over the program.
//
// Exported because cmd/zded is what puts a real one on the daemon (UseSound),
// for the reason the clipboard is wired there and not defaulted: this one runs
// a program against whatever PipeWire is on the machine, and a Server built by
// a test would otherwise mute the speakers of whoever is running the tests.
type Wpctl struct{}

// audioSink is the sink every audio key already names.
const audioSink = "@DEFAULT_AUDIO_SINK@"

// audioWait bounds one of those calls. wpctl talks to a PipeWire on the same
// machine over a local socket and answers in milliseconds; the bound is there
// because this is called from a keypress, and a wedged PipeWire must cost the
// panic key a moment rather than the whole action.
const audioWait = 2 * time.Second

func (Wpctl) Mute(on bool) error {
	state := "0"
	if on {
		state = "1"
	}
	_, err := wpctl("set-mute", audioSink, state)
	return err
}

func (Wpctl) Muted() (bool, error) {
	out, err := wpctl("get-volume", audioSink)
	if err != nil {
		return false, err
	}
	// `Volume: 0.65 [MUTED]` is the line wpctl prints, and the mark is the whole
	// of what is read: the level is not this file's question, and a volume of
	// zero is not a mute - somebody who turned it down still has a machine that
	// makes a noise when the next key turns it up.
	return strings.Contains(string(out), "[MUTED]"), nil
}

// wpctl runs one of those and answers with what it printed.
//
// The program's own words where it wrote any, because the ways this fails are
// ones a person has to be told about and they are all different: no wpctl on
// the daemon's PATH, a session with no PipeWire, a machine with no sound card
// at all - which is every VM, and the answer says which. Filtered through
// attn.Line for the reason every foreign line in zde is: this reaches a
// terminal.
func wpctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), audioWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wpctl", args...).Output()
	if err == nil {
		return out, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
		return nil, fmt.Errorf("wpctl %s: %s", args[0], attn.Line(string(exit.Stderr)))
	}
	return nil, fmt.Errorf("wpctl %s: %w", args[0], err)
}
