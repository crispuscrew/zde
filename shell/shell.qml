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

    // How a test can ask the bar what it is showing, rather than only whether
    // it is running: `qs -p <config> ipc call queue count`. A bar that never
    // read the queue and a bar reading it correctly look identical from the
    // outside, and that is the mutation the smoke test could not otherwise
    // catch.
    IpcHandler {
        target: "queue"

        function count(): string {
            if (!root.linked)
                return "unlinked";
            if (!root.known)
                return "unknown";
            return root.queued + " " + root.urgent;
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
