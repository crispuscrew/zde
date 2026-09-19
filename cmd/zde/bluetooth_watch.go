package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crispuscrew/zde/internal/bt"
	"github.com/crispuscrew/zde/internal/zded"
)

// pairDevice watches the asynchronous pairing and binds each answer to the
// question printed here. Pairing does not grant trust on later connections.
func pairDevice(c *zded.Client, addr string) error {
	if err := c.Call("bluetooth.pair", nil, addr); err != nil {
		return err
	}
	asked := ""
	return watchRadio(c, bt.WatchFor, func(st bt.State) (bool, error) {
		if d, found := deviceIn(st, addr); found && d.Paired {
			fmt.Println("paired")
			fmt.Println("not trusted, so it asks again when it reconnects.")
			fmt.Println("if it should not: zde system bluetooth trust " + d.Address)
			return true, nil
		}
		if st.Pending != nil && st.Pending.ID != asked {
			asked = st.Pending.ID
			printQuestion(*st.Pending)
			answered, err := answerHere(c, *st.Pending)
			if err != nil {
				return true, err
			}
			if !answered {
				// No terminal: the printed command is the way to answer instead.
				return true, nil
			}
		}
		if st.Doing == "" {
			if st.Failed != "" {
				return true, errors.New(st.Failed)
			}
			if asked != "" {
				return true, errors.New("not paired")
			}
		}
		return false, nil
	})
}

func connectDevice(c *zded.Client, addr string) error {
	if err := c.Call("bluetooth.connect", nil, addr); err != nil {
		return err
	}
	return watchRadio(c, bt.AnswerWait, func(st bt.State) (bool, error) {
		if d, found := deviceIn(st, addr); found && d.Connected {
			fmt.Println("connected")
			return true, nil
		}
		if st.Pending != nil {
			// Paired but untrusted devices still need permission to connect.
			printQuestion(*st.Pending)
			answered, err := answerHere(c, *st.Pending)
			return !answered, err
		}
		if st.Doing == "" && st.Failed != "" {
			return true, errors.New(st.Failed)
		}
		return false, nil
	})
}

func watchRadio(c *zded.Client, within time.Duration, stop func(bt.State) (bool, error)) error {
	deadline := time.Now().Add(within)
	for {
		var st bt.State
		if err := c.Call("bluetooth.state", &st); err != nil {
			return err
		}
		enough, err := stop(st)
		if err != nil || enough {
			return err
		}
		if time.Now().After(deadline) {
			// The daemon's attempt carries on after this terminal stops waiting.
			return errors.New("still waiting: `zde system bluetooth` says where it got to")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func deviceIn(st bt.State, addr string) (bt.Device, bool) {
	for _, d := range st.Devices {
		if strings.EqualFold(d.Address, addr) {
			return d, true
		}
	}
	return bt.Device{}, false
}
