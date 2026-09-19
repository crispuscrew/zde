package main

import (
	"fmt"
	"os"

	"github.com/crispuscrew/zde/internal/zded"
)

// zen reports the resulting state even when no shell is running to show it.
func zen(state string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var z zded.Zen
	if err := c.Call("desk.zen", &z, state); err != nil {
		return err
	}
	if z.Zen {
		fmt.Println("zen on: content only")
	} else {
		fmt.Println("zen off")
	}
	return nil
}

// deskPanic reports which way the toggle went; incomplete work is noted on stderr.
func deskPanic() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var said string
	if err := c.Call("desk.panic", &said); err != nil {
		return err
	}
	fmt.Println(said)
	if note := c.Note(); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	return nil
}

// lockPreset delegates to the daemon so the desk switch completes before locking.
// Notes on stderr distinguish a completed lock from a skipped preset switch.
func lockPreset() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Call("system.lock-preset", nil); err != nil {
		return err
	}
	if note := c.Note(); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	return nil
}
