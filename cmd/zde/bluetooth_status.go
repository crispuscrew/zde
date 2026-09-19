package main

import (
	"fmt"
	"strconv"

	"github.com/crispuscrew/zde/internal/bt"
)

func printRadio(st bt.State) {
	if !st.Adapter.Present {
		fmt.Printf("adapter    none  %s\n", st.Adapter.Why)
		return
	}
	fmt.Printf("adapter    %s  %s\n", dash(st.Adapter.Name), st.Adapter.Address)
	// BlueZ gives the default-agent role to the last requester without telling
	// the displaced agent. Registration alone does not mean we answer pairing.
	if !st.Agent.Default {
		if st.Agent.Registered {
			fmt.Println("agent      registered, and NOT the default: something else on this machine")
			fmt.Println("           answers pairing questions")
		} else {
			fmt.Printf("agent      not registered%s\n", because(st.Agent.Why))
			fmt.Println("           nothing here will be asked before a device pairs")
		}
	}
	fmt.Printf("powered    %s\n", yesno(st.Adapter.Powered))
	fmt.Printf("scanning   %s\n", yesno(st.Adapter.Discovering))
	if st.Doing != "" {
		fmt.Printf("doing      %s\n", st.Doing)
	}
	if st.Failed != "" {
		fmt.Printf("failed     %s\n", st.Failed)
	}
	if st.Pending != nil {
		printQuestion(*st.Pending)
	}
	for _, d := range st.Devices {
		fmt.Printf("%s\t%s\t%s\t%s\n", d.Address, deviceFlags(d), signal(d), dash(d.Name))
	}
}

func because(why string) string {
	if why == "" {
		return ""
	}
	return ": " + why
}

func deviceFlags(d bt.Device) string {
	flag := func(on bool, c string) string {
		if on {
			return c
		}
		return "-"
	}
	return flag(d.Paired, "p") + flag(d.Trusted, "t") + flag(d.Connected, "c")
}

// signal is dBm, or a dash for a remembered device that is not in range.
func signal(d bt.Device) string {
	if d.RSSI == 0 {
		return "-"
	}
	return strconv.Itoa(int(d.RSSI))
}
