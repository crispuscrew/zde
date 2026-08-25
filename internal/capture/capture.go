// Package capture is what happens to a picture of the screen after the
// compositor has taken it.
//
// niri takes the picture. It has had screenshots since long before zde did, it
// owns the region picker, and a second screenshotter would be a second thing to
// be wrong about colour spaces and cursors. What niri has no opinion about is
// everything after the shutter, and that is this package: which file the image
// lands in, who can read it, what happens when two shots want the same name, and
// what a person is told when nothing was written.
//
// The last one is why zde asks for the shot at all rather than leaving the three
// keys as niri natives. niri's save runs on a thread and save_screenshot returns
// Ok before that thread starts (niri 26.04, src/niri.rs), so a screenshot
// directory that is not writable, or a disk with no room, is a line in niri's
// log and a success on the socket. From a keypress that is the failure this
// repo's rules name as the worst one available: nothing on the screen, nothing
// on the disk, and nothing said. Here the same shot answers with a path or with
// a reason.
package capture

import (
	"os"
	"path/filepath"
	"time"
)

// Kind is which of niri's two automatic screenshots this is.
//
// The region is not one of them and has its own verb (RegionShot). niri's
// region picker saves the frame that goes to the monitor, so a window blocked
// from capture is in the file, and going around it is a different mechanism
// rather than a third value here (region.go).
type Kind int

const (
	// Window is the focused window.
	Window Kind = iota
	// Full is the focused monitor.
	Full
)

func (k Kind) String() string {
	if k == Window {
		return "window"
	}
	return "screen"
}

// Shooter is the compositor half: ask for one screenshot into a path, and hear
// what became of it. Implemented by *niri.Shots.
type Shooter interface {
	ScreenshotWindow(path string) error
	ScreenshotScreen(path string) error
	Captured() (<-chan string, error)
}

// How long to wait, and the two numbers are a person and a keypress. A region
// is chosen by dragging, which takes as long as it takes; niri's other two are
// captured by the time the action returns, so what is left is the encode and the
// write.
//
// Variables so the tests can make the wait short enough to assert on, the way
// the compositor client's own timeout is one (internal/niri, requestTimeout).
var (
	regionWait = 90 * time.Second
	promptWait = 10 * time.Second
)

// Ext is what a capture is. niri encodes PNG and nothing here re-encodes it.
const Ext = ".png"

// Dir is where captures land.
//
// niri's own default for screenshot-path, spelled out here rather than read out
// of niri's config, so that a shot taken by zde and a shot taken by anything
// else that has ever run on this machine are in one directory and a person has
// one place to look.
//
// The honest limit: a machine that has moved screenshot-path has two
// directories, because zde does not parse niri's KDL to find out. That is a
// setting nothing in zde writes and nothing in zde reads, and guessing at it
// would be worse than saying so.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// The same fallback the config readers take rather than joining an
		// empty string, which is a relative path and therefore a capture that
		// lands wherever the keypress happened to be started from
		// (internal/apps, Path).
		return filepath.Join(os.TempDir(), "zde", "Screenshots")
	}
	return filepath.Join(home, "Pictures", "Screenshots")
}
