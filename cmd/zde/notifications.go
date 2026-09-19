package main

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/zded"
)

// attnMode prints the resulting mode for read, set and toggle requests.
func attnMode(method string, args ...string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var a zded.Attn
	if err := c.Call(method, &a, args...); err != nil {
		return err
	}
	fmt.Println(a.Mode)
	return nil
}

func notifReach() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var r zded.Reach
	if err := c.Call("attn.reach", &r); err != nil {
		return err
	}
	if r.Reached {
		return nil
	}
	fmt.Println("no popup to reach: what has arrived is in the notification center")
	return nil
}

// notifCenter prints history when the shell cannot show it.
func notifCenter() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var center zded.Center
	if err := c.Call("attn.center", &center); err != nil {
		return err
	}
	if center.Shown {
		return nil
	}
	if len(center.Notifications) == 0 {
		fmt.Println("nothing has arrived yet")
		return nil
	}
	// Keep one printable row per arrival even if another daemon version or a
	// hand-edited snapshot supplies unfiltered text.
	for _, r := range center.Notifications {
		fmt.Printf("%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, urgentMark(r.Urgent), selfMark(r.Self), r.At.Format("Mon 15:04"),
			dash(attn.Line(r.From)), became(r), attn.Line(r.Text))
	}
	return nil
}

func became(r attn.Record) string {
	switch {
	case r.Dismissed:
		return "done"
	case r.Queued:
		return "waiting"
	default:
		return "silent"
	}
}

func urgentMark(urgent bool) string {
	if urgent {
		return "!"
	}
	return "."
}

// selfMark occupies its own column in both listings, outside sender-controlled
// text. Filtering tabs from the other columns prevents forging this mark.
func selfMark(self bool) string {
	if self {
		return "*"
	}
	return "."
}
