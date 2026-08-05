// The clipboard history (docs/model.md, section 6, clip; scenario W24). Mod+v
// asks zded for what was copied recently and this appears with it: type to
// narrow the list, Enter to put an entry back on the clipboard, and the next
// paste is the thing you chose.
//
// ActionPalette.qml's shape, deliberately and almost line for line. They are
// the same surface - a text field holding the keyboard while a list changes
// under the cursor - and the two things this one adds are what a row says and
// what choosing one means, neither of which is worth a second copy of the
// focus dance, the acknowledgement or the run-and-wait. Not folded into the
// palette itself, because a palette that also holds clipboard entries is a
// surface where Enter does two unrelated things depending on which row the
// filter happened to leave under the cursor.
//
// What is on a row is a preview and never the whole entry: zded keeps the text
// and hands over the first couple of hundred characters of it (internal/clip,
// PreviewMax), so what was copied lives in one process rather than two and
// nothing here has to expire anything. The cost, which is the honest half: the
// filter matches what is on the screen, so an entry is found by what you can
// see of it.
//
// Rows that cannot be put back are shown and marked, the way the palette marks
// an action nobody has written. An image or a file was not kept - the history
// is text only - and a person who copied one is better served by a row saying
// so than by a list that appears to have missed it.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// PanelWindow is an interface Quickshell fills in per platform; see nix/shell.nix.
PanelWindow {
    id: clips

    // What to show, handed over with the event rather than fetched: zded knows
    // it already, and a surface that has to ask before it can draw appears in
    // two steps.
    //
    // A row is { id, preview, cut, bytes, kind, why, at }. The id is what
    // choosing one sends back, and it is the only part this surface treats as
    // meaning anything.
    property var rows: []
    property int index: 0

    // The list is longer than the panel, so moving the cursor has to bring the
    // row with it. Driven from here rather than through ListView's own
    // currentIndex, which the view rewrites itself every time the model changes
    // - and the model changes on every keystroke in the filter.
    onIndexChanged: list.positionViewAtIndex(clips.index, ListView.Contain)

    // What the event asked for, sent back once this is actually up. The asker
    // is waiting on it: without it, "shown" means the socket took the bytes,
    // which a frozen shell also does.
    property string token: ""

    // Set between asking zded to put an entry back and being told what became
    // of it. The surface stays up until then, because the interesting refusal
    // is one only this surface can explain: an entry expires while the list of
    // it is on the screen, and Enter on that row has to say so rather than
    // closing on nothing.
    property bool running: false
    property string failed: ""

    // chosen(entry) is the whole output of this surface: the id of a row. The
    // shell sends it to zded, which puts that entry back on the clipboard.
    // Named entry rather than id, because `id` means something else in every
    // other line of a QML file.
    signal chosen(string entry)
    signal dismissed
    signal shown(string token)

    // What is on screen: a case-insensitive substring of the preview, which is
    // the whole of what this surface knows about an entry. No fuzzy matching,
    // for the reason the palette gives - fifty rows do not need scored
    // subsequences, and it would be a dependency to keep.
    readonly property var matches: {
        const q = query.text.toLowerCase();
        if (q === "")
            return clips.rows;
        return clips.rows.filter(r => (r.preview ?? "").toLowerCase().indexOf(q) >= 0);
    }

    // Why the highlighted row cannot be put back, said where the eye already is
    // rather than squeezed onto the row. Empty for an ordinary entry.
    readonly property string reason: {
        const r = clips.matches[clips.index];
        return r && r.kind !== "text" ? (r.why ?? "") : "";
    }

    function show(newRows, token) {
        const wasUp = clips.visible;
        clips.rows = newRows;
        clips.token = token ?? "";
        query.text = "";
        clips.index = 0;
        clips.running = false;
        clips.failed = "";
        clips.visible = true;
        if (wasUp && clips.token !== "") {
            // Already up, so visible did not change and onVisibleChanged did
            // not fire - and the key that asked for this is waiting to hear
            // that something drew it. Unacknowledged it waits out its whole
            // window and takes the shell-is-dead path, which prints the list to
            // a keybind's stdout (internal/zded, ackWait).
            clips.shown(clips.token);
        }
    }

    // On the window becoming visible rather than at the end of show(), so what
    // is acknowledged is a surface that exists.
    onVisibleChanged: {
        if (!clips.visible)
            return;
        // The field, not the panel: everything typed here goes into it.
        query.forceActiveFocus();
        if (clips.token !== "")
            clips.shown(clips.token);
    }

    function hide() {
        clips.visible = false;
    }

    function step(by) {
        const n = clips.matches.length;
        if (n === 0)
            return;
        // A refusal belongs to the row it was about, so moving off that row
        // takes it away rather than leaving it over somebody else's.
        clips.failed = "";
        clips.index = (clips.index + by + n) % n;
    }

    function put() {
        // One at a time: Enter pressed twice while the first answer is on its
        // way would write the clipboard twice.
        if (clips.running)
            return;
        const r = clips.matches[clips.index];
        if (!r)
            return;
        if (r.kind !== "text") {
            // Nothing, and the surface stays up with the reason showing. There
            // is no content behind this row to put anywhere.
            return;
        }
        clips.failed = "";
        clips.running = true;
        clips.chosen(String(r.id));
    }

    // What became of it, from the shell. Empty means the clipboard now holds
    // that entry and there is nothing left to look at; anything else is zded's
    // own words - an entry that expired while this was open is the one worth
    // reading.
    function ran(error) {
        clips.running = false;
        if (error === "") {
            clips.hide();
            return;
        }
        clips.failed = error;
    }

    // Driving the filter from outside, for the same reason the palette's is
    // reachable over IPC: a machine with no input devices cannot type, and the
    // filtering is the whole of this surface.
    function narrow(text) {
        query.text = text;
    }

    visible: false
    color: "transparent"

    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.namespace: "zde-clip"
    WlrLayershell.keyboardFocus: clips.visible ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None

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
            onClicked: clips.dismissed()
        }
    }

    Rectangle {
        id: panel

        // Presses inside the panel stop here, or a click on the row you want
        // falls through to the dim layer behind and dismisses instead.
        MouseArea {
            anchors.fill: parent
        }

        anchors.centerIn: parent
        // Wider than the palette's: a clipboard entry is a sentence and not a
        // name, and the whole point of a row is recognising it.
        width: 760
        height: Math.min(84 + clips.matches.length * 28, clips.height - 80)
        color: "#11121a"
        border.color: "#2a2c37"
        border.width: 1
        radius: 6
        clip: true

        TextInput {
            id: query

            anchors.top: parent.top
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.margins: 12
            height: 20

            focus: true
            color: "#c9ccd4"
            font.pixelSize: 14
            font.family: "monospace"
            cursorVisible: clips.visible

            // A filter that narrowed the list under a cursor left where it was
            // would put back whatever happened to move into that row.
            onTextChanged: {
                clips.index = 0;
                clips.failed = "";
                list.positionViewAtIndex(0, ListView.Beginning);
            }

            Keys.onPressed: event => {
                switch (event.key) {
                case Qt.Key_Escape:
                    clips.dismissed();
                    break;
                case Qt.Key_Return:
                case Qt.Key_Enter:
                    clips.put();
                    break;
                case Qt.Key_Down:
                    clips.step(1);
                    break;
                case Qt.Key_Up:
                    clips.step(-1);
                    break;
                default:
                    // Ctrl+n and Ctrl+p, because the arrows are a hand off the
                    // home row and j and k are letters somebody is typing.
                    if ((event.modifiers & Qt.ControlModifier) && event.key === Qt.Key_N)
                        clips.step(1);
                    else if ((event.modifiers & Qt.ControlModifier) && event.key === Qt.Key_P)
                        clips.step(-1);
                    else
                        return;
                }
                event.accepted = true;
            }

            Text {
                anchors.fill: parent
                visible: query.text === ""
                text: "type to filter"
                color: "#5a5f6a"
                font.pixelSize: 14
                font.family: "monospace"
            }
        }

        Text {
            id: hint

            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.margins: 12
            elide: Text.ElideRight
            // Three things in one line, in the order they matter: what zded
            // said about the row somebody just pressed Enter on, failing that
            // why the highlighted row cannot be put back, failing that the
            // keys. The last of them says the two facts about this list that
            // are not visible in it - entries go on their own, and there is
            // nothing here that anybody marked as a secret.
            text: {
                if (clips.failed !== "")
                    return clips.failed;
                if (clips.reason !== "")
                    return clips.reason;
                if (clips.rows.length === 0)
                    return "nothing copied recently: entries expire, and secrets are never recorded";
                return "enter copy    ctrl+n/p move    esc close";
            }
            color: {
                if (clips.failed !== "")
                    return "#e5484d";
                return clips.reason !== "" ? "#e5a23d" : "#7a7f8a";
            }
            font.pixelSize: 11
            font.family: "monospace"
        }

        ListView {
            id: list

            anchors.top: query.bottom
            anchors.bottom: hint.top
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.margins: 12
            spacing: 2
            clip: true

            model: clips.matches

            delegate: Rectangle {
                id: row

                required property var modelData
                required property int index

                // An entry with content, which is the only kind Enter does
                // anything with.
                readonly property bool putBack: row.modelData.kind === "text"

                width: list.width
                height: 26
                radius: 4
                color: row.index === clips.index ? "#2a2c37" : "transparent"

                MouseArea {
                    anchors.fill: parent
                    onClicked: {
                        clips.index = row.index;
                        clips.put();
                    }
                }

                Text {
                    anchors.left: parent.left
                    anchors.leftMargin: 8
                    anchors.right: when.left
                    anchors.rightMargin: 10
                    anchors.verticalCenter: parent.verticalCenter
                    // The preview for an entry, and for a row that was not kept
                    // the reason instead: there is no content to show, and the
                    // reason is the whole of what happened.
                    text: {
                        if (!row.putBack)
                            return row.modelData.why ?? "";
                        // An entry longer than its preview says so, or a
                        // sentence that was cut reads as one that ended.
                        return (row.modelData.preview ?? "") + (row.modelData.cut ? " ..." : "");
                    }
                    elide: Text.ElideRight
                    color: row.putBack ? "#c9ccd4" : "#6a6f7a"
                    font.pixelSize: 13
                    font.family: "monospace"
                }

                // When it was copied, which is how two similar rows are told
                // apart - and the only thing on this surface that says the list
                // is a list of moments rather than of things.
                Text {
                    id: when

                    anchors.right: parent.right
                    anchors.rightMargin: 8
                    anchors.verticalCenter: parent.verticalCenter
                    text: Qt.formatDateTime(new Date(row.modelData.at), "HH:mm")
                    color: "#7a7f8a"
                    font.pixelSize: 12
                    font.family: "monospace"
                }
            }
        }
    }
}
