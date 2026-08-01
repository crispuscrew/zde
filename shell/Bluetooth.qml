// Bluetooth: what is around, what is paired, and the pairing question that has
// to be answered by a person (internal/bt).
//
// A section and not a window. Bluetooth is one half of connections - wifi and
// ethernet are the other - and a surface that only works as a whole window
// would have to be taken apart again to sit beside them. So this is an Item
// with rows in and a command out: whoever shows it owns the layer surface, the
// keyboard, and the socket.
//
// Nothing here decides anything. The shell is a thin adapter over zded IPC with
// zero logic inside (docs/vision.md, section 2), so this draws what zded said
// and hands back the verb that was chosen; pairing, refusing and trusting all
// happen in the daemon, which is where the agent lives.
//
// One thing worth saying out loud, because it is the case this surface is worst
// at: the keyboard being paired may be the keyboard. A surface like this holds
// an exclusive keyboard grab while it is up, so the answer has to come from an
// input that already works - the built-in keyboard, or the mouse, since every
// row and both answer buttons are click targets. Once the new keyboard is
// paired and connected it feeds the same seat and drives this like any other,
// so nothing is locked out afterwards. Pairing a keyboard on a machine whose
// only keyboard is the one being paired is not possible here, and would not be
// on any surface that takes the keyboard: that one is `zde system bluetooth`
// from a terminal, or the device's own side.
pragma ComponentBehavior: Bound

import QtQuick

Item {
    id: root

    // The reply to bluetooth.state, whole. Handed over rather than fetched:
    // zded has it already, and a section that has to ask before it can draw
    // appears in two steps.
    //
    // Not called `state`: every Item has one of those already, and shadowing it
    // is a warning qmllint fails the build on rather than a subtlety to find at
    // runtime.
    property var radio: ({})
    // Whether this section is the one reading keys. Standalone it is; inside
    // the connections surface it is true while this section has the focus.
    property bool active: true
    property int index: 0

    // What was chosen, as a zded method and its arguments. This knows nothing
    // about sockets - the surface holding it writes these out.
    signal command(string method, var args)
    signal closed

    readonly property var adapter: root.radio.adapter ?? ({})
    readonly property var devices: root.radio.devices ?? []
    // The pairing question, when one is waiting. It is the only thing on this
    // surface that changes what a key means, which is why it is drawn across
    // the top rather than as a row in the list.
    readonly property var pending: root.radio.pending ?? null

    readonly property var current: root.index >= 0 && root.index < root.devices.length ? root.devices[root.index] : null

    implicitWidth: 520
    implicitHeight: column.implicitHeight + 24

    function step(by) {
        const n = root.devices.length;
        if (n === 0)
            return;
        // Wrapping, like the picker: the list is short, and one that stops at
        // the end makes you look at where the cursor is before pressing a key.
        root.index = (root.index + by + n) % n;
    }

    // Enter is "do the obvious thing to this row", and which one that is comes
    // from the row rather than from a mode: an unpaired device pairs, a paired
    // one connects, and a connected one disconnects. Nothing here can trust a
    // device - that is its own key, because it is its own decision.
    function activate() {
        const d = root.current;
        if (!d)
            return;
        if (d.connected)
            root.command("bluetooth.disconnect", [d.address]);
        else if (d.paired)
            root.command("bluetooth.connect", [d.address]);
        else
            root.command("bluetooth.pair", [d.address]);
    }

    function answer(yes) {
        if (root.pending)
            root.command("bluetooth.confirm", [yes ? "yes" : "no"]);
    }

    focus: root.active
    Keys.onPressed: event => {
        // The question first: while one is waiting, y and n are the answer to
        // it and nothing else on this surface matters as much.
        if (root.pending && (event.text === "y" || event.text === "n")) {
            root.answer(event.text === "y");
            event.accepted = true;
            return;
        }
        switch (event.key) {
        case Qt.Key_Escape:
            root.closed();
            break;
        case Qt.Key_Return:
        case Qt.Key_Enter:
            root.activate();
            break;
        case Qt.Key_Down:
            root.step(1);
            break;
        case Qt.Key_Up:
            root.step(-1);
            break;
        default:
            if (event.text === "j")
                root.step(1);
            else if (event.text === "k")
                root.step(-1);
            else if (event.text === "s")
                root.command("bluetooth.scan", [root.adapter.discovering ? "off" : "on"]);
            else if (event.text === "p")
                root.command("bluetooth.power", [root.adapter.powered ? "off" : "on"]);
            else if (event.text === "t" && root.current)
                root.command(root.current.trusted ? "bluetooth.untrust" : "bluetooth.trust", [root.current.address]);
            else if (event.text === "f" && root.current)
                root.command("bluetooth.forget", [root.current.address]);
            else if (event.text >= "1" && event.text <= "9")
                root.index = Math.min(parseInt(event.text, 10) - 1, root.devices.length - 1);
            else {
                return;
            }
        }
        event.accepted = true;
    }

    Column {
        id: column

        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: parent.top
        anchors.margins: 12
        spacing: 4

        // The adapter, in the same words the CLI uses, so the surface and
        // `zde system bluetooth` never seem to be talking about different
        // machines. A machine with no radio says so here and the list below is
        // empty, which is the whole of what this surface can honestly show.
        Text {
            width: column.width
            text: {
                if (!root.adapter.present)
                    return "bluetooth: none  " + (root.adapter.why ?? "");
                const scan = root.adapter.discovering ? "scanning" : "idle";
                return "bluetooth: " + (root.adapter.powered ? "on" : "off") + "  " + scan;
            }
            elide: Text.ElideRight
            color: root.adapter.present && root.adapter.powered ? "#c9ccd4" : "#7a7f8a"
            font.pixelSize: 13
            font.family: "monospace"
        }

        // What the daemon is in the middle of, and how the last one ended.
        // Pairing waits on a person and connecting waits on a radio, so without
        // this the surface looks identical while it works and after it failed.
        Text {
            width: column.width
            visible: text !== ""
            text: root.radio.doing ? root.radio.doing : (root.radio.failed ?? "")
            elide: Text.ElideRight
            color: root.radio.doing ? "#7a7f8a" : "#e5484d"
            font.pixelSize: 12
            font.family: "monospace"
        }

        // The question. Loud, across the top, and with both answers as click
        // targets: this is the one moment on this surface where saying yes to
        // the wrong thing hands a stranger's device a way in.
        Rectangle {
            id: question

            width: column.width
            height: 52
            visible: root.pending !== null
            radius: 4
            color: "#1d1a24"
            border.color: "#e5a23d"
            border.width: 1

            Text {
                id: asking

                anchors.left: parent.left
                anchors.top: parent.top
                anchors.margins: 8
                width: parent.width - 16
                text: {
                    if (!root.pending)
                        return "";
                    const who = root.pending.name ? root.pending.name : root.pending.device;
                    if (root.pending.kind === "display")
                        return who + "  type " + root.pending.passkey + " on it";
                    if (root.pending.kind === "service")
                        return who + "  wants " + root.pending.uuid;
                    if (root.pending.passkey)
                        return who + "  passkey " + root.pending.passkey;
                    return who + "  wants to pair";
                }
                elide: Text.ElideRight
                color: "#e5a23d"
                font.pixelSize: 13
                font.family: "monospace"
            }

            Text {
                anchors.left: parent.left
                anchors.bottom: parent.bottom
                anchors.margins: 8
                // A passkey to type on the other device is not a question: it
                // is answered by typing it there, and offering a yes here would
                // be offering to agree with something nobody checked.
                text: root.pending && root.pending.kind === "display" ? "waiting for it to be typed" : "does it match?   y yes   n no"
                color: "#7a7f8a"
                font.pixelSize: 11
                font.family: "monospace"
            }

            // Both answers as click targets, written out rather than generated
            // from a list of two: the keyboard being paired may be the
            // keyboard, and a mouse is then the only way to answer.
            Row {
                anchors.right: parent.right
                anchors.bottom: parent.bottom
                anchors.margins: 8
                spacing: 6
                visible: root.pending !== null && root.pending.kind !== "display"

                Rectangle {
                    width: 44
                    height: 20
                    radius: 3
                    color: "transparent"
                    border.color: "#3a3d4a"
                    border.width: 1

                    MouseArea {
                        anchors.fill: parent
                        onClicked: root.answer(false)
                    }

                    Text {
                        anchors.centerIn: parent
                        text: "no"
                        color: "#c9ccd4"
                        font.pixelSize: 11
                        font.family: "monospace"
                    }
                }

                Rectangle {
                    width: 44
                    height: 20
                    radius: 3
                    color: "#2a2c37"
                    border.color: "#3a3d4a"
                    border.width: 1

                    MouseArea {
                        anchors.fill: parent
                        onClicked: root.answer(true)
                    }

                    Text {
                        anchors.centerIn: parent
                        text: "yes"
                        color: "#c9ccd4"
                        font.pixelSize: 11
                        font.family: "monospace"
                    }
                }
            }
        }

        // The devices. Paired and connected are marked rather than implied by
        // the highlight, which means something else here: the highlight is what
        // Enter would act on.
        Repeater {
            model: root.devices

            Rectangle {
                id: row

                required property var modelData
                required property int index

                width: column.width
                height: 26
                radius: 4
                color: row.index === root.index ? "#2a2c37" : "transparent"

                MouseArea {
                    anchors.fill: parent
                    onClicked: {
                        root.index = row.index;
                        root.activate();
                    }
                }

                Text {
                    anchors.left: parent.left
                    anchors.leftMargin: 8
                    anchors.right: mark.left
                    anchors.rightMargin: 8
                    anchors.verticalCenter: parent.verticalCenter
                    text: (row.index < 9 ? (row.index + 1) + "  " : "   ") + (row.modelData.name ? row.modelData.name : row.modelData.address)
                    elide: Text.ElideRight
                    color: "#c9ccd4"
                    font.pixelSize: 13
                    font.family: "monospace"
                }

                // The three facts that decide what a key does to this row, in
                // the same three positions the CLI prints them in: paired,
                // trusted, connected.
                Text {
                    id: mark

                    anchors.right: parent.right
                    anchors.rightMargin: 8
                    anchors.verticalCenter: parent.verticalCenter
                    text: (row.modelData.paired ? "p" : "-") + (row.modelData.trusted ? "t" : "-") + (row.modelData.connected ? "c" : "-")
                    color: row.modelData.connected ? "#c9ccd4" : "#7a7f8a"
                    font.pixelSize: 12
                    font.family: "monospace"
                }
            }
        }

        // Nothing found is a sentence rather than an empty box: on this surface
        // the ordinary reason is that nobody has started a scan.
        Text {
            width: column.width
            visible: root.devices.length === 0
            text: root.adapter.present ? "nothing here yet - s starts a scan" : ""
            color: "#7a7f8a"
            font.pixelSize: 12
            font.family: "monospace"
        }

        // What else this surface does, said on the surface. A key nobody can
        // see is a key nobody uses.
        Text {
            width: column.width
            text: "enter connect/pair   t trust   f forget   s scan   p power   esc close"
            color: "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
        }
    }
}
