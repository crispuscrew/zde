// The picker (docs/roadmap.md, 0.1: shell MVP). Mod+Tab asks zded for the
// desks and Mod+w for the open windows; zded tells whoever is listening, and
// this appears on the screen being looked at with those rows on it.
//
// One surface for both. They differ only in what a row says and in what
// choosing one means, and neither is this surface's business: it is handed
// rows, and it hands back the key of the row that was chosen. A second copy of
// the keyboard handling, the focus dance and the click targets would be a
// second place for any of them to stop working, and the two would drift.
//
// It is the first thing zde draws that takes the keyboard, which is a question
// the roadmap has been carrying: a layer surface holding focus reads to niri as
// nothing focused at all, so a nav key pressed while this is up spends itself
// putting focus back on a window. That is why this takes focus only while it is
// visible, and gives it back the moment a row is chosen or the picker is
// dismissed.
//
// No text field. Filtering is the palette's job (0.2), and a picker you drive
// with the arrows, j/k, or a digit needs no focused input to lose.
//
// Every Text in this file sets textFormat: Text.PlainText, including the ones
// that only draw a literal. Qt Quick's default is AutoText, which renders a
// string that looks like markup as StyledText, and StyledText fetches an
// <img src="http://..."> over the network - out of a process that is holding a
// layer surface and a keyboard grab. internal/zded/qml_test.go refuses a Text
// with no format, and carries the whole of why.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// PanelWindow is an interface Quickshell fills in per platform; see nix/shell.nix.
PanelWindow {
    id: picker

    // What to show, handed over with the event rather than fetched: zded knows
    // it already, and a surface that has to ask before it can draw appears in
    // two steps.
    //
    // A row is { key, label, note }. The key is what choosing sends back - a
    // desk name, a window id - and it is the only part this surface treats as
    // meaning anything.
    property var rows: []
    // What these rows are, in one word. The surface draws nothing with it: it
    // is how a test driving this over IPC can tell a window picker that opened
    // when Mod+Tab was pressed from the desk picker that should have.
    property string kind: ""
    // What choosing a row will do, in words, where it is not "go there". The
    // desks look identical whether Enter switches to one or sends the focused
    // window to it, so the verb has to be on the surface: a picker that says
    // nothing is one where the digit you press means whatever the last key
    // pressed decided. Empty for the switcher and the window picker, which are
    // the two that do what the rows already look like they do.
    property string caption: ""
    // The key of the row you are already on, where there is one.
    property string on: ""
    property int index: 0

    // What the event asked for, sent back once this is actually up. The asker
    // is waiting on it: without it, "shown" means the socket took the bytes,
    // which a frozen shell also does.
    property string token: ""

    // chosen(key) is the whole output of this surface. The shell sends these to
    // zded; the picker itself knows nothing about sockets.
    signal chosen(string key)
    signal dismissed
    signal shown(string token)
    // A letter that is not navigation runs an action and closes. The picker
    // holds the keyboard while it is up, which is what makes a second keypress
    // readable at all - the keymap has been carrying Mod+Tab-then-L as
    // something that needed the input daemon, and it turns out the surface that
    // grabs focus can do it.
    signal action(string name)

    function show(kind, newRows, here, token, caption) {
        const wasUp = picker.visible;
        picker.kind = kind;
        picker.rows = newRows;
        picker.on = here ?? "";
        picker.token = token ?? "";
        picker.caption = caption ?? "";
        // Start on the row you are already on, so Enter alone is a no-op rather
        // than a surprise, and one press of Down is the next one. A list with no
        // such row - the windows, where the one you are looking at is not a
        // place you would press Enter to reach - starts at the first.
        picker.index = Math.max(0, newRows.findIndex(r => r.key === picker.on));
        picker.visible = true;
        if (wasUp && picker.token !== "") {
            // Already up, so visible did not change and onVisibleChanged did not
            // fire - and the key that asked for this is waiting to hear that
            // something drew it. Unacknowledged, it waits out its whole window
            // and takes the shell-is-dead path, which prints every row to a
            // keybind's stdout: Mod+w with the picker already open did exactly
            // that, on this same surface (internal/zded, ackWait).
            picker.shown(picker.token);
        }
    }

    // On the window becoming visible rather than at the end of show(), so what
    // is acknowledged is a surface that exists. It is still not a painted frame
    // - nothing short of a frame callback is - but it is the difference between
    // "the shell read a socket" and "the shell built the thing".
    onVisibleChanged: {
        if (picker.visible && picker.token !== "")
            picker.shown(picker.token);
    }

    function hide() {
        picker.visible = false;
    }

    function step(by) {
        const n = picker.rows.length;
        if (n === 0)
            return;
        // Wrapping, because the list is short and a picker that stops at the
        // end makes you look at where the cursor is before pressing a key.
        picker.index = (picker.index + by + n) % n;
    }

    function choose(i) {
        if (i >= 0 && i < picker.rows.length)
            picker.chosen(picker.rows[i].key);
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

        // Presses inside the panel stop here. Without this the panel is not a
        // mouse target at all and a click on the row you want falls through to
        // the dim layer behind, which dismisses - so clicking a row did the
        // opposite of choosing it.
        MouseArea {
            anchors.fill: parent
        }

        anchors.centerIn: parent
        // Wider than a desk name needs, because a window row carries an app and
        // a title as well as where it is, and at 420 almost every one of them
        // was elided down to the app.
        width: 520
        // The rows, their margins, and the hint line under them. Leave the hint
        // out of the sum and it draws over the last row on a full list.
        height: Math.min(list.implicitHeight + 46, picker.height - 80)
        color: "#11121a"
        border.color: "#2a2c37"
        border.width: 1
        radius: 6
        // The height is capped to the screen, so past about twenty rows they
        // would otherwise draw outside the panel and over the wallpaper.
        clip: true

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
                picker.choose(picker.index);
                break;
            case Qt.Key_Down:
                picker.step(1);
                break;
            case Qt.Key_Up:
                picker.step(-1);
                break;
            default:
                // j and k for a hand on home row, and a digit for the row in
                // that position - which is the fastest way to something you can
                // see, and the reason the rows are numbered.
                if (event.text === "j")
                    picker.step(1);
                else if (event.text === "k")
                    picker.step(-1);
                else if (event.text >= "1" && event.text <= "9")
                    picker.choose(parseInt(event.text, 10) - 1);
                else if (event.text === "l")
                    picker.action("lock");
                else {
                    return;
                }
            }
            event.accepted = true;
        }

        // What else this surface does, said on the surface. A leader key nobody
        // can see is a leader key nobody uses, and this is the whole reason a
        // menu beats a chord you have to remember.
        Text {
            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.margins: 12
            // "pick" rather than "desk": one surface lists the desks on Mod+Tab
            // and the open windows on Mod+w, and a hint that names one of them
            // is wrong half the time it is read.
            //
            // The caption goes on this line rather than above the rows: the
            // panel's height is the rows plus this, so a header would have to
            // be added to that sum in a second place to stop it drawing over
            // the last row.
            text: (picker.caption === "" ? "" : picker.caption + "    ") + "1-9 pick    j k move    l lock    esc close"
            color: "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
            textFormat: Text.PlainText
        }

        Column {
            id: list

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.margins: 12
            spacing: 2

            Repeater {
                model: picker.rows

                Rectangle {
                    id: row

                    required property var modelData
                    required property int index

                    width: list.width
                    height: 26
                    radius: 4
                    color: row.index === picker.index ? "#2a2c37" : "transparent"

                    // Clickable, because a list you can see is a list somebody
                    // will click, and a keyboard-first desktop is not a
                    // mouse-hostile one.
                    MouseArea {
                        anchors.fill: parent
                        onClicked: picker.chosen(row.modelData.key)
                    }

                    Text {
                        anchors.left: parent.left
                        anchors.leftMargin: 8
                        anchors.right: note.left
                        anchors.rightMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        text: (row.index < 9 ? (row.index + 1) + "  " : "   ") + row.modelData.label
                        // A window title is as long as the app felt like making
                        // it, and a row running out past the panel reads as the
                        // panel being broken rather than the title being long.
                        elide: Text.ElideRight
                        color: "#c9ccd4"
                        font.pixelSize: 13
                        font.family: "monospace"
                        textFormat: Text.PlainText
                    }

                    // The note: where you are for a desk, where the window is
                    // for a window. Said rather than implied by the highlight,
                    // which means something else here - the highlight is what
                    // Enter would take.
                    Text {
                        id: note

                        anchors.right: parent.right
                        anchors.rightMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        text: row.modelData.note ?? ""
                        color: "#7a7f8a"
                        font.pixelSize: 12
                        font.family: "monospace"
                        textFormat: Text.PlainText
                    }
                }
            }
        }
    }
}
