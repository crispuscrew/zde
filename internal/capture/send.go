package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/clip"
	"github.com/crispuscrew/zde/internal/plainfile"
	"github.com/crispuscrew/zde/internal/zinc"
)

// Clipboard is the send-to target that reaches everything.
//
// It is a reserved name and not an app, because it is the only channel that
// crosses the sandbox. A zinc app sees the mounts its YAML declares and nothing
// else of this filesystem: there is no document portal, no file chooser, and
// `zcr run` takes the app's name and no app arguments, so a path appended to it
// is rejected rather than handed to the container. The Wayland clipboard is the
// session's rather than the container's - every app connected to niri can paste
// off it, sandboxed or not - which is why "screenshot to discord"
// ([`docs/vision.md`](../../docs/vision.md), G2) is this target and not the
// other one.
const Clipboard = "clipboard"

// Argv is what a send-to should run, or why nothing can.
//
// The target is a name from `zde.apps`, the same seam `zde app launch` resolves
// through, so a machine says what it means by "viewer" or "discord" in one place
// and the capture verb inherits it. The path goes on the end, which is the
// convention every program that opens a file has.
func Argv(all apps.Apps, target, path string) ([]string, error) {
	argv, err := all.Argv(target)
	if err != nil {
		return nil, err
	}
	if filepath.Base(argv[0]) == zinc.Runner {
		// Fail-closed, and the refusal is the useful half. zinc 0.10.1 has no
		// way to hand a running container a file and no way to give `zcr run`
		// an argument for the app: `zcr run -v` can add a mount only while the
		// container is created, there is no `zcr exec`, and nothing carries a
		// path or a descriptor in. So the alternative to this sentence is
		// starting the app with the file's name appended, watching zcr answer
		// "unexpected argument", and having spent a keypress on it.
		return nil, fmt.Errorf("%s runs in a zinc container, and %s is a path on the host: "+
			"%s takes no argument for the app and mounts nothing into one that is already up, "+
			"so the file would not be there. `zde capture send-to %s` is the channel that "+
			"crosses - the clipboard belongs to the session, not to the container",
			target, path, zinc.Runner, Clipboard)
	}
	return append(slices.Clone(argv), path), nil
}

// clipWait bounds one wl-copy. The same three seconds the clipboard history
// spends on one (internal/clip, within): far past a copy on a working machine,
// short enough that a dead one does not hold a keypress.
const clipWait = 3 * time.Second

// Clip puts a capture on the clipboard as an image, which is the one send-to
// that reaches an application zde cannot otherwise hand a file to.
//
// Read through internal/plainfile for the reason everything zde opens goes
// through it: a FIFO at that path parks the open in the kernel waiting for a
// writer that never comes, and this path can come off a command line.
//
// wl-copy's complaint is inherited as a descriptor rather than captured through
// a pipe. wl-copy forks a process to serve the selection and returns, so the fork
// outlives this call - it has to, or the image leaves the clipboard the moment
// zde exits. A pipe makes exec wait for every holder of it and a copy that worked
// reads as a call that never returns. Direct file descriptors have no copying
// goroutine, so stdin is the open capture and stderr still reaches the journal.
func Clip(path string) error {
	f, err := plainfile.OpenNoFollow(path)
	if err != nil {
		return err
	}
	defer f.Close()
	ctx, stop := context.WithTimeout(context.Background(), clipWait)
	defer stop()
	cmd := exec.CommandContext(ctx, clip.Copy, "--type", "image/png")
	cmd.Stdin = f
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s is not installed, so a capture cannot be put on the clipboard: %w", clip.Copy, err)
		}
		return fmt.Errorf("%s could not take %s: %w (its own message is in the journal)", clip.Copy, path, err)
	}
	return nil
}
