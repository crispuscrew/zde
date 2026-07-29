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

ShellRoot {
    id: root

    // What the bar knows. Nothing derives from the compositor: the bar asks
    // zded, and zded is the only thing that knows what a desk is.
    property int queued: 0
    property int urgent: 0
    property bool linked: false

    // One connection, held open. zded speaks line-delimited JSON, so asking
    // costs a write and a read; a `zde queue` per tick would be a process per
    // tick, and on a bar that is visible.
    Socket {
        id: zded

        path: (Quickshell.env("XDG_RUNTIME_DIR") || "/run/user/1000") + "/zde/zded.sock"
        connected: true

        onConnectionStateChanged: {
            root.linked = zded.connected;
            if (!zded.connected) {
                // Say nothing rather than the last thing we knew: a stale
                // count on a bar is worse than an empty one, because it looks
                // current.
                root.queued = 0;
                root.urgent = 0;
            } else {
                ask.triggered();
            }
        }

        parser: SplitParser {
            onRead: line => {
                let res = null;
                try {
                    res = JSON.parse(line);
                } catch (e) {
                    return;
                }
                if (!res || res.error !== undefined) {
                    return;
                }
                // ok is the queue, oldest first, or null when it is empty -
                // Go marshals an empty slice as null, and `null.length` is
                // the kind of thing that takes a bar down.
                const items = res.ok || [];
                root.queued = items.length;
                root.urgent = items.filter(i => i.urgent === true).length;
            }
        }
    }

    // Two seconds, and no push. zded answers questions and does not yet raise
    // its voice, so this is the latency between something arriving and the bar
    // admitting it. An event stream is the fix, and it is worth having for the
    // notification center rather than for this.
    Timer {
        id: ask

        interval: 2000
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: {
            if (zded.connected) {
                zded.write('{"method":"queue.list"}\n');
            }
        }
    }

    // One bar per screen. Variants rebuilds this list when screens come and
    // go, which is what makes docking work without the bar knowing anything
    // about it.
    Variants {
        model: Quickshell.screens

        PanelWindow {
            id: bar

            required property var modelData

            screen: modelData
            color: "#11121a"
            implicitHeight: 26

            anchors {
                top: true
                left: true
                right: true
            }

            // Reserve the space rather than float over the windows: niri
            // tiles to what is left, so a bar that does not reserve is a bar
            // that covers the top of whatever you are reading.
            exclusiveZone: bar.implicitHeight

            Text {
                anchors.left: parent.left
                anchors.leftMargin: 10
                anchors.verticalCenter: parent.verticalCenter

                // The queue, in the words the CLI uses, so the bar and
                // `zde queue` never seem to be talking about different things.
                text: {
                    if (!root.linked) {
                        return "zded: not answering";
                    }
                    if (root.queued === 0) {
                        return "queue empty";
                    }
                    const n = root.queued === 1 ? "1 waiting" : root.queued + " waiting";
                    return root.urgent > 0 ? n + "  !" + root.urgent : n;
                }
                color: root.urgent > 0 ? "#e5484d" : (root.linked ? "#c9ccd4" : "#7a7f8a")
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

                // Rebuilt on the tick rather than bound to a clock object,
                // because the only clock QML has is a Timer.
                property var now: new Date()
                text: Qt.formatDateTime(clock.now, "ddd d MMM  HH:mm")

                Timer {
                    interval: 1000
                    running: true
                    repeat: true
                    triggeredOnStart: true
                    onTriggered: clock.now = new Date()
                }
            }
        }
    }
}
