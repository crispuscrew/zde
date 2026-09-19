package main

import (
	"fmt"
	"os"

	"github.com/crispuscrew/zde/internal/zded"
)

func deskList() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var desks []string
	if err := c.Call("desk.list", &desks); err != nil {
		return err
	}
	for _, d := range desks {
		fmt.Println(d)
	}
	return nil
}

func switchDesk(name string) error { return focusDesk("desk.switch", name) }

// focusDesk prints focused workspace names on stdout. Notes about unavailable
// monitors stay on stderr so scripts can read the names alone.
func focusDesk(method string, args ...string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var focused []string
	if err := c.Call(method, &focused, args...); err != nil {
		return err
	}
	for _, n := range focused {
		fmt.Println(n)
	}
	if note := c.Note(); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	return nil
}

func lastDesk() error { return focusDesk("desk.last") }

func switcher() error { return pickDesk("desk.switcher") }

// pickDesk prints the selectable desks when the shell cannot show the picker.
// The chosen name is accepted by the same verb on a subsequent call.
func pickDesk(method string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var sw zded.Switcher
	if err := c.Call(method, &sw); err != nil {
		return err
	}
	if sw.Shown {
		return nil
	}
	for _, d := range sw.Desks {
		if d == sw.On {
			fmt.Println(d, "(here)")
			continue
		}
		fmt.Println(d)
	}
	return nil
}

// jumpTo uses the same surface-or-list fallback, with window ids in column one.
func jumpTo() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var j zded.Jump
	if err := c.Call("window.jump-to", &j); err != nil {
		return err
	}
	if j.Shown {
		return nil
	}
	for _, w := range j.Windows {
		fmt.Printf("%d\t%s\t%s\t%s\n", w.ID, dash(w.Workspace), dash(w.AppID), w.Title)
	}
	return nil
}
