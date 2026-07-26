// zde is the command line into zded. Every generated keybind that is not a
// niri native spawns this (common/keymap/keymap.yaml), so it is also the thing
// that has to say something useful when the daemon is not running.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/crispuscrew/zde/internal/zded"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		usage()
		return
	}
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch strings.Join(args, " ") {
	case "status":
		return status()
	case "desk list":
		return deskList()
	default:
		usage()
		return fmt.Errorf("zde: unknown command %q", strings.Join(args, " "))
	}
}

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
	if st.LastDesk != "" {
		fmt.Printf("last desk  %s\n", st.LastDesk)
	}
	if st.Skipped > 0 {
		fmt.Printf("journal    %d entries could not be read\n", st.Skipped)
	}
	return nil
}

func deskList() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var desks []string
	if err := c.Call("desk.list", &desks); err != nil {
		return err
	}
	for _, d := range desks {
		fmt.Println(d)
	}
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `zde - the command line into zded

  zde status      what zded and the compositor are doing
  zde desk list   the desks that exist right now

Most of the action map (docs/model.md, section 6) is not wired yet; the
generated keybinds that call it land with roadmap 0.1.
`)
}
