// The zde bar (docs/roadmap.md, 0.1: shell MVP). Whatever a keypress depends
// on belongs on the bar (docs/vision.md, principle 4). Today that is the
// queue, because the queue is the half of 0.1 that had nowhere to be seen:
// `zde queue add` and every notification the session receives land in it, and
// until now the only way to know was to go and ask.
//
// The mode and the mic are the other two the principle names. They are not
// stubbed here: the mode has one value until the input daemon lands, and the
// mic wants a PipeWire subscription rather than a poll. They arrive with what
// owns them.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Io
import Quickshell.Services.UPower
import Quickshell.Wayland

ShellRoot {
    id: root

    // What the bar knows, and whether it knows it. `known` is the difference
    // between a number and the last number: a count left on screen after zded
    // stopped answering looks current, which is worse than saying nothing.
    property int queued: 0
    property int urgent: 0
    property bool linked: false
    property bool known: false
    // What a panel actually came up as, reported back for the same reason the
    // count is: a strip of zero height, or one reserving nothing, is on the
    // screen as far as the compositor's layer list is concerned and invisible
    // or in the way as far as a person is concerned.
    property int barHeight: 0
    property int barZone: 0
    // Set when a question has gone out and no answer has come back. Two of
    // those in a row and the number stops being trustworthy, which is how a
    // daemon that holds the socket open and goes quiet gets noticed.
    property int waiting: 0

    // The battery, read once at the root: the bar draws it and the IPC reports
    // it, and two readings of the same thing would eventually disagree.
    QtObject {
        id: batteryState

        readonly property var dev: UPower.displayDevice
        readonly property bool have: batteryState.dev !== null && batteryState.dev.isLaptopBattery
        readonly property int pct: batteryState.have ? Math.round(batteryState.dev.percentage * 100) : 0
        readonly property bool charging: batteryState.have
            && (batteryState.dev.state === UPowerDeviceState.Charging
                || batteryState.dev.state === UPowerDeviceState.FullyCharged)
        readonly property int secsLeft: batteryState.have ? batteryState.dev.timeToEmpty : 0
    }

    // One connection, held open. zded speaks line-delimited JSON, so asking
    // costs a write and a read; a `zde queue` per tick would be a process per
    // tick, and on a bar that is visible.
    //
    // XDG_RUNTIME_DIR with no fallback on purpose: zded refuses to start
    // without it (internal/zded, DefaultSocket), so a guessed path could only
    // ever point at a directory belonging to nobody, or to somebody else.
    Socket {
        id: zded

        path: Quickshell.env("XDG_RUNTIME_DIR") + "/zde/zded.sock"
        connected: true

        onConnectionStateChanged: {
            root.linked = zded.connected;
            root.waiting = 0;
            if (zded.connected) {
                ask.triggered();
            } else {
                root.known = false;
            }
        }

        parser: SplitParser {
            onRead: line => {
                root.waiting = 0;
                let res = null;
                try {
                    res = JSON.parse(line);
                } catch (e) {
                    root.known = false;
                    return;
                }
                if (!res || res.error !== undefined) {
                    root.known = false;
                    return;
                }
                // ok is the queue, oldest first, or null when it is empty - Go
                // marshals an empty slice as null, and `null.length` is the
                // kind of thing that takes a bar down.
                const items = res.ok || [];
                root.queued = items.length;
                root.urgent = items.filter(i => i.urgent === true).length;
                root.known = true;
            }
        }
    }

    // Two seconds, and no push. zded answers questions and does not yet raise
    // its voice, so this is the latency between something arriving and the bar
    // admitting it. An event stream is the fix, and it is worth having for the
    // notification center rather than for this.
    //
    // This is also the reconnect. `connected: true` is written once and stays
    // written: a socket that fails to connect, or that loses its daemon, does
    // not redial itself - so without this the bar goes blind for the rest of
    // the session the first time zded restarts, which is every time anything
    // in the daemon is rebuilt. Asking again is the cheapest possible retry
    // and it costs nothing while the socket is up.
    Timer {
        id: ask

        interval: 2000
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: {
            if (!zded.connected) {
                zded.connected = true;
                return;
            }
            if (root.waiting >= 2) {
                root.known = false;
            }
            root.waiting += 1;
            zded.write('{"method":"queue.list"}\n');
        }
    }

    // The events connection, separate from the one the bar polls on. Same
    // socket, second connection: a stream and a poll on one connection would
    // work, and keeping them apart means a broken parser on either side cannot
    // take the other with it.
    Socket {
        id: stream

        path: Quickshell.env("XDG_RUNTIME_DIR") + "/zde/zded.sock"
        connected: true

        onConnectionStateChanged: {
            if (stream.connected)
                stream.write('{"method":"events"}\n');
        }

        parser: SplitParser {
            onRead: line => {
                let msg = null;
                try {
                    msg = JSON.parse(line);
                } catch (e) {
                    return;
                }
                // Replies to our own subscribe arrive here too; only the lines
                // carrying an event are events.
                if (!msg || !msg.event)
                    return;
                if (msg.event.kind === "picker")
                    root.openPicker(msg.event);
                else if (msg.event.kind === "windows")
                    root.openWindows(msg.event);
            }
        }
    }

    // Redialling the stream, on the same tick as the bar's poll. zded is
    // restarted by any switch that changes it, and a shell that stopped
    // listening then would keep working and stop appearing, which is the worst
    // of both.
    Timer {
        interval: 2000
        running: true
        repeat: true
        onTriggered: {
            if (!stream.connected)
                stream.connected = true;
        }
    }

    // What choosing a row asks zded to do. Set when the picker is opened, since
    // that is the only place that knows which kind of rows went into it - the
    // picker itself is handed rows and hands back a key, and knows nothing
    // about sockets or methods.
    property string pickMethod: "desk.switch"

    // The desks. The note on a row is where you are, because that is the one
    // thing about a desk list you cannot see from the list.
    function openPicker(ev) {
        const here = ev.on ?? "";
        root.pickMethod = "desk.switch";
        root.showPicker(ev, "desks", (ev.desks ?? []).map(d => ({
                    key: d,
                    label: d,
                    note: d === here ? "here" : ""
                })), here);
    }

    // The open windows. The app id and the title together, because neither
    // alone tells two terminals apart; the workspace on the right, because the
    // zde name is how a person recognises which desk a window is on.
    function openWindows(ev) {
        root.pickMethod = "window.jump-to";
        root.showPicker(ev, "windows", (ev.windows ?? []).map(w => ({
                    key: String(w.id),
                    label: (w.appId ?? "") + (w.title ? "  " + w.title : ""),
                    note: w.workspace ?? ""
                })), "");
    }

    // One instance and not one per screen: it appears on the screen zded says
    // is being looked at, because a picker on every monitor is not a picker. An
    // unknown output falls back to the first screen, which on one monitor is
    // the right answer and on several is at least a screen.
    function showPicker(ev, kind, rows, here) {
        let want = null;
        for (const s of Quickshell.screens) {
            if (s.name === ev.output)
                want = s;
        }
        picker.screen = want ?? Quickshell.screens[0] ?? null;
        picker.show(kind, rows, here, ev.token ?? "");
    }

    // The one process this shell starts. Everything else it does is a line on a
    // socket; a leader action is a key doing what a key does.
    Process {
        id: leader
    }

    Picker {
        id: picker

        onChosen: key => {
            picker.hide();
            // The shell decides nothing: switching a desk and jumping to a
            // window are both zded's, over the same socket everything else uses
            // (docs/vision.md, section 2 - the shell is a thin adapter with
            // zero logic inside).
            //
            // On the stream connection and not the bar's. The reply to this is
            // the list of workspaces it focused, and the bar's parser reads
            // whatever arrives as a queue listing - so picking a desk made the
            // bar say "1 waiting" for two seconds about a queue that was empty.
            // The stream's parser ignores any line with no event in it.
            if (stream.connected)
                stream.write(JSON.stringify({
                    method: root.pickMethod,
                    args: [key]
                }) + "\n");
            else
                console.warn("zde: picked " + key + " with no connection to zded");
        }
        onDismissed: picker.hide()

        // A leader action runs the same command the key would, rather than the
        // shell learning what locking is: `zde system lock` resolves the
        // machine's locker through the same table Mod+t uses, and when that
        // becomes zcr's job it changes in one place and not two.
        onAction: name => {
            picker.hide();
            leader.command = ["zde", "system", name];
            leader.running = true;
        }

        // The asker is waiting on this, briefly, to find out whether anything
        // came of the event it sent.
        onShown: token => {
            if (stream.connected)
                stream.write(JSON.stringify({
                    method: "shown",
                    args: [token]
                }) + "\n");
        }
    }

    // How a test can ask the bar what it is showing, rather than only whether
    // it is running: `qs -p <config> ipc call queue count`. A bar that never
    // read the queue and a bar reading it correctly look identical from the
    // outside, and that is the mutation the smoke test could not otherwise
    // catch.
    // The picker, over the same IPC. Two reasons, and the second is the honest
    // one: a picker is driven by a keyboard, and a machine with no input devices
    // - which is every CI machine - cannot press a key. So the choice a key
    // makes is reachable from here too, which makes it testable and, as a side
    // effect, scriptable.
    IpcHandler {
        target: "picker"

        // What it is showing, how many rows, and which row you are already on -
        // a dash where there is none, so that the answer has the same shape
        // either way and a test is not comparing against a trailing space.
        function state(): string {
            if (!picker.visible)
                return "closed";
            const here = picker.on === "" ? "-" : picker.on;
            return "open " + picker.kind + " " + picker.rows.length + " " + here;
        }

        function pick(key: string): string {
            if (!picker.visible)
                return "closed";
            if (picker.rows.findIndex(r => r.key === key) < 0)
                return "no such row";
            picker.chosen(key);
            return "picked";
        }

        function dismiss(): string {
            picker.dismissed();
            return "closed";
        }

        // The leader letters, reachable the same way the pick is: a machine
        // with no input devices cannot press l, and the wiring behind it is
        // the part worth proving.
        function act(name: string): string {
            if (!picker.visible)
                return "closed";
            picker.action(name);
            return "acted";
        }
    }

    IpcHandler {
        target: "queue"

        function count(): string {
            if (!root.linked)
                return "unlinked";
            if (!root.known)
                return "unknown";
            return root.queued + " " + root.urgent;
        }

        // What the battery reads, so a machine without one can be told apart
        // from a bar that failed to ask. A VM has no battery, which is the
        // case worth pinning: a desktop should say nothing here rather than
        // show a stub reading full.
        function battery(): string {
            if (!batteryState.have)
                return "none";
            return batteryState.pct + (batteryState.charging ? " charging" : " discharging");
        }

        // Height and reserved space, as the panel came up. Not the same claim
        // as "the compositor honoured it" - proving that means measuring a
        // window with the bar and without it, which the smoke test does not do
        // yet - but it is the difference between a bar and a line of nothing.
        function geometry(): string {
            return root.barHeight + " " + root.barZone;
        }
    }

    // One bar per screen. Variants rebuilds this list when screens come and
    // go, which is what makes docking work without the bar knowing anything
    // about it.
    Variants {
        model: Quickshell.screens

        // qmllint disable uncreatable-type
        // PanelWindow is exported from PanelWindowInterface, which is declared
        // uncreatable because Quickshell substitutes the platform
        // implementation (wlr-layer-shell) at runtime. Disabled for this
        // instantiation only: as a whole category it would also hide a real
        // attempt to build Quickshell itself, or a DataStream.
        PanelWindow {
            id: bar

            required property var modelData

            screen: bar.modelData
            color: "#11121a"
            implicitHeight: 26

            // Ours, not quickshell's default. It is what niri layer rules will
            // name, and it is what the smoke test looks for - matching the
            // default would have matched any quickshell on the machine.
            WlrLayershell.namespace: "zde-bar"

            anchors {
                top: true
                left: true
                right: true
            }

            // Reserve the space rather than float over the windows: niri
            // tiles to what is left, so a bar that does not reserve is a bar
            // that covers the top of whatever you are reading.
            exclusiveZone: bar.implicitHeight

            // qmllint enable uncreatable-type

            Component.onCompleted: {
                root.barHeight = bar.height;
                root.barZone = bar.exclusiveZone;
            }

            Text {
                anchors.left: parent.left
                anchors.leftMargin: 10
                anchors.verticalCenter: parent.verticalCenter

                // The queue, in the words the CLI uses, so the bar and
                // `zde queue` never seem to be talking about different things.
                text: {
                    if (!root.linked)
                        return "zded: not answering";
                    if (!root.known)
                        return "queue: unknown";
                    if (root.queued === 0)
                        return "queue empty";
                    const n = root.queued === 1 ? "1 waiting" : root.queued + " waiting";
                    return root.urgent > 0 ? n + "  !" + root.urgent : n;
                }
                color: root.urgent > 0 ? "#e5484d" : (root.linked && root.known ? "#c9ccd4" : "#7a7f8a")
                font.pixelSize: 13
                font.family: "monospace"
            }

            // The battery, which is on the bar for the reason principle 4 gives:
            // what a keypress depends on. On a laptop away from a desk, what
            // every decision depends on is how long it has left.
            //
            // Empty on a machine with no battery rather than a stub reading
            // 100%: a desktop should say nothing here, and UPower's display
            // device on one is not a laptop battery.
            Text {
                id: battery

                readonly property bool have: batteryState.have
                readonly property int pct: batteryState.pct
                readonly property bool charging: batteryState.charging

                anchors.right: clock.left
                anchors.rightMargin: 14
                anchors.verticalCenter: parent.verticalCenter
                visible: battery.have

                text: {
                    if (!battery.have)
                        return "";
                    const mark = battery.charging ? "+" : "";
                    // The time left, when the machine knows it and is running
                    // on it. A percentage answers "how full"; a train journey
                    // asks "how long", and they are not the same question on a
                    // battery that has aged.
                    const secs = batteryState.secsLeft;
                    if (!battery.charging && secs > 60) {
                        const h = Math.floor(secs / 3600);
                        const m = Math.floor((secs % 3600) / 60);
                        return mark + battery.pct + "%  " + h + "h" + (m < 10 ? "0" : "") + m;
                    }
                    return mark + battery.pct + "%";
                }
                // Loud below a fifth, which is where a decision has to be made
                // about the next hour rather than the next day.
                color: !battery.charging && battery.pct <= 20 ? "#e5484d" : "#c9ccd4"
                font.pixelSize: 13
                font.family: "monospace"
            }

            Text {
                id: clock

                anchors.right: parent.right
                anchors.rightMargin: 10
                anchors.verticalCenter: parent.verticalCenter

                color: "#c9ccd4"
                font.pixelSize: 13
                font.family: "monospace"

                property var now: new Date()
                text: Qt.formatDateTime(clock.now, "ddd d MMM  HH:mm")

                // Woken on the minute rather than on the second. A clock that
                // shows minutes has nothing to say 59 times out of 60, and
                // this is a project with a laptop profile on its roadmap.
                Timer {
                    id: tick

                    interval: 60000 - (new Date().getSeconds() * 1000 + new Date().getMilliseconds())
                    running: true
                    repeat: false
                    onTriggered: {
                        const d = new Date();
                        clock.now = d;
                        // Re-aimed at the next boundary each time, so it
                        // cannot drift into the middle of a minute and stay
                        // there.
                        tick.interval = 60000 - (d.getSeconds() * 1000 + d.getMilliseconds());
                        tick.restart();
                    }
                }
            }
        }
    }
}
