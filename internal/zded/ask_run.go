package zded

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// askRun streams privately and keeps no transcript. It owns both pipes so timeout
// and process exit can unblock readers even when descendants retain write ends.
// Cancellation kills the process group. Pdeathsig covers the direct child on daemon
// death, but descendants do not inherit it and external containers belong to zinc.
func (s *Server) askRun(k *sink, tier string, argv []string, doc string) {
	defer k.asking.Store(false)
	defer s.releaseAsk()

	ctx, cancel := context.WithTimeout(s.runCtx, askTimeout)
	defer cancel()
	go func() {
		select {
		case <-k.endedOf():
			cancel()
		case <-ctx.Done():
		}
	}()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Conversation text is stdin, never subprocess argv or a stored transcript.
	cmd.Stdin = strings.NewReader(doc)

	pr, pw, err := os.Pipe()
	if err != nil {
		askDone(k, err.Error())
		return
	}
	er, ew, err := os.Pipe()
	if err != nil {
		pr.Close()
		pw.Close()
		askDone(k, err.Error())
		return
	}
	cmd.Stdout = pw
	cmd.Stderr = ew
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		killGroup(cmd)
		return nil
	}
	cmd.WaitDelay = askGrace

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		er.Close()
		ew.Close()
		reason := err
		if inner := errors.Unwrap(err); inner != nil {
			reason = inner
		}
		askDone(k, fmt.Sprintf("the %s tier could not start %q: %s (set zde.ask.tiers.%s to something this machine has)",
			tier, argv[0], reason, tier))
		return
	}
	// Parent write ends must close before readers can observe EOF.
	pw.Close()
	ew.Close()
	go func() {
		<-ctx.Done()
		pr.Close()
		er.Close()
	}()
	complaint := make(chan []byte, 1)
	go func() { complaint <- drain(er, complaintMax) }()

	waited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pr.SetReadDeadline(time.Now().Add(askDrain))
		er.SetReadDeadline(time.Now().Add(askDrain))
		waited <- err
	}()

	said := pump(k, pr, cancel)
	werr := <-waited
	// CommandContext no longer calls Cancel once Wait has returned.
	killGroup(cmd)
	complained := <-complaint

	switch {
	case said >= askMax:
		askDone(k, fmt.Sprintf("the %s tier went past %d KiB and was stopped: what is above is as much of the answer as arrived",
			tier, askMax>>10))
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		askDone(k, fmt.Sprintf("the %s tier did not finish within %s", tier, askTimeout))
	case werr != nil:
		askDone(k, fmt.Sprintf("the %s tier failed: %s", tier, complaintOf(complained, werr)))
	case said == 0:
		askDone(k, "the "+tier+" tier ran and said nothing")
	default:
		askDone(k, "")
	}
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
