pragma ComponentBehavior: Bound

import Quickshell
import Quickshell.Io

Scope {
    id: actions
    required property var palette
    required property var powerMenu

    IpcHandler {
        target: "palette"

        function state(): string {
            if (!actions.palette.visible)
                return "closed";
            return "open " + actions.palette.rows.length + " " + actions.palette.matches.length;
        }

        function filter(text: string): string {
            if (!actions.palette.visible)
                return "closed";
            actions.palette.narrow(text);
            return "filtered";
        }

        // Use the same disabled-row and one-flight gates as keyboard activation.
        function run(name: string): string {
            if (!actions.palette.visible)
                return "closed";
            const index = actions.palette.matches.findIndex(entry => entry.name === name);
            if (index < 0)
                return "no such row";
            actions.palette.index = index;
            actions.palette.run();
            return actions.palette.running ? "ran" : "cannot";
        }

        function dismiss(): string {
            actions.palette.dismissed();
            return "closed";
        }
    }

    IpcHandler {
        target: "power"

        function state(): string {
            if (!actions.powerMenu.visible)
                return "closed";
            const waiting = actions.powerMenu.asking === "" ? "-" : actions.powerMenu.asking;
            return "open " + actions.powerMenu.rows.length + " " + waiting;
        }

        // Choosing and confirming stay separate, just as they are on the surface.
        function choose(name: string): string {
            if (!actions.powerMenu.visible)
                return "closed";
            const index = actions.powerMenu.rows.findIndex(entry => entry.name === name);
            if (index < 0)
                return "no such row";
            actions.powerMenu.choose(index);
            if (actions.powerMenu.asking !== "")
                return "asks";
            return actions.powerMenu.running ? "ran" : "nothing";
        }

        function confirm(): string {
            if (!actions.powerMenu.visible)
                return "closed";
            if (actions.powerMenu.asking === "")
                return "nothing asked";
            actions.powerMenu.confirm();
            return actions.powerMenu.running ? "ran" : "nothing";
        }

        function dismiss(): string {
            actions.powerMenu.dismissed();
            return "closed";
        }
    }
}
