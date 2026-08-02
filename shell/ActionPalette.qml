// The palette (docs/model.md, section 6, launch; scenario W14, "forgot a
// hotkey"). Mod+semicolon asks zded for every action zde has, and this appears
// with them on it: type to narrow the list, Enter to run, and beside each row
// the key that would have done it - because half of what a palette is for is
// learning the key you forgot.
//
// Picker.qml's shape rather than Picker.qml itself. They differ in the one
// thing this surface is: a text field that holds the keyboard while the list
// changes under the cursor. Folding that into the picker would put an input
// into the one surface that says in its own comment why it has none, and a
// picker you drive with j and k would lose those letters to the field.
//
// Not Palette.qml, which is the name it wants. QtQuick has a Palette of its own
// - the colour group behind every control - and an imported module's type beats
// a file sitting in the same directory, so `Palette { }` in shell.qml would
// quietly build a set of colours with none of the properties this file
// declares. A component is named by its file, so the file name is the only
// place that can be fixed. Found by qmllint, which reported every member of it
// as missing.
//
// Rows that cannot run are shown and marked rather than left out. Most of the
// keymap is bound to commands nobody has written yet, and a bind whose command
// is not written is a key that does nothing, silently (README). Somebody who
// has forgotten a key is better served by "that one is not written yet, and
// here is the key it will be" than by a list that pretends the action does not
// exist - and Mod+slash lists them too, so hiding them here would make the
// palette a second, shorter answer to the same question. Enter on one of those
// does nothing and says why on the line below, which is the opposite of what
// the key itself does.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// PanelWindow is an interface Quickshell fills in per platform; see nix/shell.nix.
PanelWindow {
    id: palette

    // What to show, handed over with the event rather than fetched: zded knows
    // it already, and a surface that has to ask before it can draw appears in
    // two steps.
    //
    // A row is { name, desc, key, live, why }. The name is what running one
    // sends back, and it is the only part this surface treats as meaning
    // anything.
    property var rows: []
    property int index: 0

    // The list is every action zde has and the panel shows a screenful, so
    // moving the cursor has to bring the row with it. Driven from here rather
    // than through ListView's own currentIndex: the view writes to that itself
    // when the model changes under it, which is every keystroke in the filter,
    // and a binding it has overwritten stays overwritten.
    onIndexChanged: list.positionViewAtIndex(palette.index, ListView.Contain)

    // What the event asked for, sent back once this is actually up. The asker
    // is waiting on it: without it, "shown" means the socket took the bytes,
    // which a frozen shell also does.
    property string token: ""

    // Set between asking zded to run a row and being told what became of it.
    //
    // The surface used to close on the way out, which meant a refusal came back
    // to nothing: the shell's stream parser drops what it cannot use, so a row
    // zded would not run closed the palette and did nothing at all - the silent
    // key, performed by the surface built to expose it. So it waits, and hides
    // itself only on an answer that worked.
    property bool running: false
    // The last refusal, shown where the eye is until something else is asked
    // for. Kept until then rather than timed out, because the one thing worse
    // than a message nobody reads is one that leaves before they look.
    property string failed: ""

    // chosen(name) is the whole output of this surface. The shell sends it to
    // zded, which does what that action's key would have done.
    signal chosen(string name)
    signal dismissed
    signal shown(string token)

    // What is on screen: a case-insensitive substring of the name or of what
    // the action does, so that `desk` finds the desk group and `lock` finds the
    // lock. No fuzzy matching - a list of sixty rows with a name column does
    // not need scored subsequences, and it would be a dependency to keep.
    readonly property var matches: {
        const q = query.text.toLowerCase();
        if (q === "")
            return palette.rows;
        return palette.rows.filter(r => (r.name + " " + r.desc).toLowerCase().indexOf(q) >= 0);
    }

    // Why the highlighted row would do nothing, said where the eye already is
    // rather than squeezed onto the row. Empty for a row that works, and for an
    // empty list.
    readonly property string reason: {
        const r = palette.matches[palette.index];
        return r && !r.live ? (r.why ?? "") : "";
    }

    function show(newRows, token) {
        const wasUp = palette.visible;
        palette.rows = newRows;
        palette.token = token ?? "";
        query.text = "";
        palette.index = 0;
        palette.running = false;
        palette.failed = "";
        palette.visible = true;
        if (wasUp && palette.token !== "") {
            // Already up, so visible did not change and onVisibleChanged did
            // not fire - and the key that asked for this is waiting to hear
            // that something drew it. Unacknowledged it waits out its whole
            // window and takes the shell-is-dead path, which prints all
            // seventy-odd rows to a keybind's stdout (internal/zded, ackWait).
            palette.shown(palette.token);
        }
    }

    // On the window becoming visible rather than at the end of show(), so what
    // is acknowledged is a surface that exists.
    onVisibleChanged: {
        if (!palette.visible)
            return;
        // The field, not the panel: everything typed here goes into it, and a
        // surface reopened after a run comes back with the keyboard already in
        // the right place.
        query.forceActiveFocus();
        if (palette.token !== "")
            palette.shown(palette.token);
    }

    function hide() {
        palette.visible = false;
    }

    function step(by) {
        const n = palette.matches.length;
        if (n === 0)
            return;
        // A refusal belongs to the row it was about, so moving off that row
        // takes it away rather than leaving it over somebody else's.
        palette.failed = "";
        // Wrapping, like the picker: the list is short once it is filtered, and
        // one that stops at the end makes you look at where the cursor is
        // before pressing a key.
        palette.index = (palette.index + by + n) % n;
    }

    function run() {
        // One at a time: Enter pressed twice while the first answer is on its
        // way would start two of whatever it is.
        if (palette.running)
            return;
        const r = palette.matches[palette.index];
        if (!r)
            return;
        if (!r.live) {
            // Nothing, and the surface stays up with the reason showing. zded
            // would refuse this too, and a refusal nobody sees is the silence
            // the mark exists to break.
            return;
        }
        palette.failed = "";
        palette.running = true;
        palette.chosen(r.name);
    }

    // What became of it, from the shell. Empty means it ran, and the surface
    // has no more reason to be on the screen; anything else is zded's own words
    // about why it did not, which is the half a keypress cannot say.
    function ran(error) {
        palette.running = false;
        if (error === "") {
            palette.hide();
            return;
        }
        palette.failed = error;
    }

    // Driving the filter from outside, for the same reason the picker's choice
    // is reachable over IPC: a machine with no input devices cannot type, and
    // the filtering is the whole of this surface.
    function narrow(text) {
        query.text = text;
    }

    visible: false
    color: "transparent"

    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.namespace: "zde-palette"
    WlrLayershell.keyboardFocus: palette.visible ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None

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
            onClicked: palette.dismissed()
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
        // Three columns wide: the action's name, what it does, and the key.
        width: 680
        // The field, the rows, and the line under them. Capped to the screen,
        // because this list is every action zde has and there are sixty of
        // them.
        height: Math.min(84 + palette.matches.length * 28, palette.height - 80)
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
            cursorVisible: palette.visible

            // A filter that narrowed the list under a cursor left where it was
            // would run whatever happened to move into that row. Back to the
            // top of the view as well as of the list, since the two only move
            // together when the index actually changed.
            onTextChanged: {
                palette.index = 0;
                palette.failed = "";
                list.positionViewAtIndex(0, ListView.Beginning);
            }

            Keys.onPressed: event => {
                switch (event.key) {
                case Qt.Key_Escape:
                    palette.dismissed();
                    break;
                case Qt.Key_Return:
                case Qt.Key_Enter:
                    palette.run();
                    break;
                case Qt.Key_Down:
                    palette.step(1);
                    break;
                case Qt.Key_Up:
                    palette.step(-1);
                    break;
                default:
                    // Ctrl+n and Ctrl+p, because the arrows are a hand off the
                    // home row and j and k are letters somebody is typing.
                    if ((event.modifiers & Qt.ControlModifier) && event.key === Qt.Key_N)
                        palette.step(1);
                    else if ((event.modifiers & Qt.ControlModifier) && event.key === Qt.Key_P)
                        palette.step(-1);
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
            // Three things in one line, in the order they matter. What zded
            // said about the row somebody just tried to run; failing that, why
            // the highlighted row would do nothing; failing that, the keys.
            // All of it takes the hint's place because each is more useful than
            // the hint exactly when it is there.
            text: {
                if (palette.failed !== "")
                    return palette.failed;
                if (palette.reason !== "")
                    return palette.reason;
                return "enter run    ctrl+n/p move    esc close";
            }
            color: {
                if (palette.failed !== "")
                    return "#e5484d";
                return palette.reason !== "" ? "#e5a23d" : "#7a7f8a";
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

            model: palette.matches

            delegate: Rectangle {
                id: row

                required property var modelData
                required property int index

                width: list.width
                height: 26
                radius: 4
                color: row.index === palette.index ? "#2a2c37" : "transparent"

                MouseArea {
                    anchors.fill: parent
                    onClicked: {
                        palette.index = row.index;
                        palette.run();
                    }
                }

                Text {
                    id: name

                    anchors.left: parent.left
                    anchors.leftMargin: 8
                    anchors.verticalCenter: parent.verticalCenter
                    width: 210
                    text: row.modelData.name
                    elide: Text.ElideRight
                    // A row that cannot run is dimmed rather than hidden, and
                    // its key stays legible: the key is what somebody came here
                    // to find out.
                    color: row.modelData.live ? "#c9ccd4" : "#6a6f7a"
                    font.pixelSize: 13
                    font.family: "monospace"
                }

                Text {
                    anchors.left: name.right
                    anchors.leftMargin: 10
                    anchors.right: key.left
                    anchors.rightMargin: 10
                    anchors.verticalCenter: parent.verticalCenter
                    text: row.modelData.desc
                    elide: Text.ElideRight
                    color: row.modelData.live ? "#7a7f8a" : "#565b66"
                    font.pixelSize: 12
                    font.family: "monospace"
                }

                Text {
                    id: key

                    anchors.right: parent.right
                    anchors.rightMargin: 8
                    anchors.verticalCenter: parent.verticalCenter
                    // A dash rather than nothing: an action with no key is a
                    // thing you can only reach from here, and a blank column
                    // reads as a missing answer.
                    text: row.modelData.key !== "" ? row.modelData.key : "-"
                    color: row.modelData.live ? "#7a7f8a" : "#565b66"
                    font.pixelSize: 12
                    font.family: "monospace"
                }
            }
        }
    }
}
