package main

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/zded"
)

// powerMenu opens the menu or prints its choices when no shell answers. Naming
// a choice runs it directly: typing the verb confirms the terminal request.
func powerMenu(what string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	if what != "" {
		var said string
		if err := c.Call("system.power", &said, what); err != nil {
			return err
		}
		fmt.Println(said)
		return nil
	}
	var menu zded.Power
	if err := c.Call("system.power", &menu); err != nil {
		return err
	}
	if menu.Shown {
		return nil
	}
	for _, ch := range menu.Choices {
		fmt.Printf("%-9s %s  %s\n", ch.Name, asksFirst(ch), ch.Desc)
		if ch.Why != "" {
			fmt.Println(powerIndent + ch.Why)
		}
		for _, cost := range ch.Costs {
			fmt.Println(powerIndent + cost)
		}
	}
	return nil
}

// Align continuations under the description: 9 name + 1 space + 1 mark + 2 spaces.
const powerIndent = "             "

func asksFirst(ch zded.PowerChoice) string {
	if ch.Confirm {
		return "!"
	}
	return "."
}
