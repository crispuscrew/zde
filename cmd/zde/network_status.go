package main

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/link"
	"github.com/crispuscrew/zde/internal/zded"
)

// connections prints the link and nearby networks when no shell shows the widget.
func connections() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var cn zded.Connections
	if err := c.Call("net.connections", &cn); err != nil {
		return err
	}
	if cn.Shown {
		return nil
	}
	fmt.Println(linkLine(cn.Link))
	if len(cn.Networks) == 0 {
		switch {
		case cn.Link.Kind == link.KindAbsent:
			// The line above already said the whole of it.
		case !cn.Link.Wifi:
			fmt.Println("no wifi radio on this machine")
		default:
			fmt.Println("no wifi networks in range")
		}
		return nil
	}
	for _, n := range cn.Networks {
		fmt.Printf("%d\t%s\t%s\t%s\n", n.Signal, security(n), note(n), n.SSID)
	}
	return nil
}

func netStatus() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var st link.Status
	if err := c.Call("net.status", &st); err != nil {
		return err
	}
	fmt.Println(linkLine(st))
	return nil
}

func linkLine(st link.Status) string {
	if st.Killed {
		// A deliberate cut also reports no link; show its cause before the kind.
		return "cut: zde has NetworkManager's networking switch off, and `zde net kill` puts it back"
	}
	switch st.Kind {
	case link.KindWifi:
		if st.SSID == "" {
			return "wifi"
		}
		return fmt.Sprintf("wifi %s %d%%", st.SSID, st.Signal)
	case link.KindWired:
		return "wired"
	case link.KindAbsent:
		return "no NetworkManager on this machine: zde asks it everything about " +
			"the network, and layer 0 installs it with zde.laptop.enable"
	default:
		return "not connected"
	}
}

func security(n link.Network) string {
	if n.Secure {
		return "secure"
	}
	return "open"
}

func note(n link.Network) string {
	switch {
	case n.Active:
		return "here"
	case n.Saved:
		return "saved"
	}
	return "-"
}
