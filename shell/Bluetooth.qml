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
//
// Every Text in this file sets textFormat: Text.PlainText, including the ones
// that only draw a literal. Qt Quick's default is AutoText, which renders a
// string that looks like markup as StyledText, and StyledText fetches an
// <img src="http://..."> over the network - out of a process that is holding a
// layer surface and a keyboard grab. internal/zded/qml_test.go refuses a Text
// with no format, and carries the whole of why.
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
    // The address of the row under the cursor, and not its number. The list
    // reorders itself whenever something connects or pairs - what you own comes
    // first - so a remembered index is a cursor that moves onto a different
    // device while somebody is reaching for a key. The address does not move.
    property string selected: ""
    // A destructive key waiting for a second press: { verb, address, name }.
    // Trusting and forgetting are both one keystroke on a highlighted row, and
    // both are permissions - one grants a standing one, the other throws a
    // pairing away - so each asks first.
    property var confirming: null

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
    // Where zde stands with BlueZ's agent manager, and null before the first
    // reply has arrived - there is nothing to warn about until something has
    // been asked.
    readonly property var agentState: root.radio.agent ?? null

    readonly property int index: root.rowOf(root.selected)
    readonly property var current: root.index >= 0 && root.index < root.devices.length ? root.devices[root.index] : null

    // Where a device sits in the list right now, and 0 for one that is no longer
    // there - a device that went away leaves the cursor at the top rather than
    // pointing past the end.
    function rowOf(address: string): int {
        const at = root.devices.findIndex(d => d.address === address);
        return at < 0 ? 0 : at;
    }

    implicitWidth: 520
    implicitHeight: column.implicitHeight + 24

    function step(by) {
        const n = root.devices.length;
        if (n === 0)
            return;
        // Wrapping, like the picker: the list is short, and one that stops at
        // the end makes you look at where the cursor is before pressing a key.
        root.selected = root.devices[(root.index + by + n) % n].address;
        // Moving is answering "not that one" to whatever was being confirmed.
        root.confirming = null;
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

    // The answer names the question. Without the id it would be a yes to
    // whatever is waiting when it lands, which is not always the question that
    // was read: one expires after 45 seconds and another arrives in its place
    // (internal/bt/agent.go, Answer).
    function answer(yes) {
        if (root.pending)
            root.command("bluetooth.confirm", [root.pending.id, yes ? "yes" : "no"]);
    }

    // A destructive key asks before it acts, and the second press is what does
    // it. Not a habit-forming dialog: it is two keys for the two verbs that
    // change what a device is allowed to do, and one for everything else.
    function ask(verb, device) {
        if (device)
            root.confirming = {
                "verb": verb,
                "address": device.address,
                "name": device.name ? device.name : device.address
            };
    }

    function actOnConfirmation() {
        const c = root.confirming;
        root.confirming = null;
        if (c)
            root.command(c.verb, [c.address]);
    }

    focus: root.active
    Keys.onPressed: event => {
        // The pairing question first, and ahead of any local confirmation:
        // while a device is waiting to be let in, y and n belong to it and
        // nothing on this surface matters as much. A local confirmation is
        // dropped rather than queued behind it, so that a y meant for one is
        // never read as the other.
        if (root.pending) {
            root.confirming = null;
            if (event.text === "y" || event.text === "n") {
                root.answer(event.text === "y");
                event.accepted = true;
                return;
            }
        } else if (root.confirming && (event.text === "y" || event.text === "n")) {
            if (event.text === "y")
                root.actOnConfirmation();
            else
                root.confirming = null;
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
            else if (event.text === "t" && root.current) {
                // Untrusting takes a permission away, so it happens on the
                // press; trusting grants a standing one, so it asks first.
                if (root.current.trusted)
                    root.command("bluetooth.untrust", [root.current.address]);
                else
                    root.ask("bluetooth.trust", root.current);
            } else if (event.text === "f" && root.current) {
                root.ask("bluetooth.forget", root.current);
            } else if (event.text >= "1" && event.text <= "9") {
                const at = Math.min(parseInt(event.text, 10) - 1, root.devices.length - 1);
                if (at >= 0)
                    root.selected = root.devices[at].address;
                root.confirming = null;
            } else {
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
            textFormat: Text.PlainText
        }

        // Whether zde is still the agent BlueZ calls. It has one default agent
        // and gives the role to whoever asked last, so anything else on this
        // machine can quietly become the thing that answers pairing questions -
        // and this surface would go on drawing an empty, calm list while it did.
        Text {
            width: column.width
            visible: root.agentState !== null && root.agentState.default !== true
            text: "pairing questions are NOT being answered here"
            elide: Text.ElideRight
            color: "#e5484d"
            font.pixelSize: 12
            font.family: "monospace"
            textFormat: Text.PlainText
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
            textFormat: Text.PlainText
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
                    const q = root.pending;
                    const who = q.name ? q.name : q.device;
                    if (q.kind === "display") {
                        const typed = q.entered > 0 ? "   " + q.entered + " typed" : "";
                        return who + "  type " + q.passkey + " on it" + typed;
                    }
                    if (q.kind === "service")
                        return who + "  wants " + (q.service ? q.service : q.uuid);
                    if (q.passkey)
                        return who + "  should be showing " + q.passkey;
                    return who + "  wants to pair";
                }
                elide: Text.ElideRight
                color: "#e5a23d"
                font.pixelSize: 13
                font.family: "monospace"
                textFormat: Text.PlainText
            }

            Text {
                anchors.left: parent.left
                anchors.bottom: parent.bottom
                anchors.margins: 8
                // Per kind, because "does it match?" over a question with
                // nothing to match is how a person learns that the words above
                // a yes do not mean anything. A passkey to type on the other
                // device is not a question here at all: it is answered by
                // typing it there.
                text: {
                    if (!root.pending)
                        return "";
                    if (root.pending.kind === "display")
                        return "waiting for it to be typed";
                    if (root.pending.kind === "service")
                        return "allow it?   y yes   n no";
                    if (root.pending.kind === "authorize")
                        return "nothing to compare: only if you started this   y yes   n no";
                    return "is it showing the same?   y yes   n no";
                }
                color: "#7a7f8a"
                font.pixelSize: 11
                font.family: "monospace"
                textFormat: Text.PlainText
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
                        textFormat: Text.PlainText
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
                        textFormat: Text.PlainText
                    }
                }
            }
        }

        // The second half of a destructive key. It sits where the pairing
        // question does and looks less like it on purpose: this one is about a
        // device you already have, and the loud frame belongs to the one about a
        // stranger. Hidden the moment a real question arrives, so that a y can
        // never mean both.
        Rectangle {
            width: column.width
            height: 26
            visible: root.confirming !== null && root.pending === null
            radius: 4
            color: "#1a1c24"
            border.color: "#3a3d4a"
            border.width: 1

            Text {
                anchors.left: parent.left
                anchors.leftMargin: 8
                anchors.right: parent.right
                anchors.rightMargin: 8
                anchors.verticalCenter: parent.verticalCenter
                text: {
                    const c = root.confirming;
                    if (!c)
                        return "";
                    const what = c.verb === "bluetooth.forget" ? "forget" : "always allow";
                    return what + " " + c.name + "?   y yes   n no";
                }
                elide: Text.ElideRight
                color: "#c9ccd4"
                font.pixelSize: 12
                font.family: "monospace"
                textFormat: Text.PlainText
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
                        root.selected = row.modelData.address;
                        root.confirming = null;
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
                    textFormat: Text.PlainText
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
                    textFormat: Text.PlainText
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
            textFormat: Text.PlainText
        }

        // What else this surface does, said on the surface. A key nobody can
        // see is a key nobody uses.
        Text {
            width: column.width
            text: "enter connect/pair   t trust   f forget   s scan   p power   esc close"
            // Both of the first two ask before they act, and the answer to
            // everything on this surface is y or n.
            color: "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
            textFormat: Text.PlainText
        }
    }
}
