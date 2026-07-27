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
	switch {
	case len(args) == 2 && args[0] == "desk" && args[1] == "switcher":
		// The switcher is a shell surface (roadmap 0.1); until it exists,
		// listing is the honest thing this key can do.
		return deskList()
	case len(args) == 3 && args[0] == "desk" && args[1] == "switch":
		return switchDesk(args[2])
	case len(args) == 2 && args[0] == "desk" && args[1] == "last":
		return lastDesk()
	case len(args) == 2 && args[0] == "desk" && args[1] == "reconcile":
		return reconcile()
	case len(args) == 2 && args[0] == "desk" && args[1] == "snapshot":
		return snapshot(nil)
	case len(args) == 3 && args[0] == "desk" && args[1] == "snapshot":
		return snapshot([]string{args[2]})
	}
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

func switchDesk(name string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var focused []string
	if err := c.Call("desk.switch", &focused, name); err != nil {
		return err
	}
	for _, n := range focused {
		fmt.Println(n)
	}
	return nil
}

func lastDesk() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call("desk.last", nil)
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

func usage() {
	fmt.Fprint(os.Stderr, `zde - the command line into zded

  zde status             what zded and the compositor are doing
  zde desk list          the desks that exist right now
  zde desk switch NAME   bring a desk up on every monitor it owns
  zde desk last          go back to the desk you came from
  zde desk reconcile     make the workspace names true again
  zde desk snapshot [N]  write down the desk you are on, so you can ask for it

Most of the action map (docs/model.md, section 6) is not wired yet; the
generated keybinds that call it land with roadmap 0.1.
`)
}
