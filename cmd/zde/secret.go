package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	sig "os/signal"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// readSecret disables terminal echo and keeps the prompt on stderr, leaving
// stdout available for a redirected command result. The secret is never printed.
func readSecret(prompt string) (string, error) {
	restore := hush(os.Stdin)
	defer restore()
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	// Enter could not echo its newline while the terminal was quiet.
	fmt.Fprintln(os.Stderr)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// hush restores echo on return and on INT/TERM, then re-raises a caught signal
// so the parent still sees an interrupted process. Non-terminal stdin is a no-op.
func hush(f *os.File) func() {
	fd := int(f.Fd())
	before, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}
	}
	quiet := *before
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &quiet); err != nil {
		return func() {}
	}
	restore := func() {
		unix.IoctlSetTermios(fd, unix.TCSETS, before) //nolint:errcheck // nothing useful to do about a terminal that will not take its settings back
	}

	interrupted := make(chan os.Signal, 1)
	sig.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	over := make(chan struct{})
	go func() {
		select {
		case caught := <-interrupted:
			restore()
			sig.Stop(interrupted)
			syscall.Kill(os.Getpid(), caught.(syscall.Signal)) //nolint:errcheck // the process is on its way out
		case <-over:
		}
	}()
	return func() {
		close(over)
		sig.Stop(interrupted)
		restore()
	}
}
