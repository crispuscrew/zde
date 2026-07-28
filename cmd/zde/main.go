// zde is the command line into zded. Every generated keybind that is not a
// niri native spawns this (common/keymap/keymap.yaml), so it is also the thing
// that has to say something useful when the daemon is not running.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/crispuscrew/zde/internal/journal"
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
	case len(args) == 3 && args[0] == "desk" && args[1] == "move-window":
		return focusDesk("desk.move-window", args[2])
	case len(args) == 3 && args[0] == "desk" && args[1] == "move-window-to":
		return focusDesk("desk.move-window-to", args[2])
	case len(args) == 2 && args[0] == "workspace" && (args[1] == "next" || args[1] == "prev"):
		return focusDesk("workspace." + args[1])
	case len(args) == 2 && args[0] == "nav" && (args[1] == "down" || args[1] == "up"):
		return focusDesk("nav." + args[1])
	case len(args) == 2 && args[0] == "desk" && (args[1] == "next" || args[1] == "prev"):
		return focusDesk("desk." + args[1])
	case len(args) >= 3 && args[0] == "queue" && args[1] == "add":
		// Everything after "add" is the text, so it can be typed without
		// quoting: zde queue add reply to ilya about the invoice.
		return queueAdd(strings.Join(args[2:], " "))
	case len(args) == 1 && args[0] == "queue":
		return queueList()
	case len(args) == 3 && args[0] == "queue" && args[1] == "done":
		return call("queue.done", args[2])
	case len(args) == 2 && args[0] == "desk" && args[1] == "queue-jump":
		return focusDesk("desk.queue-jump")
	case len(args) == 2 && args[0] == "desk" && args[1] == "regulars":
		return focusDesk("desk.regulars")
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
	if st.OnDesk != "" {
		fmt.Printf("on desk    %s\n", st.OnDesk)
	}
	if st.LastDesk != "" {
		fmt.Printf("last desk  %s\n", st.LastDesk)
	}
	if st.Skipped > 0 {
		fmt.Printf("journal    %d entries could not be read\n", st.Skipped)
	}
	return nil
}

// queueAdd says what it recorded, with the id needed to finish it.
func queueAdd(text string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var it journal.Item
	if err := c.Call("queue.add", &it, text); err != nil {
		return err
	}
	fmt.Printf("%d\t%s\n", it.ID, it.Text)
	return nil
}

// queueList prints one item per line, id first, so that finishing one is a
// copy of what is already on the screen - and so that a bar can read it.
func queueList() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var q []journal.Item
	if err := c.Call("queue.list", &q); err != nil {
		return err
	}
	for _, it := range q {
		where := it.Desk
		if where == "" {
			where = "-"
		}
		fmt.Printf("%d\t%s\t%s\n", it.ID, where, it.Text)
	}
	return nil
}

// call is a verb with nothing to print: it worked, or it says why not.
func call(method string, args ...string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(method, nil, args...)
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

func switchDesk(name string) error { return focusDesk("desk.switch", name) }

// focusDesk calls one of the verbs that bring a desk up, and prints the
// workspaces it left focused - one name per line, the way everything else in
// zde says a workspace.
func focusDesk(method string, args ...string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var focused []string
	if err := c.Call(method, &focused, args...); err != nil {
		return err
	}
	for _, n := range focused {
		fmt.Println(n)
	}
	return nil
}

// lastDesk goes back, and says where to. It is a switch like the others and
// answers like one; throwing that away left the one verb whose whole point is
// where you end up as the only one that would not say.
func lastDesk() error { return focusDesk("desk.last") }

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
  zde workspace next|prev
                         one along this desk's band, stopping at its ends
  zde nav down|up        the window along the stack, else the desk beside this
  zde desk move-window next|prev
                         carry the focused window to the desk beside, and go
  zde desk move-window-to NAME
                         carry it to that desk, the way into the regulars
  zde desk next          the desk after this one, wrapping (regulars excluded)
  zde desk prev          the desk before this one, wrapping
  zde queue              what is waiting, oldest first
  zde queue add TEXT     make something wait, on the desk you are on
  zde queue done ID      it is not waiting any more
  zde desk queue-jump    go to where the oldest thing waiting is
  zde desk regulars      the band that belongs to no desk (comms, music)
  zde desk last          go back to the desk you came from
  zde desk reconcile     make the workspace names true again
  zde desk snapshot [N]  write down the desk you are on, so you can ask for it

Most of the action map (docs/model.md, section 6) is not wired yet; the
generated keybinds that call it land with roadmap 0.1.
`)
}
