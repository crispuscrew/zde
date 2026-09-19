package main

import (
	"fmt"
	"strings"

	"github.com/crispuscrew/zde/internal/bt"
	"github.com/crispuscrew/zde/internal/zded"
)

func bluetooth(args []string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	switch {
	case len(args) == 0:
		var st bt.State
		if err := c.Call("bluetooth.state", &st); err != nil {
			return err
		}
		printRadio(st)
		return nil
	case len(args) == 2 && (args[0] == "power" || args[0] == "scan"):
		return c.Call("bluetooth."+args[0], nil, args[1])
	case len(args) == 3 && args[0] == "confirm":
		// The id binds the answer to the question the person actually read.
		return c.Call("bluetooth.confirm", nil, args[1], args[2])
	case len(args) == 2 && args[0] == "pair":
		return pairDevice(c, args[1])
	case len(args) == 2 && args[0] == "connect":
		return connectDevice(c, args[1])
	case len(args) == 2 && (args[0] == "disconnect" || args[0] == "forget" ||
		args[0] == "trust" || args[0] == "untrust"):
		return c.Call("bluetooth."+args[0], nil, args[1])
	}
	usage()
	return fmt.Errorf("zde: unknown command %q", strings.Join(append([]string{"system", "bluetooth"}, args...), " "))
}
