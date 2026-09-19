package zded

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

// DefaultSocket requires the login's private, ephemeral runtime directory.
func DefaultSocket() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", errors.New("zded: XDG_RUNTIME_DIR is not set, and the zde socket belongs nowhere else")
	}
	return filepath.Join(dir, "zde", "zded.sock"), nil
}

// Listen removes a stale socket only after a dial finds nobody answering.
// The directory and socket permissions are both part of the user boundary.
func (s *Server) Listen(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return fmt.Errorf("zded: another zded is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	return nil
}

func (s *Server) Serve() error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return errors.New("zded: Serve before Listen")
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

// Close stops the radio and popup pump, then listening, then subprocesses.
// Repeated calls are safe; the subprocess wait is bounded.
func (s *Server) Close() error {
	s.closeRadio()
	s.stopPump.Do(func() { close(s.popupStop) })
	s.mu.Lock()
	ln := s.ln
	s.ln = nil
	s.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
	}
	s.stopRuns()
	return err
}

// runStopWait bounds reaping after cancellation kills subprocess groups.
const runStopWait = 2 * time.Second

// startRun acknowledges before starting the stream so text cannot precede acceptance.
func (s *Server) startRun(k *sink, tier string, argv []string, doc, requestID string) bool {
	s.mu.Lock()
	if s.runCtx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	s.runs.Add(1)
	s.mu.Unlock()
	k.replyTo(requestID, ok("asking"))
	go func() {
		defer s.runs.Done()
		s.askRun(k, tier, argv, doc)
	}()
	return true
}

func (s *Server) stopRuns() {
	s.mu.Lock()
	s.runStop()
	s.mu.Unlock()

	gone := make(chan struct{})
	go func() {
		s.runs.Wait()
		close(gone)
	}()
	t := time.NewTimer(runStopWait)
	defer t.Stop()
	select {
	case <-gone:
	case <-t.C:
		log.Printf("zded: something it started was still running %v after being stopped", runStopWait)
	}
}
