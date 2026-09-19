package main

import (
	"fmt"
	"os"

	"github.com/crispuscrew/zde/internal/keymap"
	"github.com/crispuscrew/zde/internal/zded"
)

// keys reads the list generated with the installed binds. ReadText rejects
// non-regular files so a FIFO cannot hang the terminal opened by a keybind.
func keys() error {
	path := keymap.TextPath()
	data, err := keymap.ReadText()
	if os.IsNotExist(err) {
		return fmt.Errorf("no keymap at %s: layer 1 installs it, so this is a zde "+
			"whose home-manager module has not been activated", path)
	}
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

// palette prints selectable names when the shell cannot show the picker.
func palette() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var p zded.Palette
	if err := c.Call("palette.list", &p); err != nil {
		return err
	}
	if p.Shown {
		return nil
	}
	for _, a := range p.Actions {
		fmt.Printf("%s\t%s\t%s\t%s\n", a.Name, dash(a.Key), dash(a.Why), a.Desc)
	}
	return nil
}
