// zde is the command line into zded, also used by generated keybinds.
package main

import (
	"fmt"
	"os"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/zded"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		usage()
		return
	}
	if err := run(args); err != nil {
		complain(err)
		os.Exit(1)
	}
}

// complain filters all reported errors at the terminal boundary. Block preserves
// parser layout while stripping terminal instructions and indenting continuations.
func complain(err error) { fmt.Fprintln(os.Stderr, attn.Block(err.Error())) }

// call is a verb with nothing to print: it worked, or it says why not.
func call(method string, args ...string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(method, nil, args...)
}
