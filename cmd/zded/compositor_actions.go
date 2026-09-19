package main

import "github.com/crispuscrew/zde/internal/niri"

func (compositor) MoveWindowToWorkspace(name string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.MoveWindowToWorkspace(name)
}

func (compositor) FocusWindow(id uint64) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.FocusWindow(id)
}

func (compositor) FocusWindowVertically(down bool) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.FocusWindowVertically(down)
}

func (compositor) Perform(action string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Perform(action)
}

func (compositor) ReloadConfig() error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.ReloadConfig()
}

func (compositor) FocusWorkspace(name string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.FocusWorkspace(name)
}

func (compositor) RenameWorkspace(from, to string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.RenameWorkspace(from, to)
}

func (compositor) SetWorkspaceNameByID(id uint64, name string) error {
	c, err := niri.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.SetWorkspaceNameByID(id, name)
}
