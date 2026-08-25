package capture

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// guarded bounds a subprocess with a context, its own process group, and a
// parent-death signal. A strings.Builder avoids exec's pipe-copy goroutine,
// which can otherwise wait on a child that inherited the pipe.
func guarded(ctx context.Context, name string, args ...string) (*exec.Cmd, *strings.Builder) {
	cmd := exec.CommandContext(ctx, name, args...)
	said := &strings.Builder{}
	cmd.Stderr = said
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck // ESRCH means it already ended
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd, said
}

func firstLine(message string) string {
	if index := strings.IndexByte(message, '\n'); index >= 0 {
		message = message[:index]
	}
	return strings.TrimSpace(message)
}

func missing(tool string) error {
	return fmt.Errorf("%s is not installed, so a region cannot be captured. niri's own region "+
		"picker is deliberately not used here: it saves the frame that goes to the monitor, so a "+
		"window blocked from capture is in the file (docs/model.md, section 6). Install %s and %s, "+
		"which layer 1 does", tool, Selector, Grabber)
}
