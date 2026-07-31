// zde is the command line into zded. Every generated keybind that is not a
// niri native spawns this (common/keymap/keymap.yaml), so it is also the thing
// that has to say something useful when the daemon is not running.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/doctor"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/zded"
	"github.com/crispuscrew/zde/internal/zinc"
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
	case len(args) == 3 && args[0] == "app" && args[1] == "launch":
		return launch(args[2])
	case len(args) == 2 && args[0] == "system" && args[1] == "lock":
		// The same resolution as any other launch: locking a screen is running
		// a program, and which one a machine has is the machine's business.
		if err := launch("lock"); err != nil {
			return fmt.Errorf("nothing to lock the screen with: %w", err)
		}
		return nil
	case len(args) == 2 && args[0] == "app" && args[1] == "list":
		return appList()
	case len(args) == 2 && args[0] == "desk" && args[1] == "switcher":
		return switcher()
	case len(args) == 2 && args[0] == "window" && args[1] == "jump-to":
		return jumpTo()
	case len(args) == 3 && args[0] == "window" && args[1] == "jump-to":
		// The id from the list, spent as it is read. It goes through focusDesk
		// because a jump answers the way every other verb that moves the
		// session answers: the workspace it left focused.
		return focusDesk("window.jump-to", args[2])
	case len(args) == 3 && args[0] == "desk" && args[1] == "switch":
		return switchDesk(args[2])
	case len(args) == 3 && args[0] == "desk" && args[1] == "move-window":
		return focusDesk("desk.move-window", args[2])
	case len(args) == 3 && args[0] == "desk" && args[1] == "move-window-to":
		return focusDesk("desk.move-window-to", args[2])
	case len(args) == 3 && args[0] == "desk" && args[1] == "move-workspace-to":
		return focusDesk("desk.move-workspace-to", args[2])
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
	case len(args) == 2 && args[0] == "desk" && args[1] == "apps":
		return deskApps(nil)
	case len(args) == 3 && args[0] == "desk" && args[1] == "apps":
		return deskApps([]string{args[2]})
	case len(args) == 2 && args[0] == "desk" && args[1] == "snapshot":
		return snapshot(nil)
	case len(args) == 3 && args[0] == "desk" && args[1] == "snapshot":
		return snapshot([]string{args[2]})
	}
	switch strings.Join(args, " ") {
	case "help":
		// Mod+slash spawns this. It has been an unknown command that printed
		// the usage to stderr and exited 1, which is the right text and the
		// wrong everything else.
		usage()
		return nil
	case "status":
		return status()
	case "doctor":
		return runDoctor()
	case "desk list":
		return deskList()
	default:
		usage()
		return fmt.Errorf("zde: unknown command %q", strings.Join(args, " "))
	}
}

// launch runs what this machine calls that name. It replaces this process
// rather than starting a child and waiting: the key that spawned `zde` wants a
// terminal, not a `zde` sitting behind one for as long as it lives.
//
// Nothing here talks to zded. Launching is not the daemon's business today, and
// when it becomes zcr's it will not be this process's either.
func launch(name string) error {
	all, err := apps.Load(apps.DefaultPath())
	if err != nil {
		return err
	}
	argv, err := all.Argv(name)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("%s is configured to run %q, which is not there: %w", name, argv[0], err)
	}
	return syscall.Exec(bin, argv, os.Environ())
}

// appList is how somebody finds out what their machine can start, which is
// otherwise only discoverable by pressing keys and watching nothing happen.
func appList() error {
	all, err := apps.Load(apps.DefaultPath())
	if err != nil {
		return err
	}
	names := all.Names()
	if len(names) == 0 {
		return fmt.Errorf("nothing is configured to run: set zde.apps in your home-manager config")
	}
	for _, n := range names {
		argv, _ := all.Argv(n)
		fmt.Printf("%s\t%s\n", n, strings.Join(argv, " "))
	}
	return nil
}

// deskApps prints what a desk is made of before any of it is running: the
// address of each app, the workspace it is pinned to, and where the thing that
// runs it says that instance keeps its state.
//
// The last column is asked for rather than worked out (internal/zinc). It is
// also the answer to the question a manifest raises and nothing else answers -
// two desks can declare the same app, and what makes them two browsers rather
// than one is a directory neither the manifest nor zde chooses.
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
		// Not an error: a desk with no apps is a desk somebody arranged by
		// hand, which is how every desk starts (docs/model.md, snapshot).
		fmt.Println("no apps declared")
		return nil
	}
	// address, where it goes, where its state is - tab separated like every
	// other list zde prints, with the long field last.
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
	// Once, after the list, and on stderr: the addresses are still the answer
	// to what the desk declares, and repeating one missing binary per app would
	// bury them.
	if unanswered != nil {
		fmt.Fprintln(os.Stderr, unanswered)
	}
	return nil
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
	// The three facts somebody wants when a key did nothing and there is no
	// second machine to look them up on. Always printed, including the boring
	// answers: "shell      no" is the whole diagnosis for a session where
	// Mod+Tab prints a list, and an absent line explains nothing.
	fmt.Printf("shell      %s\n", yesno(st.Shell))
	fmt.Printf("notify     %s\n", yesno(st.Notifications))
	// Layer 2: whether the session can run a sandboxed app at all. It is the
	// whole diagnosis for a desk whose apps do nothing, and it is a fact about
	// the session's PATH rather than this terminal's, which is why zded is the
	// one asked.
	fmt.Printf("zinc       %s\n", yesno(st.Zinc))
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
	// The desks that are not there. Printed last and one per line, because this
	// is the answer to "why is my desk gone", and a count would send someone
	// looking through the directory for which one.
	for _, bad := range st.BadManifests {
		fmt.Printf("manifest   %s\n", bad)
	}
	return nil
}

// runDoctor is every check on one screen (internal/doctor). The report goes to
// stdout whole, because somebody is going to paste it into a bug report; the
// summary goes to stderr as an error, which is also what makes the exit status
// non-zero. Only failures do that - a machine with no zinc yet has warnings on
// every line it can have them on, and a command that always exits non-zero is
// one nobody looks at.
func runDoctor() error {
	report := doctor.Run()
	fmt.Print(report)
	if n := report.Failed(); n > 0 {
		return fmt.Errorf("zde doctor: %d of %d checks failed", n, len(report))
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
	// id, urgency, desk, sender, text - tab separated, text last because it is
	// the only field that can be long, so a reader splitting on tabs has every
	// column it wants before it. A dash is "nothing here", which for the
	// sender means a person typed it.
	for _, it := range q {
		urgent := "."
		if it.Urgent {
			urgent = "!"
		}
		fmt.Printf("%d\t%s\t%s\t%s\t%s\n", it.ID, urgent, dash(it.Desk), dash(it.From), it.Text)
	}
	return nil
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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

// switcher asks for the picker. With a shell listening, that is a surface and
// this prints nothing; without one, the key still has to do something, so it
// prints the list it would have shown - which is what it did before there was
// a picker at all.
func switcher() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var sw zded.Switcher
	if err := c.Call("desk.switcher", &sw); err != nil {
		return err
	}
	if sw.Shown {
		return nil
	}
	for _, d := range sw.Desks {
		if d == sw.On {
			fmt.Println(d, "(here)")
			continue
		}
		fmt.Println(d)
	}
	return nil
}

// jumpTo asks for the window picker. The same bargain as the desk switcher:
// with a shell listening this prints nothing, and without one it prints the
// list, so that the key does something on a session whose shell has died - and
// so that what is open is greppable at all.
func jumpTo() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var j zded.Jump
	if err := c.Call("window.jump-to", &j); err != nil {
		return err
	}
	if j.Shown {
		return nil
	}
	// id, workspace, app, title - tab separated, id first so that the id is
	// cut from column one and handed straight back to `zde window jump-to`,
	// and the title last because it is the only field that can be long.
	for _, w := range j.Windows {
		fmt.Printf("%d\t%s\t%s\t%s\n", w.ID, dash(w.Workspace), dash(w.AppID), w.Title)
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
  zde doctor             every check on one screen: the daemon, the compositor,
                         the shell, notifications, the units, the manifests and
                         whether the screen lock could accept a password.
                         Non-zero when something failed, so it is worth piping
                         into a bug report
  zde desk list          the desks that exist right now
  zde desk switcher      open the picker; prints the list when no shell is up
  zde app launch NAME    run what this machine calls that (Mod+t, Mod+e)
  zde system lock        lock the screen (Mod+Ctrl+semicolon)
  zde app list           what it can start
  zde window jump-to [ID]
                         open the window picker (Mod+w); prints the list when
                         no shell is up - id, workspace, app, title - and with
                         an id goes straight to that window, desk and all
  zde desk switch NAME   bring a desk up on every monitor it owns
  zde workspace next|prev
                         one along this desk's band, stopping at its ends
  zde nav down|up        the window along the stack, else the desk beside this
  zde desk move-window next|prev
                         carry the focused window to the desk beside, and go
  zde desk move-window-to NAME
                         carry the focused window to that desk
  zde desk move-workspace-to NAME
                         hand this whole workspace to that desk, which is how
                         the regulars are made and how work comes back out
  zde desk next          the desk after this one, wrapping (regulars excluded)
  zde desk prev          the desk before this one, wrapping
  zde queue              what is waiting, oldest first
                         (id, urgency, desk, sender, text - tab separated)
  zde queue add TEXT     make something wait, on the desk you are on
  zde queue done ID      it is not waiting any more
  zde desk queue-jump    go to where the oldest thing waiting is
  zde desk regulars      the band that belongs to no desk (comms, music)
  zde desk last          go back to the desk you came from
  zde desk reconcile     make the workspace names true again
  zde desk snapshot [N]  write down the desk you are on, so you can ask for it
  zde desk apps [NAME]   what a desk declares: the address of each app, where
                         it goes, and where zinc says its state lives

Updating zde, which is two deliberate steps and nothing automatic
(docs/update.md). Use your own host name from your flake:

  cd /etc/nixos
  sudo nix flake update zde
  sudo nixos-rebuild switch --flake .#zdebox

The sandbox is its own pin in that flake: edit the zinc tag, then
nix flake update zinc, then rebuild. Two decisions rather than one, because the
thing that isolates every app should not move because the bar did.

Use boot rather than switch for a kernel or mesa change, and log out after a
niri one: a running compositor is not replaced by a rebuild, and its config is.
If the new one is worse, sudo nixos-rebuild switch --rollback, or pick the
generation above the newest in the boot menu.

Most of the action map (docs/model.md, section 6) is not wired yet; the
generated keybinds that call it land with roadmap 0.1.
`)
}
