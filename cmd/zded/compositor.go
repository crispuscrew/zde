package main

import (
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/niri"
	"github.com/crispuscrew/zde/internal/zded"
)

// compositor dials niri per call so a compositor restart cannot leave the daemon
// using a dead connection or reporting stale state.
type compositor struct{}

// subscribe owns a separate connection for the lifetime of the event stream.
func subscribe() (<-chan string, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	events, err := c.Events()
	if err != nil {
		c.Close()
		return nil, err
	}
	return events, nil
}

func (compositor) DeskMap() (*desk.Map, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.DeskMap()
}

func (compositor) FocusedName() (string, error) {
	c, err := niri.Dial()
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.FocusedName()
}

func (compositor) FocusedOutput() (string, error) {
	c, err := niri.Dial()
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.FocusedOutput()
}

func (compositor) FocusedPlace() (string, string, error) {
	c, err := niri.Dial()
	if err != nil {
		return "", "", err
	}
	defer c.Close()
	return c.FocusedPlace()
}

func (compositor) FocusedWindow() (uint64, error) {
	c, err := niri.Dial()
	if err != nil {
		return 0, err
	}
	defer c.Close()
	return c.FocusedWindow()
}

// Windows converts niri's fields to the socket's independent wire type.
func (compositor) Windows() ([]zded.Window, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	open, err := c.OpenWindows()
	if err != nil {
		return nil, err
	}
	out := make([]zded.Window, 0, len(open))
	for _, w := range open {
		out = append(out, zded.Window{
			ID:        w.ID,
			Title:     w.Title,
			AppID:     w.AppID,
			Workspace: w.Workspace,
		})
	}
	return out, nil
}

func (compositor) EmptyByOutput() (map[string][]uint64, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.EmptyByOutput()
}

func (compositor) FirstApps() (map[uint64]string, error) {
	c, err := niri.Dial()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.FirstApps()
}
