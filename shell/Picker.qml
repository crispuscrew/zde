// The desk picker (docs/roadmap.md, 0.1: shell MVP). Mod+Tab asks zded for it,
// zded tells whoever is listening, and this appears on the screen being looked
// at with the desks on it.
//
// It is the first thing zde draws that takes the keyboard, which is a question
// the roadmap has been carrying: a layer surface holding focus reads to niri as
// nothing focused at all, so a nav key pressed while this is up spends itself
// putting focus back on a window. That is why this takes focus only while it is
// visible, and gives it back the moment a desk is chosen or the picker is
// dismissed.
//
// No text field. Filtering is the palette's job (0.2), and a picker you drive
// with the arrows, j/k, or a digit needs no focused input to lose.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// PanelWindow is an interface Quickshell fills in per platform; see nix/shell.nix.
PanelWindow {
    id: picker

    // What to show, handed over with the event rather than fetched: zded knows
    // the desks, and a surface that has to ask before it can draw appears in
    // two steps.
    property var desks: []
    property string on: ""
    property int index: 0

    // chosen(name) is the whole output of this surface. The shell sends it to
    // zded; the picker itself knows nothing about sockets.
    signal chosen(string name)
    signal dismissed

    function show(list, here) {
        picker.desks = list;
        picker.on = here;
        // Start on the desk you are on, so Enter alone is a no-op rather than a
        // surprise, and one press of Down is the next desk.
        picker.index = Math.max(0, list.indexOf(here));
        picker.visible = true;
    }

    function hide() {
        picker.visible = false;
    }

    function step(by) {
        const n = picker.desks.length;
        if (n === 0)
            return;
        // Wrapping, because the list is short and a picker that stops at the
        // end makes you look at where the cursor is before pressing a key.
        picker.index = (picker.index + by + n) % n;
    }

    visible: false
    color: "transparent"

    // Above the bar, and only ours while it is up.
    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.namespace: "zde-picker"
    WlrLayershell.keyboardFocus: picker.visible ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None

    // The whole screen, so a click anywhere outside dismisses and the panel can
    // sit in the middle of it. No exclusive zone: this is not furniture, it is
    // a moment.
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
            onClicked: picker.dismissed()
        }
    }

    Rectangle {
        id: panel

        anchors.centerIn: parent
        width: 420
        height: Math.min(rows.implicitHeight + 24, picker.height - 80)
        color: "#11121a"
        border.color: "#2a2c37"
        border.width: 1
        radius: 6

        // focus lives here, and the surface only holds the keyboard while it is
        // visible, so nothing is listening for keys when the picker is not up.
        focus: true
        Keys.onPressed: event => {
            switch (event.key) {
            case Qt.Key_Escape:
                picker.dismissed();
                break;
            case Qt.Key_Return:
            case Qt.Key_Enter:
                if (picker.index >= 0 && picker.index < picker.desks.length)
                    picker.chosen(picker.desks[picker.index]);
                break;
            case Qt.Key_Down:
                picker.step(1);
                break;
            case Qt.Key_Up:
                picker.step(-1);
                break;
            default:
                // j and k for a hand on home row, and a digit for the desk in
                // that position - which is the fastest way to a desk you can
                // see, and the reason the rows are numbered.
                if (event.text === "j")
                    picker.step(1);
                else if (event.text === "k")
                    picker.step(-1);
                else if (event.text >= "1" && event.text <= "9") {
                    const want = parseInt(event.text, 10) - 1;
                    if (want < picker.desks.length)
                        picker.chosen(picker.desks[want]);
                } else {
                    return;
                }
            }
            event.accepted = true;
        }

        Column {
            id: rows

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.margins: 12
            spacing: 2

            Repeater {
                model: picker.desks

                Rectangle {
                    id: row

                    required property string modelData
                    required property int index

                    width: rows.width
                    height: 26
                    radius: 4
                    color: row.index === picker.index ? "#2a2c37" : "transparent"

                    Text {
                        anchors.left: parent.left
                        anchors.leftMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        text: (row.index < 9 ? (row.index + 1) + "  " : "   ") + row.modelData
                        color: "#c9ccd4"
                        font.pixelSize: 13
                        font.family: "monospace"
                    }

                    // Where you are, said rather than implied by the highlight,
                    // which means something else here: the highlight is what
                    // Enter would take.
                    Text {
                        anchors.right: parent.right
                        anchors.rightMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        text: row.modelData === picker.on ? "here" : ""
                        color: "#7a7f8a"
                        font.pixelSize: 12
                        font.family: "monospace"
                    }
                }
            }
        }
    }
}
