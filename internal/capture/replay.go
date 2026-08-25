package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/crispuscrew/zde/internal/plainfile"
)

const (
	replayCLI    = "gsr-cli"
	replaySocket = "zde-replay.sock"
)

var replayWait = time.Minute

// ReplayClip asks the one recorder instance ZDE started to save its buffer.
// gsr-cli returns only after the muxer closes the file and names that file.
func ReplayClip() (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return "", fmt.Errorf("no replay clip: XDG_RUNTIME_DIR is not set, so the replay service socket has no location")
	}
	bin, err := exec.LookPath(replayCLI)
	if err != nil {
		return "", fmt.Errorf("no replay clip: %s is not installed; enable zde.capture.replay.enable: %w", replayCLI, err)
	}

	ctx, stop := context.WithTimeout(context.Background(), replayWait)
	defer stop()
	cmd := exec.CommandContext(ctx, bin, "-ipc", filepath.Join(runtime, replaySocket), "save-replay")
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("no replay clip: %s did not confirm a saved file within %s", replayCLI, replayWait)
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if said := firstLine(string(exit.Stderr)); said != "" {
				return "", fmt.Errorf("no replay clip: %s", said)
			}
		}
		return "", fmt.Errorf("no replay clip: %s: %w", replayCLI, err)
	}

	path := strings.TrimSpace(string(out))
	if path == "" || strings.ContainsAny(path, "\r\n") || !filepath.IsAbs(path) {
		return "", fmt.Errorf("no replay clip: %s returned no absolute file path", replayCLI)
	}
	file, err := plainfile.OpenNoFollow(path)
	if err != nil {
		return "", fmt.Errorf("replay recorder reported %s, but it is not a file: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return "", fmt.Errorf("replay recorder reported %s, but it is empty or unreadable", path)
	}
	return path, nil
}
