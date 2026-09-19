package main

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/zded"
)

// queueAdd prints the id needed to finish the item.
func queueAdd(text string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var it journal.Item
	if err := c.Call("queue.add", &it, text); err != nil {
		return err
	}
	fmt.Printf("%d\t%s\n", it.ID, it.Text)
	return nil
}

// queueClear prints the count, including zero; typing the verb confirms it.
func queueClear() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var cleared zded.Cleared
	if err := c.Call("queue.clear", &cleared); err != nil {
		return err
	}
	fmt.Printf("%d\n", cleared.Count)
	return nil
}

// queueList prints one item per line, with its id first for queue.done.
func queueList() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var q []journal.Item
	if err := c.Call("queue.list", &q); err != nil {
		return err
	}
	// Filter at the terminal boundary too: journal replay can include text
	// written outside the validated arrival path. Tabs must remain separators.
	for _, it := range q {
		fmt.Printf("%d\t%s\t%s\t%s\t%s\t%s\n",
			it.ID, urgentMark(it.Urgent), selfMark(it.Self), dash(attn.Line(it.Desk)),
			dash(attn.Line(it.From)), attn.Line(it.Text))
	}
	return nil
}
