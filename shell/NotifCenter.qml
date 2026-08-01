// The notification center (docs/roadmap.md, 0.1: shell MVP). Mod+n asks zded
// for the history, zded tells whoever is listening, and this appears on the
// screen being looked at with what arrived on it.
//
// It is the other half of principle 3 (docs/vision.md): the modes stop things
// interrupting you, and this is where you find out what they stopped. So a row
// says what became of it - waiting, done, or silent because a mode kept it off
// the queue - and never leaves that to be inferred from its absence.
//
// The same shape as Picker.qml, and deliberately not the same surface. A picker
// hands back the key of a row and knows nothing else; this one has two verbs on
// a row (act on it, or take it off) and something to say back when a verb was
// not possible. Sharing one surface would have meant a mode flag inside it,
// which is the point where one surface becomes two badly.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// PanelWindow is an interface Quickshell fills in per platform; see nix/shell.nix.
PanelWindow {
    id: center

    // What arrived, newest first, handed over with the event rather than
    // fetched: zded has it already, and a surface that has to ask before it can
    // draw appears in two steps.
    //
    // A row is one attn.Record as it comes off the socket: id, from, text,
    // body, urgent, at, queued, dismissed, action.
    property var rows: []
    property int index: 0

    // What this surface has to say back, shown under the rows. The one thing a
    // read-only list cannot do is explain why a key did nothing, and most rows
    // here have nothing to invoke - zde does not claim the notification spec's
    // actions capability, so most apps never send one.
    property string note: ""

    // What the event asked for, sent back once this is actually up. The asker
    // is waiting on it: without it, "shown" means the socket took the bytes,
    // which a frozen shell also does.
    property string token: ""

    // invoke(id) fires a notification's default action; drop(id) takes it off.
    // Both go to zded over the socket - this surface knows nothing about
    // sockets, and decides nothing (docs/vision.md, section 2).
    signal invoke(string id)
    signal drop(string id)
    signal dismissed
    signal shown(string token)

    function show(newRows, newToken) {
        center.rows = newRows ?? [];
        center.token = newToken ?? "";
        center.note = "";
        // The newest, which is what somebody opening this is looking for.
        center.index = 0;
        center.visible = true;
    }

    // On the window becoming visible rather than at the end of show(), so what
    // is acknowledged is a surface that exists.
    onVisibleChanged: {
        if (center.visible && center.token !== "")
            center.shown(center.token);
    }

    function hide() {
        center.visible = false;
    }

    function step(by) {
        const n = center.rows.length;
        if (n === 0)
            return;
        center.index = (center.index + by + n) % n;
    }

    function rowAt(i) {
        if (i < 0 || i >= center.rows.length)
            return null;
        return center.rows[i];
    }

    // Enter. A row whose app declared no default action is refused here rather
    // than by zded, so the answer arrives with the keypress instead of after a
    // round trip - and zded refuses it too, because the shell is not where a
    // rule lives.
    function act() {
        const r = center.rowAt(center.index);
        if (!r)
            return;
        if (!r.action) {
            center.note = "nothing to invoke: " + (r.from ?? "it") + " sent a notification, not a button";
            return;
        }
        center.note = "sent to " + (r.from ?? "it");
        center.invoke(String(r.id));
    }

    // d. Marked here as well as asked of zded: the reply carries no id, so a
    // row that waited for confirmation would sit there looking unchanged.
    function dismiss() {
        const r = center.rowAt(center.index);
        if (!r || r.dismissed)
            return;
        center.drop(String(r.id));
        center.rows = center.rows.map(x => x.id === r.id ? Object.assign({}, x, {
                dismissed: true
            }) : x);
        center.note = "";
    }

    // What became of one, in a word. "silent" is the one worth having: it says
    // a mode kept this off the queue, which is the difference between an app
    // that stopped sending and a session that stopped listening.
    function became(r) {
        if (r.dismissed)
            return "done";
        if (r.queued)
            return "waiting";
        return "silent";
    }

    // The clock time it arrived. A day and a time would be more precise and
    // less readable, and the history is bounded at a day or two of use.
    function when(r) {
        const d = new Date(r.at);
        return isNaN(d.getTime()) ? "--:--" : Qt.formatDateTime(d, "HH:mm");
    }

    visible: false
    color: "transparent"

    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.namespace: "zde-notif-center"
    // The keyboard only while it is up, and given back the moment it closes: a
    // layer surface holding focus reads to niri as nothing focused at all, so
    // one that kept it would eat the next nav key (see Picker.qml).
    WlrLayershell.keyboardFocus: center.visible ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None

    anchors {
        top: true
        bottom: true
        left: true
        right: true
    }
    // qmllint enable uncreatable-type

    Rectangle {
        anchors.fill: parent
        color: "#000000"
        opacity: 0.35

        MouseArea {
            anchors.fill: parent
            onClicked: center.dismissed()
        }
    }

    Rectangle {
        id: panel

        // Presses inside the panel stop here, or a click on the row you want
        // falls through to the dim layer behind and closes the surface.
        MouseArea {
            anchors.fill: parent
        }

        anchors.centerIn: parent
        // Wider than the picker: a row here carries a time, a sender, the text
        // and what became of it, and at the picker's width the text was the
        // only part that got elided away.
        width: 640
        // The rows, their margins, the body line and the hint under them.
        height: Math.min(list.implicitHeight + 76, center.height - 80)
        color: "#11121a"
        border.color: "#2a2c37"
        border.width: 1
        radius: 6
        clip: true

        focus: true
        Keys.onPressed: event => {
            switch (event.key) {
            case Qt.Key_Escape:
                center.dismissed();
                break;
            case Qt.Key_Return:
            case Qt.Key_Enter:
                center.act();
                break;
            case Qt.Key_Down:
                center.step(1);
                break;
            case Qt.Key_Up:
                center.step(-1);
                break;
            default:
                // A digit moves the highlight rather than acting on the row.
                // In the picker a digit chooses, because choosing a desk is
                // reversible; here the two verbs invoke somebody's app and take
                // a notification away, and neither is a thing to do by
                // mistyping a workspace number.
                if (event.text === "j")
                    center.step(1);
                else if (event.text === "k")
                    center.step(-1);
                else if (event.text === "d")
                    center.dismiss();
                else if (event.text >= "1" && event.text <= "9")
                    center.index = Math.min(parseInt(event.text, 10) - 1, center.rows.length - 1);
                else {
                    return;
                }
            }
            event.accepted = true;
        }

        // Nothing has arrived. Said rather than left blank: an empty panel and
        // a broken one look the same.
        Text {
            anchors.centerIn: parent
            visible: center.rows.length === 0
            text: "nothing has arrived"
            color: "#7a7f8a"
            font.pixelSize: 13
            font.family: "monospace"
        }

        // The body of the row you are on, which is the half a one-line list
        // cannot show. A notification is meant to land in history with its full
        // text (docs/vision.md, principle 3), and this is where the rest of it
        // is.
        Text {
            id: body

            anchors.bottom: hint.top
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.margins: 12
            anchors.bottomMargin: 6
            text: {
                const r = center.rowAt(center.index);
                if (center.note !== "")
                    return center.note;
                return r && r.body ? r.body : "";
            }
            color: center.note !== "" ? "#e5a23d" : "#9aa0ac"
            elide: Text.ElideRight
            font.pixelSize: 12
            font.family: "monospace"
        }

        Text {
            id: hint

            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.margins: 12
            text: "j k move    enter act    d dismiss    esc close"
            color: "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
        }

        Column {
            id: list

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.margins: 12
            spacing: 2

            Repeater {
                model: center.rows

                Rectangle {
                    id: row

                    required property var modelData
                    required property int index

                    width: list.width
                    height: 26
                    radius: 4
                    color: row.index === center.index ? "#2a2c37" : "transparent"

                    MouseArea {
                        anchors.fill: parent
                        onClicked: center.index = row.index
                    }

                    Text {
                        anchors.left: parent.left
                        anchors.leftMargin: 8
                        anchors.right: mark.left
                        anchors.rightMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        text: (row.index < 9 ? (row.index + 1) + "  " : "   ") + center.when(row.modelData) + "  " + (row.modelData.urgent ? "! " : "  ") + (row.modelData.from ?? "-") + "  " + row.modelData.text
                        elide: Text.ElideRight
                        // Dimmed once it is done, so the list reads as what is
                        // left rather than as everything that ever happened.
                        color: row.modelData.dismissed ? "#7a7f8a" : (row.modelData.urgent ? "#e5484d" : "#c9ccd4")
                        font.pixelSize: 13
                        font.family: "monospace"
                    }

                    Text {
                        id: mark

                        anchors.right: parent.right
                        anchors.rightMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        text: center.became(row.modelData)
                        color: "#7a7f8a"
                        font.pixelSize: 12
                        font.family: "monospace"
                    }
                }
            }
        }
    }
}
