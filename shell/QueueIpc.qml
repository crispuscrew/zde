pragma ComponentBehavior: Bound

import Quickshell
import Quickshell.Io

Scope {
    id: ipc
    required property var state
    required property var hardware
    required property var network
    required property var idleState
    IpcHandler {
        target: "queue"

        function count(): string {
            if (!ipc.state.linked)
                return "unlinked";
            if (!ipc.state.known)
                return "unknown";
            return ipc.state.queued + " " + ipc.state.urgent;
        }

        function battery(): string {
            if (!ipc.hardware.battery.have)
                return "none";
            return ipc.hardware.battery.pct + (ipc.hardware.battery.charging ? " charging" : " discharging");
        }

        function mic(): string {
            if (!ipc.hardware.mic.known)
                return "unknown";
            if (!ipc.hardware.mic.have)
                return "none";
            if (ipc.hardware.mic.muted)
                return "muted";
            return ipc.hardware.mic.live ? "live" : "idle";
        }

        function net(): string {
            if (!ipc.network.known)
                return "unknown";
            if (ipc.network.killed)
                return "cut";
            if (ipc.network.kind === "wifi")
                return "wifi " + ipc.network.ssid + " " + ipc.network.strength;
            return ipc.network.kind;
        }

        // "none" describes logind's table, not the unobservable Wayland inhibitors.
        function idle(): string {
            if (!ipc.idleState.known)
                return "unknown";
            return ipc.idleState.holds === 0 ? "none" : "held " + ipc.idleState.holds;
        }

        function geometry(): string {
            return ipc.state.barHeight + " " + ipc.state.barZone;
        }

        function zen(): string {
            if (!ipc.state.zenKnown)
                return "unknown";
            if (!ipc.state.zen)
                return "off";
            return ipc.hardware.mic.live ? "on mic" : "on";
        }
    }
}
