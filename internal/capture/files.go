package capture

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

// UTC keeps directory order stable across daylight-saving changes.
const stampFmt = "20060102T150405Z"

// Only files this package named are candidates for an implicit send-to.
var nameShape = regexp.MustCompile(`^zde-(\d{8}T\d{6}Z)(?:-(\d+))?\.png$`)

// A complete PNG ends with this fixed IEND chunk.
var pngEnd = []byte{0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82}

// Far past anybody's finger, but bounds collisions in a planted directory.
const tries = 64

// claim takes a name in dir that nothing else has. O_EXCL prevents both
// same-second overwrites and a planted symlink redirecting a screen image.
func claim(dir string, now time.Time) (string, error) {
	stamp := now.UTC().Format(stampFmt)
	for sequence := 1; sequence <= tries; sequence++ {
		path := filepath.Join(dir, captureName(stamp, sequence))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err == nil {
			return path, file.Close()
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("cannot write a capture to %s: %w", dir, err)
		}
	}
	return "", fmt.Errorf("%d captures already have this second's name in %s, "+
		"which is not a finger on a key", tries, dir)
}

// claimPending pairs the reserved public name with a private path for niri's
// asynchronous writer. Only a confirmed, complete PNG replaces the reservation.
func claimPending(dir string, now time.Time) (string, string, error) {
	path, err := claim(dir, now)
	if err != nil {
		return "", "", err
	}
	file, err := os.CreateTemp(dir, ".zde-capture-*.pending")
	if err != nil {
		os.Remove(path) //nolint:errcheck // release the public reservation
		return "", "", fmt.Errorf("cannot stage a capture in %s: %w", dir, err)
	}
	pending := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)    //nolint:errcheck // the pair is unusable
		os.Remove(pending) //nolint:errcheck // the pair is unusable
		return "", "", fmt.Errorf("reserve capture %s: %w", pending, err)
	}
	return path, pending, nil
}

func captureName(stamp string, sequence int) string {
	if sequence == 1 {
		return fmt.Sprintf("zde-%s%s", stamp, Ext)
	}
	return fmt.Sprintf("zde-%s-%d%s", stamp, sequence, Ext)
}

func completePNG(path string) bool {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < int64(len(pngEnd)) {
		return false
	}
	end := make([]byte, len(pngEnd))
	if _, err := file.ReadAt(end, info.Size()-int64(len(end))); err != nil {
		return false
	}
	return bytes.Equal(end, pngEnd)
}
