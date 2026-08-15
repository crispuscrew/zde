// zde is the command line into zded. Every generated keybind that is not a
// niri native spawns this (common/keymap/keymap.yaml), so it is also the thing
// that has to say something useful when the daemon is not running.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	// Aliased because this file already has a signal(): the bluetooth widget's
	// column of dBm readings took the plain name first.
	sig "os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crispuscrew/zde/internal/apps"
	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/bt"
	"github.com/crispuscrew/zde/internal/clip"
	"github.com/crispuscrew/zde/internal/doctor"
	"github.com/crispuscrew/zde/internal/journal"
	"github.com/crispuscrew/zde/internal/keymap"
	"github.com/crispuscrew/zde/internal/link"
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
		complain(err)
		os.Exit(1)
	}
}

// complain is every error this command puts in front of a person: the one above
// that ends the process, and the one place that prints an error and carries on
// (deskApps).
//
// One door rather than a filter at each of the few dozen places that return an
// error, because of where the words come from. Very little of an error here is
// zde's own: `zcr` prints its refusal and it is carried whole (internal/zinc,
// Run); logind's message comes back through the bus; niri's comes back through
// the daemon, which hands its own errors over the socket as text and they are
// printed exactly as they arrived (internal/zded, Call); a manifest somebody
// hand-edited is answered by a YAML parser in several lines. Filtering at each
// call site is filtering the error paths that exist today, and the next one
// added is the one that forgets.
//
// What it must not cost is legibility. An error is read in order to fix
// something, so the path, the quoted name and the parser's caret line all have
// to survive - which is why this is Block and not the one-line filter a queue
// row takes (internal/attn).
func complain(err error) { fmt.Fprintln(os.Stderr, attn.Block(err.Error())) }

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
	case len(args) == 2 && args[0] == "system" && args[1] == "power":
		return powerMenu("")
	case len(args) == 3 && args[0] == "system" && args[1] == "power":
		// The name from the list, handed straight back - the same two arities
		// the window picker has, and for the same reason: with no surface to
		// ask, what the first form printed is what the second one takes.
		//
		// Nothing asks again here. The menu is where a person is told what a
		// reboot is about to cost and answers for it; from a terminal the
		// answer is having typed the word, which is the same bargain
		// `zde system bluetooth confirm ID yes` makes.
		return powerMenu(args[2])
	case len(args) == 2 && args[0] == "system" && args[1] == "quiet":
		// Mod+q. A toggle rather than a mode name, because the key is for the
		// moment somebody needs silence now: one press in, one press out.
		return attnMode("attn.quiet")
	case len(args) == 2 && args[0] == "system" && args[1] == "notif-center":
		return notifCenter()
	case len(args) == 2 && args[0] == "system" && args[1] == "notif-reach":
		return notifReach()
	case len(args) == 1 && args[0] == "attn":
		return attnMode("attn.mode")
	case len(args) == 2 && args[0] == "attn":
		return attnMode("attn.mode", args[1])
	case len(args) == 2 && args[0] == "system" && args[1] == "connections":
		return connections()
	case len(args) == 2 && args[0] == "net" && args[1] == "status":
		return netStatus()
	case len(args) == 3 && args[0] == "net" && args[1] == "connect":
		// Three words and never four. The fourth would be the password, and a
		// password in argv is readable by every account on the machine for as
		// long as the process lives (/proc/<pid>/cmdline) - so there is no
		// spelling of this command that can leak one. It comes from stdin.
		return netConnect(args[2])
	case len(args) == 3 && args[0] == "net" && args[1] == "forget":
		return netForget(args[2])
	case len(args) == 2 && args[0] == "net" && args[1] == "disconnect":
		return netDisconnect()
	case len(args) >= 2 && args[0] == "system" && args[1] == "bluetooth":
		return bluetooth(args[2:])
	case len(args) == 1 && args[0] == "keys":
		return keys()
	case len(args) == 1 && args[0] == "palette":
		return palette()
	case len(args) >= 2 && args[0] == "palette":
		// The name from the list, handed straight back - the same two arities
		// the window picker has, and for the same reason: the picker and the
		// choice are one question asked twice, and with nothing to ask it of,
		// what the first form printed is what the second one takes.
		//
		// Everything after the verb is the name, the way `queue add` takes its
		// text. Eight action names carry a space (`window.focus left`), and the
		// list prints the name in column one, so a form that took exactly one
		// argument refused a name copied off the row above it - and blamed the
		// person for mistyping it.
		return call("palette.run", strings.Join(args[1:], " "))
	case len(args) == 2 && args[0] == "clip" && args[1] == "history":
		return clipHistory()
	case len(args) == 3 && args[0] == "clip" && args[1] == "history":
		// The id from the list, spent as it is read - the two arities the window
		// picker has, and for the same reason: with no shell to ask, what the
		// first form printed is what the second one takes.
		return call("clip.history", args[2])
	case len(args) == 2 && args[0] == "clip" && args[1] == "clear":
		return clipClear()
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
	case len(args) >= 2 && args[0] == "ask":
		// Everything after the verb is the question, so it can be typed the way
		// it would be said: zde ask oneshot what is the capital of peru. The
		// same bargain `zde queue add` makes with quoting.
		return ask(args[1], strings.Join(args[2:], " "))
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
	case "report":
		return runReport()
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

// keys prints the keymap, which is the answer to "what does this desktop do".
//
// It is a file rather than something rendered here on purpose: the binds a
// machine actually has were generated at build time from the same keymap
// (nix/zde-config.nix), so the list somebody reads and the binds niri loaded
// come from one build and cannot disagree. A `zde` that rendered its own could
// be a version behind the config and would say so confidently.
//
// Mod+slash spawns a terminal on this (zde.apps.help). Printing to a stderr no
// keypress has is what it did before, which on a desktop where most keys are
// still silent is the worst key to have chosen for that.
func keys() error {
	path := keymap.TextPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("no keymap at %s: layer 1 installs it, so this is a zde "+
			"whose home-manager module has not been activated", path)
	}
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
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
		complain(unanswered)
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
	// And how many, once it is more than the one a session has. "yes" used to
	// be the whole answer, and under a flood of connections that subscribe and
	// never read it was a true sentence about the wrong connection: the picker
	// never appeared and status said a shell was listening.
	shell := yesno(st.Shell)
	if st.Listeners > 1 {
		shell = fmt.Sprintf("%s (%d listening)", shell, st.Listeners)
	}
	fmt.Printf("shell      %s\n", shell)
	fmt.Printf("notify     %s\n", yesno(st.Notifications))
	// Layer 2: whether the session can run a sandboxed app at all. It is the
	// whole diagnosis for a desk whose apps do nothing, and it is a fact about
	// the session's PATH rather than this terminal's, which is why zded is the
	// one asked.
	fmt.Printf("zinc       %s\n", yesno(st.Zinc))
	// The attn mode, next to the queue it governs. This is the answer to "why
	// has nothing arrived all afternoon", and without it the only way to find
	// out is to send yourself a notification and watch it not appear.
	fmt.Printf("attn       %s\n", st.Mode)
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
	// The cost of a fail-closed answer, said rather than left to be discovered
	// as a history that will not fill up and a session that stops drawing
	// cards. It is only ever non-zero on a machine that declares a private
	// desk, and it says which of the two things to fix: a desk nothing has
	// named yet, or a compositor nothing can read.
	if st.Unplaced > 0 {
		fmt.Printf("unplaced   %d arrivals kept in memory and drew no card: no desk could be named for them, and one here is private\n", st.Unplaced)
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

// version is this build of zde, set at link time by the derivation that builds
// it (nix/zde.nix passes -X main.version to every subPackage). A `go build`
// with no ldflags says "dev" rather than claiming a release it is not, which is
// the same bargain cmd/zded makes.
var version = "dev"

// runReport writes the state snapshot: what this machine looked like, for
// somebody who will read it off a disk that will not boot (internal/doctor,
// report.go).
//
// A verb of its own and not a mode of `zde doctor`, and the three differences
// are the argument. What doctor produces is a screen of verdicts for somebody
// sitting in front of a working session; this produces a file of readings for
// somebody with no session at all, days later, on another machine. Doctor's
// exit status is a verdict - non-zero when a check failed - and this one's must
// not be, because the unit that writes it at login would then go `failed` on
// exactly the machines it exists for, putting a red herring in `systemctl
// --failed` on the morning somebody is already debugging a black screen. And a
// flag on doctor would have to mean "print differently and also write a file
// and also stop meaning what the exit status meant", which is two commands
// wearing one name.
//
// What it does not do is gather twice: the whole of doctor is a section of the
// file, judged off one Gather (internal/doctor, WriteReport).
//
// It prints the report when it could not write one, which is the shape every
// surface-backed verb in this file already has - `zde desk switcher` prints the
// list when no shell is up. A machine with nowhere to write is a machine where
// somebody typed this into a terminal, and the answer is still the answer.
func runReport() error {
	path, text, err := doctor.WriteReport(doctor.Self{Zde: version})
	if err == nil {
		fmt.Println("wrote", path)
		return nil
	}
	fmt.Print(text)
	// Not an error the process exits on. There is nothing wrong with the report
	// - it is above - and a non-zero exit here would make the one command that
	// still works on a broken machine look like another thing that is broken.
	fmt.Fprintf(os.Stderr, "\nno file written: %s\n"+
		"%s belongs to root and is made by layer 0 when zde.debug is on (nix/system.nix).\n", err, doctor.ReportDir)
	return nil
}

// ask is the quick LLM (docs/vision.md, section 2), in the two shapes it has:
// a surface, which is what the keys press, and a terminal.
//
// oneshot is one question and one answer, and a question typed here is answered
// here, never in a popup: it was typed into a terminal, so the answer belongs
// where it can be read back, piped and kept - a popup would take it somewhere
// none of that is true. Without a question there is nothing to type into, so
// the key asks the shell for a window, and says so plainly when there is no
// shell to ask.
//
// panel is the other verb and it names the other thing, with or without a
// question. The panel is the window that stays open and carries what was asked
// in it into the next question, and none of that exists in a terminal - so a
// question here opens the panel with it already asked rather than printing an
// answer, which is what oneshot is for and what a script that wants text should
// still use. It used to run a provider oneshot and ignore the word "panel"
// entirely: harmless while nobody relied on it, and a verb that quietly does
// something else is worse the longer it is left.
//
// local and escalate are terminal verbs only. They are the tiers the surface
// reaches with a key of its own, and from here they are how a private question
// or a hard one gets asked without a shell at all.
func ask(kind, question string) error {
	if question == "" {
		typed, err := questionOnStdin()
		if err != nil {
			return err
		}
		question = typed
	}
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	switch kind {
	case "oneshot":
		if question == "" {
			return askSurface(c, "ask.oneshot", "")
		}
		return askRun(c, zded.TierProvider, question)
	case "panel":
		return askSurface(c, "ask.panel", question)
	case zded.TierLocal, zded.TierEscalate:
		if question == "" {
			return fmt.Errorf("zde ask %s takes the question, in an argument or on stdin: say what to ask", kind)
		}
		return askRun(c, kind, question)
	}
	return fmt.Errorf("zde ask takes oneshot, panel, local or escalate, not %q", kind)
}

// questionOnStdin is the question when it was piped in rather than written as
// an argument, and empty when there is nothing there to read.
//
// It exists because of where a question ends up. The tier is handed it on
// stdin, zded never puts it in an argument, and the one place it does reach a
// command line is this process's own - where `ps` shows it to anybody on the
// machine for as long as the answer takes. That is the wrong property for the
// tier this CLI calls private, so `zde ask local < note` and
// `something | zde ask local` are how a question stays between the two
// processes that need it.
//
// A character device is a terminal or /dev/null, and both mean there is nothing
// waiting: reading the first would hang on a key nobody is going to press, and
// the second is what stdin is for the process a keybind spawns.
func questionOnStdin() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, questionMax+1))
	if err != nil {
		return "", err
	}
	if len(raw) > questionMax {
		// Bounded, because this becomes one line of JSON to the daemon that
		// answers every keypress. Something bigger is a job for a program with
		// tools and files, which ask deliberately is not (docs/vision.md).
		return "", fmt.Errorf("that is more than %d KiB, which is a document rather than a question", questionMax>>10)
	}
	return strings.TrimSpace(string(raw)), nil
}

// questionMax is generous for a question with something pasted into it, and far
// below anything that would make a daemon read a file it did not want.
const questionMax = 64 << 10

// askSurface asks the shell to open one, with the question already asked where
// there is one. Nothing to print when no shell answers: the switcher can fall
// back to its list, and a question nobody has typed yet has no list - so this
// says how to ask from here instead, which is the only useful thing left to
// say.
//
// A question and no shell is the case worth being loud about, and it is why
// this refuses rather than quietly asking the tier itself. Falling back to
// printing an answer would be the same verb doing two different things
// depending on whether a shell happened to be up, which is exactly what nobody
// can write a script against - so the failure says what did not happen and
// names the verb that does work here.
func askSurface(c *zded.Client, method, question string) error {
	// Sent as no argument rather than an empty one: a surface opened to type
	// into and a surface opened with an empty question are the same thing, and
	// zded refuses the second (internal/zded, server.go).
	var args []string
	if question != "" {
		args = []string{question}
	}
	var shown bool
	if err := c.Call(method, &shown, args...); err != nil {
		return err
	}
	if shown {
		return nil
	}
	if question != "" {
		return errors.New("no shell to draw the ask panel, so that question was not asked: " +
			"`zde ask oneshot` takes the same question and answers here")
	}
	return errors.New("no shell to draw the ask window: ask from a terminal instead, " +
		"as `zde ask oneshot what is the capital of peru`")
}

// askRun asks, and prints the answer as it arrives. This is the whole text
// fallback: no surface, no history, one answer on stdout.
//
// The pieces arrive as events on this connection (internal/zded, ask.go), so
// the printing is a write per piece and not a wait for the last one - which is
// what a person watching a terminal wants, and what a test without a compositor
// can watch.
func askRun(c *zded.Client, tier, question string) error {
	if err := c.Call(zded.MethodAskRun, nil, tier, question); err != nil {
		return err
	}
	ended := true
	for {
		ev, err := c.NextEvent()
		if err != nil {
			// The connection went, which is how a stopped answer arrives here:
			// zded closes a connection it cannot write a whole line to rather
			// than leaving the reader waiting for an end that also failed to
			// send. Said as what happened, since "waiting for an event" is not
			// what somebody watching an answer appear thinks they are doing.
			return fmt.Errorf("the answer stopped coming: %w", err)
		}
		if ev.Kind != zded.EventAskText {
			continue
		}
		// Filtered first and then asked about, so that a piece which was
		// nothing but control characters is a piece that printed nothing - and
		// does not leave this thinking it ended a line it never wrote.
		//
		// Text and not Block: this arrives in pieces, and a piece is not a whole
		// message to indent the lines of. zded hands the bytes over as the tier
		// produced them and counts them against its own cap while it does
		// (internal/zded, pump), so the filtering is here, where there is no
		// bookkeeping to disturb and the reader is known to be a terminal.
		if said := attn.Text(ev.Text); said != "" {
			fmt.Print(said)
			ended = strings.HasSuffix(said, "\n")
		}
		if !ev.Done {
			continue
		}
		if !ended {
			// A tier that streams tokens has no reason to end on a newline, and
			// a shell prompt landing at the end of the answer reads as part of
			// it.
			fmt.Println()
		}
		if ev.Error != "" {
			return errors.New(ev.Error)
		}
		return nil
	}
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

// attnMode prints the mode the session is in, whether it was asked to change
// it or only to say. One printed line for all three verbs - read, set, toggle -
// so that Mod+q and a person typing get the same answer, and so that whatever
// reads it back does not have to know which one was run.
func attnMode(method string, args ...string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var a zded.Attn
	if err := c.Call(method, &a, args...); err != nil {
		return err
	}
	fmt.Println(a.Mode)
	return nil
}

// notifReach puts the keyboard on the newest popup, which is the only way one
// ever holds it (internal/zded, EventAttnReach).
//
// Nothing to fall back to printing, unlike the center: what this asks for is not
// a list, it is the keys going somewhere else for a moment. So the failure is a
// sentence, and it says both things it could mean at once - no popup is up, or
// no shell is running to have drawn one - because from the far side of a
// keypress those are one fact, and the next place to look is the same either way.
func notifReach() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var r zded.Reach
	if err := c.Call("attn.reach", &r); err != nil {
		return err
	}
	if r.Reached {
		return nil
	}
	fmt.Println("no popup to reach: what has arrived is in the notification center")
	return nil
}

// notifCenter asks for the center. The same bargain as the desk switcher: with
// a shell listening this prints nothing and a surface appears, and without one
// it prints the history, so that Mod+n on a session whose shell has died still
// answers "what did I miss" - and so that what arrived is greppable at all.
func notifCenter() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var center zded.Center
	if err := c.Call("attn.center", &center); err != nil {
		return err
	}
	if center.Shown {
		return nil
	}
	if len(center.Notifications) == 0 {
		// Not an error, and not silence: a session where nothing has arrived is
		// the ordinary state of a fresh login, and a key that printed nothing
		// would be indistinguishable from one that failed.
		fmt.Println("nothing has arrived yet")
		return nil
	}
	// id, urgency, when, sender, what became of it, text - tab separated, text
	// last because it is the only field that can be long. The weekday rather
	// than a date: the history is bounded at a day or two of use, so a weekday
	// tells a person which one it was without a column nobody reads.
	for _, r := range center.Notifications {
		fmt.Printf("%d\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, urgentMark(r.Urgent), r.At.Format("Mon 15:04"), dash(r.From), became(r), r.Text)
	}
	return nil
}

// became is what happened to a notification after it arrived, in one word.
// "silent" is the one worth having: it says a mode kept this out of the queue,
// which is the difference between an app that stopped sending and a session
// that stopped listening.
func became(r attn.Record) string {
	switch {
	case r.Dismissed:
		return "done"
	case r.Queued:
		return "waiting"
	default:
		return "silent"
	}
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
	//
	// Filtered on the way out as well as on the way in, which is the one place
	// in this command where that is worth the line. `zde queue add` refuses a
	// control character and a notification's summary is cleaned before it is
	// queued (internal/zded, checkQueueText; internal/attn, oneLine), so
	// everything this session put on the queue is already printable - but the
	// queue outlives the session, and what it is replayed from is a file of
	// JSON lines. A line put there by hand is checked for an id and a text and
	// nothing else (internal/journal, apply), so the promise that a queue item
	// is one printable line was a promise the write path kept alone.
	//
	// Writing that file takes this user's own uid, so this is not a way in; it
	// is the difference between a promise the code keeps and one it only makes.
	// Kept here rather than at the replay because this is what the promise is
	// about: every reader of the queue is line-based and column-based, and this
	// is the reader that is a terminal.
	for _, it := range q {
		fmt.Printf("%d\t%s\t%s\t%s\t%s\n",
			it.ID, urgentMark(it.Urgent), dash(attn.Line(it.Desk)), dash(attn.Line(it.From)), attn.Line(it.Text))
	}
	return nil
}

// urgentMark is the urgency column, in the one character both listings use: the
// queue and the notification center are two views of the same arrivals, and two
// spellings of "this one said it was urgent" would eventually disagree.
func urgentMark(urgent bool) string {
	if urgent {
		return "!"
	}
	return "."
}

// bluetooth is the radio: what is around, what is paired, and the pairing
// question that has to be answered by a person (internal/bt).
//
// All of it prints text, and that is deliberate. It is the half that works
// without a compositor - on a machine whose shell has died, over ssh, in the
// smoke test - and it is the only surface for it until the connections widget
// exists.
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
		// The question's id, then the answer. Two words rather than one because
		// an answer that does not name its question is an answer to whatever is
		// waiting when it lands - which is not always what was read
		// (internal/bt/agent.go, Answer). The id is printed with the question.
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

// printRadio is the whole state on a few lines: the adapter as key and value
// the way `zde status` does it, then one device per line, tab separated with
// the long field last like every other list zde prints.
func printRadio(st bt.State) {
	if !st.Adapter.Present {
		// A machine with no radio says so and stops. Not an error: a desktop
		// that never asked for bluetooth is the ordinary case, and a key that
		// exits non-zero on it is one nobody trusts afterwards.
		fmt.Printf("adapter    none  %s\n", st.Adapter.Why)
		return
	}
	fmt.Printf("adapter    %s  %s\n", dash(st.Adapter.Name), st.Adapter.Address)
	// Where zde stands with BlueZ, and the line only worth printing when the
	// answer is bad. BlueZ has one default agent and gives it to whoever asked
	// last, so anything else on this machine can take over the answering of
	// pairing questions - silently, since nothing tells the agent it displaced.
	// A machine in that state should be able to say so.
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
	// address, flags, signal, name. The flags are three fixed positions -
	// paired, trusted, connected - so that a column stays a column: "p-c" is a
	// device you agreed to that is connected and still gets asked about.
	for _, d := range st.Devices {
		fmt.Printf("%s\t%s\t%s\t%s\n", d.Address, deviceFlags(d), signal(d), dash(d.Name))
	}
}

// printQuestion is the pairing question, what it is really asking, and how to
// answer it. Three lines, because the last one is a command to run and burying
// it at the end of a long line is how it gets missed.
//
// The wording is per kind and that is the point of having kinds. "Does it
// match?" printed over a question with nothing to match is how people learn to
// say yes without reading, on the one surface whose whole job is to stop that.
func printQuestion(req bt.Request) {
	who := req.Device
	if req.Name != "" {
		who += "  " + req.Name
	}
	switch req.Kind {
	case bt.KindDisplay:
		// Nothing to answer: typing it on the device is the answer. The count is
		// what says somebody is actually typing.
		fmt.Printf("asking     %s\n", who)
		typed := ""
		if req.Entered > 0 {
			typed = fmt.Sprintf("  (%d typed so far)", req.Entered)
		}
		fmt.Printf("           type %s on it%s\n", req.Passkey, typed)
	case bt.KindService:
		fmt.Printf("asking     %s\n", who)
		fmt.Printf("           wants %s\n", serviceWords(req))
		fmt.Printf("           allow it: zde system bluetooth confirm %s yes\n", req.ID)
	case bt.KindAuthorize:
		// Just works pairing: there is no number, so the only thing a person can
		// check is whether they are the one who started it. Say that, rather
		// than asking them to compare something that does not exist.
		fmt.Printf("asking     %s wants to pair\n", who)
		fmt.Println("           there is nothing to compare: say yes only if you started this")
		fmt.Printf("           zde system bluetooth confirm %s yes\n", req.ID)
	default:
		fmt.Printf("asking     %s\n", who)
		fmt.Printf("           it should be showing %s\n", req.Passkey)
		fmt.Printf("           if it is: zde system bluetooth confirm %s yes\n", req.ID)
	}
}

// serviceWords is what a device is asking to do, in words, with the raw UUID
// behind it for whoever is reading a bug report.
func serviceWords(req bt.Request) string {
	if req.Service == "" {
		return "a service this does not recognise: " + req.UUID
	}
	return req.Service + "  (" + req.UUID + ")"
}

// because turns a reason into a clause, and nothing into nothing: a line that
// ends in a bare colon reads as something missing.
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

// signal is the reading in dBm, and a dash for a device that is remembered
// rather than in the room: nothing is heard from it, which is not the same as
// hearing it faintly.
func signal(d bt.Device) string {
	if d.RSSI == 0 {
		return "-"
	}
	return strconv.Itoa(int(d.RSSI))
}

// pairDevice starts a pairing and stays with it.
//
// It has to stay: pairing is asynchronous in the daemon, because the thing it
// waits for is a person comparing six digits on two screens - which is longer
// than a socket round trip has any business being. So this asks, watches, shows
// the question when it arrives, and says how it ended.
func pairDevice(c *zded.Client, addr string) error {
	if err := c.Call("bluetooth.pair", nil, addr); err != nil {
		return err
	}
	// The question that was put to this terminal, by id: an answer is only ever
	// given to the question that was printed here, and if the one waiting has
	// become a different one, this stops rather than answering it.
	asked := ""
	return watchRadio(c, bt.WatchFor, func(st bt.State) (bool, error) {
		if d, found := deviceIn(st, addr); found && d.Paired {
			fmt.Println("paired")
			// Said out loud, because it is the difference between this and
			// every other bluetooth UI: pairing is not trust, so the device
			// will be asked about again when it reconnects.
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
				// Nowhere to ask: a keybind's stdin is not a terminal. The
				// question is on screen and the command to answer it is on the
				// line under it, which is all this can honestly do.
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

// connectDevice opens the link and waits a little to say whether it opened. The
// daemon starts it in the background for the same reason pairing is started
// there - a headset takes seconds - so the answer is in the next reading.
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
			// A device that is paired and not trusted is asked about when it
			// connects. That is what untrusted means (internal/bt/agent.go).
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

// watchRadio asks for the state until something has happened or the time runs
// out. Both long verbs need it, and neither can be answered by the call that
// started it.
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
			// Not a failure of the thing itself: the attempt is the daemon's
			// and carries on. Say where to look.
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

// answerHere asks the person at this terminal, and only at a terminal: stdin
// from a keybind is not one, and a prompt nobody can answer is a command that
// hangs holding a pairing open.
//
// Two things make it safe to prompt at all. The answer names the question, so a
// keystroke that arrives after that question died is refused by the daemon
// rather than spent on whatever is waiting now. And the prompt does not outlive
// the question: reading with no deadline left a "[y/N]" on a terminal for as
// long as somebody left the window open, and the answer to it went somewhere.
//
// A question with nothing to answer is not put here - a passkey to type on the
// other device is answered by typing it there.
func answerHere(c *zded.Client, req bt.Request) (bool, error) {
	if req.Kind == bt.KindDisplay {
		return false, nil
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false, nil
	}
	fmt.Print("           " + prompt(req) + " [y/N] ")

	// The read happens in a goroutine because there is no portable way to give
	// stdin a deadline. It is left behind when the wait runs out, which costs a
	// blocked goroutine in a process that is about to exit - and buys a command
	// that comes back.
	typed := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		typed <- line
	}()
	var line string
	select {
	case line = <-typed:
	case <-time.After(bt.AnswerWait):
		// The question is gone by now: the agent refuses it at exactly this
		// point. Saying so beats leaving a prompt that answers nothing.
		fmt.Println()
		return true, errors.New("nobody answered here in time, so it was refused")
	}
	// Anything that is not a yes is a no, which is the way round a pairing
	// question has to default.
	answer := "no"
	if s := strings.ToLower(strings.TrimSpace(line)); s == "y" || s == "yes" {
		answer = "yes"
	}
	return true, c.Call("bluetooth.confirm", nil, req.ID, answer)
}

// prompt is the question in the form of a question, per kind. A person who is
// asked "does it match?" about something with nothing to match learns that the
// words do not mean anything, which is the habit this surface exists to not
// build.
func prompt(req bt.Request) string {
	switch req.Kind {
	case bt.KindService:
		return "allow it?"
	case bt.KindAuthorize:
		return "did you start this?"
	default:
		return "is it showing " + req.Passkey + "?"
	}
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

// connections opens the connections widget. The same bargain as the desk
// switcher: with a shell listening this prints nothing, and without one it
// prints the link and what is in range, so that the key does something on a
// session whose shell has died - and so that this is testable on a machine
// with no compositor at all.
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
	// The link first, because it is the answer to the question the key is
	// usually pressed to ask, and it is the only line on a machine with no
	// NetworkManager and no radio.
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
	// signal, security, note, ssid - tab separated like every other list zde
	// prints, with the only field that can be long last. The note is where you
	// are, or that joining will not ask for anything.
	for _, n := range cn.Networks {
		fmt.Printf("%d\t%s\t%s\t%s\n", n.Signal, security(n), note(n), n.SSID)
	}
	return nil
}

// clipHistory asks for the clipboard history (Mod+v). The same bargain as the
// desk switcher: with a shell listening this prints nothing and a surface
// appears, and without one it prints the rows, so the key does something on a
// session whose shell has died - and so that an entry can be put back from a
// terminal at all.
//
// The whole of an entry is never printed, only the preview zded sends (up to
// clip.PreviewMax characters of it): the text stays in the daemon until somebody
// asks for it by id, so what a list can put into a terminal's scrollback is
// bounded by the preview and not by what was copied.
//
// Said exactly, because the loop below does print every row. For anything short
// - a password, a token, a one-line address - the preview is the whole entry, so
// this is fifty previews on a screen and not fifty whole clipboard entries.
// Worth knowing before running it in front of somebody: this path is reached
// whenever the shell does not acknowledge within ackWait, which is a slow shell
// as well as a dead one, and `zde clip clear` is the answer if it happens where
// it should not have.
func clipHistory() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var h zded.Clips
	if err := c.Call("clip.history", &h); err != nil {
		return err
	}
	if h.Shown {
		return nil
	}
	if len(h.Entries) == 0 {
		// Not an error, and not silence. A fresh session, or one where
		// everything has expired, and a key that printed nothing would be
		// indistinguishable from one that failed.
		//
		// And the other reason a history is empty, which is the one worth doing
		// something about: nothing is watching the clipboard at all. The two
		// look identical from a keyboard, so the daemon says which it is.
		if h.Why != "" {
			fmt.Println("nothing is watching the clipboard: " + h.Why)
			return nil
		}
		fmt.Println("nothing copied recently: entries last " + clip.TTL.String())
		return nil
	}
	// id, when, what it is, and the text - tab separated, the id in column one
	// because it is what goes back to `zde clip history ID`, and the only field
	// that can be long last. A row that is not text says why instead of showing
	// content it does not have.
	for _, e := range h.Entries {
		said := e.Preview
		if e.Kind != clip.KindText {
			said = e.Why
		}
		fmt.Printf("%d\t%s\t%s\t%s\n", e.ID, e.At.Format("15:04"), e.Kind, said)
	}
	return nil
}

// clipClear forgets the history now rather than in fifteen minutes, and says
// how much went: "it did something" and "there was nothing there" are different
// answers, and this is a verb somebody runs before handing over a screen.
func clipClear() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var gone int
	if err := c.Call("clip.clear", &gone); err != nil {
		return err
	}
	if gone == 1 {
		fmt.Println("forgot 1 entry")
		return nil
	}
	fmt.Printf("forgot %d entries\n", gone)
	return nil
}

// palette asks for the palette. The same bargain as the desk switcher: with a
// shell listening this prints nothing, and without one it prints the list, so
// that the key does something on a session whose shell has died - and so that
// what this machine can be asked to do is greppable at all.
func palette() error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var p zded.Palette
	if err := c.Call("palette.list", &p); err != nil {
		return err
	}
	if p.Shown {
		return nil
	}
	// name, key, why not, what it does - tab separated, the name in column one
	// because it is what goes back to `zde palette NAME`, and the description
	// last because it is the only field that can be long. A dash is "nothing
	// here", which in the third column means the action works.
	for _, a := range p.Actions {
		fmt.Printf("%s\t%s\t%s\t%s\n", a.Name, dash(a.Key), dash(a.Why), a.Desc)
	}
	return nil
}

// powerMenu opens the power menu, or runs one row of it.
//
// The same bargain as the desk switcher: with a shell listening this prints
// nothing and a surface appears, and without one it prints the menu, so that
// Mod+Shift+x does something on a session whose shell has died - which is one
// of the sessions somebody most wants to log out of.
//
// Printed rather than tab separated, unlike every list in this CLI, because
// this is not a list of ids to cut a column out of: it is five rows and the
// half worth reading is underneath each one - what it is about to cost, and
// what would stop it. The name is still first, so `zde system power reboot` is
// what the row says.
func powerMenu(what string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	if what != "" {
		var said string
		if err := c.Call("system.power", &said, what); err != nil {
			return err
		}
		// In the present tense, because logind answers when it has taken the
		// request: the machine goes some moments after this line.
		fmt.Println(said)
		return nil
	}
	var menu zded.Power
	if err := c.Call("system.power", &menu); err != nil {
		return err
	}
	if menu.Shown {
		return nil
	}
	for _, ch := range menu.Choices {
		// The mark is the queue's, in the one character both surfaces use: "!"
		// is the row the menu asks twice about, which is also the row with
		// something under it worth reading.
		fmt.Printf("%-9s %s  %s\n", ch.Name, asksFirst(ch), ch.Desc)
		if ch.Why != "" {
			fmt.Println(powerIndent + ch.Why)
		}
		for _, cost := range ch.Costs {
			fmt.Println(powerIndent + cost)
		}
	}
	return nil
}

// powerIndent lines the continuations up under the description, the way the
// bluetooth readout does: what a row costs belongs to that row, and a line
// starting in column one reads as another choice.
//
// Thirteen, counted off the format above rather than guessed: `%-9s` is nine,
// then a space, then the one-character mark, then two more spaces, so the
// description starts at column fourteen and a continuation has thirteen to
// fill.
const powerIndent = "             "

func asksFirst(ch zded.PowerChoice) string {
	if ch.Confirm {
		return "!"
	}
	return "."
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

// netStatus is the one line the bar draws, for whoever has no bar.
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

// linkLine is the link in words, and the same words the bar uses so that the
// two never look like they are talking about different machines.
func linkLine(st link.Status) string {
	switch st.Kind {
	case link.KindWifi:
		if st.SSID == "" {
			// On wifi, and the access point would not say its name. Rare, and
			// not worth claiming a network called "".
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

// netConnect joins a network, and asks for the password only when joining
// needs one: an open network, or one NetworkManager already has a profile for,
// asks nobody anything.
//
// The password never becomes an argument to anything (see run, and
// internal/link on why not nmcli). It goes from the terminal into one D-Bus
// message and is not written down on the way.
func netConnect(ssid string) error {
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	var networks []link.Network
	// A list that cannot be read is not a reason to refuse: zded says why when
	// the join itself fails, in words about the network rather than about a
	// list nobody asked for.
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

// netForget drops a saved network. It is how a password that has changed gets
// typed again: nothing asks for one while NetworkManager has a profile, so a
// network saved with the wrong password is otherwise unjoinable from zde for
// good.
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

// readSecret takes a password from stdin, with the terminal's echo off while it
// is typed.
//
// The prompt goes to stderr because stdout is the command's answer and a prompt
// is not - so `zde net connect x > log` still asks, and the log still holds only
// what happened.
//
// Echo off because a password on the screen is a password in the scrollback,
// and in tmux's buffer, and in whatever is recording the terminal. If this is
// killed mid-prompt the terminal is left quiet until `stty sane`, which is the
// same deal every password prompt on the machine makes.
func readSecret(prompt string) (string, error) {
	restore := hush(os.Stdin)
	defer restore()
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	// The newline the person's Enter could not echo, so the next thing printed
	// does not land on the prompt.
	fmt.Fprintln(os.Stderr)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// hush turns a terminal's echo off and answers with how to put it back. A
// stdin that is not a terminal - a pipe from `pass show wifi` - echoes nothing
// to begin with, so there is nothing to turn off and nothing to restore.
//
// Ctrl+C at the prompt puts it back too. Interrupting a password prompt is an
// ordinary thing to do, and a terminal left echoless afterwards is a terminal
// somebody has to know `stty sane` to get out of - every password prompt on the
// machine traps this, and the comment here used to claim zde did while it did
// not. The signal is then raised again with the handler gone, so the shell that
// started this still sees a process that was interrupted rather than one that
// chose to exit.
func hush(f *os.File) func() {
	fd := int(f.Fd())
	before, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}
	}
	quiet := *before
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &quiet); err != nil {
		return func() {}
	}
	restore := func() {
		unix.IoctlSetTermios(fd, unix.TCSETS, before) //nolint:errcheck // nothing useful to do about a terminal that will not take its settings back
	}

	interrupted := make(chan os.Signal, 1)
	sig.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	over := make(chan struct{})
	go func() {
		select {
		case caught := <-interrupted:
			restore()
			sig.Stop(interrupted)
			// With nothing listening any more this goes back to killing the
			// process, which is what it was always going to do.
			syscall.Kill(os.Getpid(), caught.(syscall.Signal)) //nolint:errcheck // the process is on its way out
		case <-over:
		}
	}()
	return func() {
		close(over)
		sig.Stop(interrupted)
		restore()
	}
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
  zde report             write down what this machine looks like: the graphics
                         device and whether niri ever reached a renderer on it,
                         the versions, the hardware, and the whole of doctor.
                         It lands in /var/log/zde, 0600, one file per boot, for
                         the failure nothing else survives - a black screen with
                         no terminal to ask anything from. Needs zde.debug on;
                         with nowhere to write it prints the report instead.
                         No notification text, no clipboard, no queue, no window
                         titles, and nothing about a desk declared private
  zde desk list          the desks that exist right now
  zde desk switcher      open the picker; prints the list when no shell is up
  zde app launch NAME    run what this machine calls that (Mod+t, Mod+e)
  zde system lock        lock the screen (Mod+Ctrl+semicolon)
  zde system power       the power menu (Mod+Shift+x): lock, log out, suspend,
                         reboot, power off. Prints the five when no shell is
                         up, each with what it is about to cost underneath -
                         the windows that close, what the notification center
                         is holding that the queue never got, anybody else
                         logged in here, and whatever is holding a suspend off
  zde system power NAME  run one of them, by the name in column one. The menu
                         is where the three that end things are asked about;
                         typing the word here is the answer, the same bargain
                         zde system bluetooth confirm makes. The lock runs the
                         same locker zde system lock does, and the other four
                         are logind's - so a refusal, an inhibitor holding
                         sleep or a second person logged in, comes back as a
                         refusal and not as silence
  zde system connections open the connections widget (Mod+Shift+c); prints the
                         link and what is in range when no shell is up -
                         signal, security, note, network - and says so plainly
                         on a machine with no NetworkManager
  zde net status         what the link is right now: wifi and its signal,
                         wired, nothing, or no NetworkManager to ask
  zde net connect SSID   join a wifi network. The password is read from stdin,
                         never from the command line - anybody with an account
                         on this machine can read a running process's
                         arguments - and it is only asked for when the network
                         is secured and NetworkManager has no profile for it
  zde net forget SSID    drop what NetworkManager has saved for a network,
                         which is how a changed password gets typed again -
                         nothing asks for one while a profile is there
  zde net disconnect     drop the wifi link, keeping the saved profile
  zde app list           what it can start
  zde system bluetooth   the radio, and what is around it: the adapter, then
                         one device per line - address, flags, signal, name.
                         The flags are three positions: paired, trusted,
                         connected, a dash where it is not. Says "adapter none"
                         on a machine with no radio rather than failing
  zde system bluetooth power on|off
                         the radio itself. Off unless somebody said otherwise,
                         and nothing here turns it on as a side effect
  zde system bluetooth scan on|off
                         look for what is around, and stop again: a radio left
                         scanning is one that keeps announcing itself
  zde system bluetooth pair ADDR
                         pair, which asks before anything happens: the passkey
                         is shown here and has to match what the device shows.
                         Pairing does not trust
  zde system bluetooth confirm ID yes|no
                         answer one pairing question, by the id printed with it.
                         The id is not decoration: questions come and go on
                         their own - one expires after 45 seconds, another
                         arrives - and an answer that did not name one would be
                         spent on whichever is waiting when it lands
  zde system bluetooth connect|disconnect ADDR
                         open or drop the link to a device already paired
  zde system bluetooth trust|untrust ADDR
                         trusted means it reconnects and uses its services
                         without asking again - a decision, never a side effect
                         of having paired once
  zde system bluetooth forget ADDR
                         remove it: the key, the trust, the lot
  zde window jump-to [ID]
                         open the window picker (Mod+w); prints the list when
                         no shell is up - id, workspace, app, title - and with
                         an id goes straight to that window, desk and all
  zde ask oneshot [QUESTION]
                         the quick LLM (Mod+a). With a question typed here the
                         answer arrives here, streamed as it comes; without one
                         it opens the popup, and says so when no shell can
  zde ask panel [QUESTION]
                         the window that stays open to keep asking
                         (Mod+Shift+a), carrying what was asked in it into the
                         next question. With a question it opens the panel with
                         that one asked and nothing in front of it, so the
                         answer arrives in the window and not here; with no
                         shell to draw one, nothing is asked and it says so.
                         Use oneshot for an answer on stdout
  zde ask local QUESTION the private tier, which is the one that runs with no
                         network
  zde ask escalate QUESTION
                         the tier kept for a question the first two got wrong.
                         Every tier is a command this machine was configured
                         with (zde.ask.tiers), and an unset one is unset:
                         nothing here talks to anybody's API.
                         A question written as an argument is visible in ps for
                         as long as the answer takes, so all four of these read
                         it from stdin when it is piped in instead:
                         zde ask local < the-question
  zde clip history [ID]  what was copied recently (Mod+v); prints the rows when
                         no shell is up - id, when, what it is, and the text -
                         and with an id puts that entry back on the clipboard.
                         Text only, in memory only, and every entry expires:
                         nothing an app marked as a secret is ever recorded, and
                         an image or a file is a row saying so rather than
                         content
  zde clip clear         forget the history now rather than when it expires
  zde keys               the whole keymap, one key per line (Mod+slash opens
                         this in a terminal)
  zde palette [NAME]     every action by name (Mod+semicolon); prints the list
                         when no shell is up - name, key, why it would do
                         nothing, what it does - and with a name runs that one,
                         doing exactly what its key would do
  zde desk switch NAME   bring a desk up on every monitor it owns, and start
                         what its manifest declares
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
  zde attn [MODE]        the attn mode, or set it: work queues everything,
                         focus queues only what the sender called urgent,
                         quiet queues none of it. All three keep the lot in
                         the notification center, so a mode changes what
                         interrupts you and never what happened
  zde system quiet       toggle quiet (Mod+q), which is the same mode by the
                         key you reach for when you need silence now
  zde system notif-center
                         what arrived (Mod+n); prints the history when no
                         shell is up - id, urgency, when, sender, whether it
                         is waiting, done or silent, and the text
  zde system notif-reach put the keyboard on the newest popup (Mod+Ctrl+n), so
                         its sender's buttons can be pressed. A popup never
                         takes the keyboard on its own, which is why this key
                         exists; says so when there is no popup to reach
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
