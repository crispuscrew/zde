package main

import (
	"fmt"
	"log"
	"os"

	"github.com/crispuscrew/zde/internal/attn"
)

func fatal(err error) {
	complain(err)
	os.Exit(1)
}

func complain(err error) { fmt.Fprintln(errOut, "zded:", err) }

// errOut filters fmt and internal/zded's log output at one boundary so new call
// sites cannot bypass terminal filtering. Block preserves multiline diagnostics
// and indents continuations so external text cannot forge a daemon log line.
// Inherited subprocess stderr is outside this writer.
var errOut door

// door assumes one message per Write, as fmt and log provide. Every piece is
// filtered even if a future caller splits a message; its layout may then change.
type door struct{}

func (door) Write(p []byte) (int, error) {
	// Resolve stderr per write so redirection also applies in tests.
	if _, err := fmt.Fprintln(os.Stderr, attn.Block(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Install before main so even startup log messages pass through the filter.
func init() { log.SetOutput(errOut) }
