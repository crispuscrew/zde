package main

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/link"
	"github.com/crispuscrew/zde/internal/zded"
)

// netConnect asks for a password only for a secured network with no saved
// profile. The secret travels through the socket, never a process argument.
func netConnect(ssid string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var networks []link.Network
	// A failed list does not preempt the join's more relevant error.
	_ = c.Call("net.list", &networks)
	args := []string{ssid}
	for _, n := range networks {
		if n.SSID != ssid || !n.Secure || n.Saved {
			continue
		}
		secret, err := readSecret("password for " + ssid + ": ")
		if err != nil {
			return err
		}
		if secret == "" {
			return fmt.Errorf("%s needs a password", ssid)
		}
		args = append(args, secret)
		break
	}
	var said string
	if err := c.Call("net.connect", &said, args...); err != nil {
		return err
	}
	fmt.Println(said)
	return nil
}

func netForget(ssid string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var said string
	if err := c.Call("net.forget", &said, ssid); err != nil {
		return err
	}
	fmt.Println(said)
	return nil
}

func netDisconnect() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var said string
	if err := c.Call("net.disconnect", &said); err != nil {
		return err
	}
	fmt.Println(said)
	return nil
}

// netKill prints the resulting state so a terminal user can distinguish the
// two directions of the toggle.
func netKill() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var said string
	if err := c.Call("net.kill", &said); err != nil {
		return err
	}
	fmt.Println(said)
	return nil
}
