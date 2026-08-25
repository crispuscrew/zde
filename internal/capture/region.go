package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The region shot goes around niri's own picker, and this is the whole reason.
//
// niri renders three versions of every frame - Output, Screencast,
// ScreenCapture - and `block-out-from` decides which of them a window is black
// in. The rule is one line of niri 26.04 (src/render_helpers/mod.rs):
//
//	Some(BlockOutFrom::ScreenCapture) => self != RenderTarget::Output
//
// so `screen-capture` blacks a window out of everything except the frame that
// goes to the monitor. `screenshot-screen` and `screenshot-window` render
// ScreenCapture and are covered. The interactive screenshot UI saves
// `data.screenshot[0]`, which is the Output render (src/ui/screenshot_ui.rs),
// and is not - deliberately, and niri's own wiki says so: "This setting will
// still let you use the interactive built-in screenshot UI ... with an
// interactive selection, you can make sure that you avoid screenshotting
// sensitive content."
//
// That reasoning is niri's to make and it is not zde's: a window somebody
// blocked is blocked, and a region key that photographs it anyway is a promise
// this desktop did not keep. So the region shot takes its pixels through
// wlr-screencopy, which niri renders with RenderTarget::ScreenCapture
// (src/niri.rs, render_for_screencopy_with_damage and _without_damage) and which
// is therefore covered by the same rule.
//
// Measured rather than read, on a nested niri 26.04 with one window carrying the
// rule: with no rule the window is in `grim -g`'s output, with
// `block-out-from "screencast"` it is still there, and with
// `block-out-from "screen-capture"` the region comes back solid black - the same
// answer `screenshot-screen` gives to the same window.
//
// What it costs, because it is not free. niri's picker snaps to windows and
// outputs, freezes the screen while you choose, takes the keyboard, and can
// toggle the pointer into the shot; slurp is a drag on a live screen and none of
// those. Two programs go on the host that were not there before, against
// principle 1 (docs/vision.md). And niri put its own shots on the clipboard for
// nothing, where this has to spend a wl-copy.
//
// There is no falling back to niri's picker when these are missing. A fallback
// is the hole reopening on the machine least likely to notice.
const (
	// Selector draws the rectangle. layer-shell, which niri gates on the
	// security context, so this is a host program and not a sandboxed one.
	Selector = "slurp"
	// Grabber takes the pixels. wlr-screencopy, gated the same way.
	Grabber = "grim"
)

// geometry is slurp's output and grim's input, spelled out rather than left to
// slurp's default so that the two agree even if a slurp changes its mind.
const geometry = "%x,%y %wx%h"

// grabWait bounds the grab. The selection has no bound but the person's, and is
// given regionWait; a grab that has the rectangle is a read of one output.
const grabWait = 15 * time.Second

// RegionShot picks a rectangle and writes what is inside it.
//
// The same file discipline as the other two (claim), and the same rule at the
// end: what decides is the file. grim writes the whole PNG before it exits, so
// the fixed PNG end chunk must be present.
func RegionShot(ctx context.Context, dir string) (string, error) {
	return regionShot(ctx, dir, time.Now)
}

func regionShot(ctx context.Context, dir string, now func() time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("nowhere to put a capture: %w", err)
	}
	rect, err := selectRegion(ctx)
	if err != nil {
		return "", err
	}
	path, err := claim(dir, now())
	if err != nil {
		return "", err
	}
	if err := grab(ctx, rect, path); err != nil {
		os.Remove(path) //nolint:errcheck // nothing usable was written into it
		return "", err
	}
	if !completePNG(path) {
		os.Remove(path) //nolint:errcheck // incomplete or already gone
		return "", fmt.Errorf("%s said it captured %s, but the file is not a complete PNG", Grabber, rect)
	}
	return path, nil
}

// selectRegion is the rectangle, or why there is not one.
func selectRegion(ctx context.Context) (string, error) {
	ctx, stop := context.WithTimeout(ctx, regionWait)
	defer stop()
	cmd, said := guarded(ctx, Selector, "-f", geometry)
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return "", missing(Selector)
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("no region was chosen within %s", regionWait)
	}
	if err != nil {
		// slurp exits non-zero when the selection was cancelled, which is
		// Escape or a right-click and is the ordinary way out of it. Its own
		// line says which, and it is one line.
		if msg := firstLine(said.String()); msg != "" {
			return "", fmt.Errorf("no region captured: %s", msg)
		}
		return "", fmt.Errorf("no region captured: %s: %w", Selector, err)
	}
	rect := strings.TrimSpace(string(out))
	if rect == "" {
		return "", fmt.Errorf("%s chose nothing", Selector)
	}
	return rect, nil
}

// grab writes the rectangle to path.
func grab(ctx context.Context, rect, path string) error {
	ctx, stop := context.WithTimeout(ctx, grabWait)
	defer stop()
	cmd, said := guarded(ctx, Grabber, "-g", rect, path)
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return missing(Grabber)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s did not answer in %s", Grabber, grabWait)
	}
	if err != nil {
		if msg := firstLine(said.String()); msg != "" {
			return fmt.Errorf("%s: %s", Grabber, msg)
		}
		return fmt.Errorf("%s %s: %w", Grabber, rect, err)
	}
	return nil
}
