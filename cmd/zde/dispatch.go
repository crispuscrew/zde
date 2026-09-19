package main

import (
	"fmt"
	"strings"
)

func run(args []string) error {
	switch {
	case len(args) == 3 && args[0] == "app" && args[1] == "launch":
		return launch(args[2])
	case len(args) == 2 && args[0] == "system" && args[1] == "lock":
		if err := launch("lock"); err != nil {
			return fmt.Errorf("nothing to lock the screen with: %w", err)
		}
		return nil
	case len(args) == 2 && args[0] == "system" && args[1] == "lock-preset":
		return lockPreset()
	case len(args) == 2 && args[0] == "system" && args[1] == "power":
		return powerMenu("")
	case len(args) == 3 && args[0] == "system" && args[1] == "power":
		return powerMenu(args[2])
	case len(args) == 2 && args[0] == "system" && args[1] == "quiet":
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
		// Never accept a password in argv, where other accounts can read it.
		return netConnect(args[2])
	case len(args) == 2 && args[0] == "net" && args[1] == "kill":
		return netKill()
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
		return call("palette.run", strings.Join(args[1:], " "))
	case len(args) == 2 && args[0] == "clip" && args[1] == "history":
		return clipHistory()
	case len(args) == 3 && args[0] == "clip" && args[1] == "history":
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
		return focusDesk("window.jump-to", args[2])
	case len(args) == 3 && args[0] == "desk" && args[1] == "switch":
		return switchDesk(args[2])
	case len(args) == 3 && args[0] == "desk" && args[1] == "move-window":
		return focusDesk("desk.move-window", args[2])
	case len(args) == 2 && args[0] == "desk" && args[1] == "move-window-to":
		return pickDesk("desk.move-window-to")
	case len(args) == 3 && args[0] == "desk" && args[1] == "move-window-to":
		return focusDesk("desk.move-window-to", args[2])
	case len(args) == 2 && args[0] == "desk" && args[1] == "move-workspace-to":
		return pickDesk("desk.move-workspace-to")
	case len(args) == 3 && args[0] == "desk" && args[1] == "move-workspace-to":
		return focusDesk("desk.move-workspace-to", args[2])
	case len(args) == 2 && args[0] == "workspace" && (args[1] == "next" || args[1] == "prev"):
		return focusDesk("workspace." + args[1])
	case len(args) == 2 && args[0] == "nav" && (args[1] == "down" || args[1] == "up"):
		return focusDesk("nav." + args[1])
	case len(args) == 2 && args[0] == "desk" && (args[1] == "next" || args[1] == "prev"):
		return focusDesk("desk." + args[1])
	case len(args) >= 2 && args[0] == "ask":
		return ask(args[1], strings.Join(args[2:], " "))
	case len(args) >= 3 && args[0] == "queue" && args[1] == "add":
		return queueAdd(strings.Join(args[2:], " "))
	case len(args) == 1 && args[0] == "queue":
		return queueList()
	case len(args) == 3 && args[0] == "queue" && args[1] == "done":
		return call("queue.done", args[2])
	case len(args) == 2 && args[0] == "queue" && args[1] == "clear":
		return queueClear()
	case len(args) == 2 && args[0] == "desk" && args[1] == "zen":
		return zen("toggle")
	case len(args) == 3 && args[0] == "desk" && args[1] == "zen":
		return zen(args[2])
	case len(args) == 2 && args[0] == "desk" && args[1] == "queue-jump":
		return focusDesk("desk.queue-jump")
	case len(args) == 2 && args[0] == "desk" && args[1] == "regulars":
		return focusDesk("desk.regulars")
	case len(args) == 2 && args[0] == "desk" && args[1] == "last":
		return lastDesk()
	case len(args) == 2 && args[0] == "desk" && args[1] == "panic":
		return deskPanic()
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
