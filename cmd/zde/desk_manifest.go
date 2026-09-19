package main

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/zded"
	"github.com/crispuscrew/zde/internal/zinc"
)

// deskApps asks zinc for each instance's state directory rather than deriving
// it from the manifest. Missing locations do not hide the declared addresses.
func deskApps(args []string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var list []zded.DeskApp
	if err := c.Call("desk.apps", &list, args...); err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no apps declared")
		return nil
	}
	var unanswered error
	for _, app := range list {
		place := dash(app.Place) // unpinned: adoption places it
		state := "-"
		if loc, err := zinc.Where(app.Address); err == nil {
			state = loc.State
		} else if unanswered == nil {
			unanswered = err
		}
		fmt.Printf("%s\t%s\t%s\n", app.Address, place, state)
	}
	// One error after the list keeps a missing binary from burying the rows.
	if unanswered != nil {
		complain(unanswered)
	}
	return nil
}

func reconcile() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var r zded.Reconciled
	if err := c.Call("desk.reconcile", &r); err != nil {
		return err
	}
	for _, line := range r.Renamed {
		fmt.Println("renamed ", line)
	}
	for _, line := range r.Adopted {
		fmt.Println("adopted ", line)
	}
	for _, line := range r.Conflict {
		fmt.Println("conflict", line)
	}
	if len(r.Renamed)+len(r.Adopted)+len(r.Conflict) == 0 {
		fmt.Println("nothing to reconcile")
	}
	return nil
}

func snapshot(args []string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var path string
	if err := c.Call("desk.snapshot", &path, args...); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}
