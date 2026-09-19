package main

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/clip"
	"github.com/crispuscrew/zde/internal/zded"
)

// clipHistory prints previews when the shell does not acknowledge the picker.
// A preview can contain an entire short secret; a slow shell also takes this
// fallback. Full entries stay in the daemon until selected by id.
func clipHistory() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var h zded.Clips
	if err := c.Call("clip.history", &h); err != nil {
		return err
	}
	if h.Shown {
		return nil
	}
	if len(h.Entries) == 0 {
		if h.Why != "" {
			fmt.Println("nothing is watching the clipboard: " + h.Why)
			return nil
		}
		fmt.Println("nothing copied recently: entries last " + clip.TTL.String())
		return nil
	}
	for _, e := range h.Entries {
		said := e.Preview
		if e.Kind != clip.KindText {
			said = e.Why
		}
		fmt.Printf("%d\t%s\t%s\t%s\n", e.ID, e.At.Format("15:04"), e.Kind, said)
	}
	return nil
}

func clipClear() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var gone int
	if err := c.Call("clip.clear", &gone); err != nil {
		return err
	}
	if gone == 1 {
		fmt.Println("forgot 1 entry")
		return nil
	}
	fmt.Printf("forgot %d entries\n", gone)
	return nil
}
