package main

import (
	"fmt"

	"github.com/crispuscrew/zde/internal/zded"
)

func status() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var st zded.Status
	if err := c.Call("status", &st); err != nil {
		return err
	}
	fmt.Printf("zded       %s\n", st.Version)
	fmt.Printf("compositor %s\n", st.Compositor)
	fmt.Printf("desks      %d\n", st.Desks)
	shell := yesno(st.Shell)
	if st.Listeners > 1 {
		shell = fmt.Sprintf("%s (%d listening)", shell, st.Listeners)
	}
	fmt.Printf("shell      %s\n", shell)
	conns := fmt.Sprintf("%d of %d", st.Connections, zded.ConnectionsMax)
	if st.Dropped > 0 {
		conns = fmt.Sprintf("%s, %d dropped to make room", conns, st.Dropped)
	}
	fmt.Printf("conns      %s\n", conns)
	fmt.Printf("notify     %s\n", yesno(st.Notifications))
	fmt.Printf("zinc       %s\n", yesno(st.Zinc))
	fmt.Printf("attn       %s\n", st.Mode)
	hidden := "off"
	if st.Zen {
		hidden = "on"
	}
	fmt.Printf("zen        %s\n", hidden)
	fmt.Printf("queue      %d waiting\n", st.Queued)
	if st.OnDesk != "" {
		fmt.Printf("on desk    %s\n", st.OnDesk)
	}
	if st.LastDesk != "" {
		fmt.Printf("last desk  %s\n", st.LastDesk)
	}
	if st.Skipped > 0 {
		fmt.Printf("journal    %d entries could not be read\n", st.Skipped)
	}
	if st.Unplaced > 0 {
		fmt.Printf("unplaced   %d arrivals kept in memory and drew no card: no desk could be named for them, and one here is private\n", st.Unplaced)
	}
	for _, bad := range st.BadManifests {
		fmt.Printf("manifest   %s\n", bad)
	}
	return nil
}
