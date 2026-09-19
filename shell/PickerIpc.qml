pragma ComponentBehavior: Bound

import Quickshell
import Quickshell.Io

Scope {
    id: ipc
    required property var surface
    IpcHandler {
        target: "picker"

        function state(): string {
            if (!ipc.surface.visible)
                return "closed";
            const here = ipc.surface.on === "" ? "-" : ipc.surface.on;
            return "open " + ipc.surface.kind + " " + ipc.surface.rows.length + " " + here;
        }

        function pick(key: string): string {
            if (!ipc.surface.visible)
                return "closed";
            if (ipc.surface.rows.findIndex(entry => entry.key === key) < 0)
                return "no such row";
            ipc.surface.chosen(key);
            return "picked";
        }

        function dismiss(): string {
            ipc.surface.dismissed();
            return "closed";
        }

        function act(name: string): string {
            if (!ipc.surface.visible)
                return "closed";
            ipc.surface.action(name);
            return "acted";
        }
    }
}
