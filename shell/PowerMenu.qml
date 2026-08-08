// The power menu (docs/model.md, section 6: system.power). Mod+Shift+x asks
// zded for the five things that end a session or a machine, and this appears
// with them on it: lock, log out, suspend, reboot, power off.
//
// Picker.qml's shape rather than the palette's, because there is nothing to
// type: five rows are a list you point at, and a text field would be a focused
// input to lose the j and the k to.
//
// What it adds to the picker is the second question. Three of these rows end
// things nobody can get back, so choosing one does not run it - it puts what is
// about to be lost on the screen and waits for a y. The lines are zded's, not
// this file's: how many windows close, what the notification center is holding
// that the queue never got, who else is logged in, what is holding sleep. A
// confirmation that only asked "are you sure" would teach people to press
// through it, which is the habit this surface exists not to build.
//
// y and not Enter. Enter is the key that chose the row, and a person who
// presses it twice in the rhythm of choosing something would power the machine
// off with the second press - so the key that confirms is deliberately not the
// key that got here, and it is on the screen beside the question. Space says
// yes as well, and only because y cannot be typed on every keyboard this runs
// on; the reasoning is beside the code that reads them.
//
// Not Power.qml: a file is a QML type, and a name that generic is one to
// collide with. ActionPalette.qml carries the same scar - QtQuick has a Palette
// of its own, and an imported module's type beats a file in the same directory,
// so the surface silently became a set of colours.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// PanelWindow is an interface Quickshell fills in per platform; see nix/shell.nix.
PanelWindow {
    id: menu

    // What to show, handed over with the event rather than fetched: zded knows
    // it already, and a confirmation that had to ask before it could say what
    // is being lost would fill in under somebody's finger.
    //
    // A row is { name, label, desc, confirm, costs, why }. The name is what
    // choosing one sends back, and it is the only part this surface treats as
    // meaning anything.
    property var rows: []
    property int index: 0

    // The name of the row waiting for a y, and empty when the list is live.
    // Deliberately not called `state`: every Item has one of those, and
    // shadowing it has already broken this shell once.
    property string asking: ""

    // What the event asked for, sent back once this is actually up. The asker
    // is waiting on it: without it, "shown" means the socket took the bytes,
    // which a frozen shell also does.
    property string token: ""

    // Set between asking zded to run a row and being told what became of it.
    // The surface stays up until then, for the reason the palette does: a
    // refusal that arrived after it closed would be dropped by the shell's
    // parser, and a logind that said no would look exactly like a key that did
    // nothing.
    property bool running: false
    property string failed: ""

    signal chosen(string name)
    signal dismissed
    signal shown(string token)

    readonly property var here: menu.rows[menu.index] ?? null
    // The row being asked about, found by name rather than kept as an index:
    // the list is handed over whole on every open, and an index would outlive
    // the rows it pointed into.
    readonly property var asked: {
        for (const r of menu.rows) {
            if (r.name === menu.asking)
                return r;
        }
        return null;
    }

    // What is under the list, in the order it matters: what zded said about the
    // row somebody just ran, then what the row being confirmed is about to
    // cost, then whatever the highlighted row would cost or why it would do
    // nothing at all.
    readonly property var lines: {
        if (menu.failed !== "")
            return [menu.failed];
        const r = menu.asked ?? menu.here;
        if (!r)
            return [];
        const why = (r.why ?? "") !== "" ? [r.why] : [];
        return why.concat(r.costs ?? []);
    }

    function show(newRows, newToken) {
        const wasUp = menu.visible;
        menu.rows = newRows;
        menu.token = newToken ?? "";
        menu.index = 0;
        menu.asking = "";
        menu.running = false;
        menu.failed = "";
        menu.visible = true;
        if (wasUp && menu.token !== "") {
            // Already up, so visible did not change and onVisibleChanged did
            // not fire - and the key that asked for this is waiting to hear
            // that something drew it. Unacknowledged it waits out its whole
            // window and takes the shell-is-dead path, which prints the menu to
            // a keybind's stdout (internal/zded, ackWait).
            menu.shown(menu.token);
        }
    }

    // On the window becoming visible rather than at the end of show(), so what
    // is acknowledged is a surface that exists.
    onVisibleChanged: {
        if (menu.visible && menu.token !== "")
            menu.shown(menu.token);
    }

    function hide() {
        menu.visible = false;
    }

    function step(by) {
        const n = menu.rows.length;
        if (n === 0 || menu.asking !== "")
            return;
        // A refusal belongs to the row it was about, so moving off that row
        // takes it away rather than leaving it over somebody else's.
        menu.failed = "";
        menu.index = (menu.index + by + n) % n;
    }

    // A digit goes to that row and stops there. It is a way of moving and not a
    // way of running: choose() runs a row outright when it has nothing to
    // confirm, so a digit wired to it made `1` lock the screen and `3` suspend
    // the machine from one press - on the one surface built around no single
    // keypress ending anything.
    function moveTo(i) {
        if (i < 0 || i >= menu.rows.length || menu.asking !== "")
            return;
        menu.failed = "";
        menu.index = i;
    }

    // Choosing a row: the ones that end something ask first, the rest go.
    function choose(i) {
        if (i < 0 || i >= menu.rows.length || menu.asking !== "")
            return;
        menu.index = i;
        menu.failed = "";
        const r = menu.rows[i];
        if (r.confirm === true) {
            menu.asking = r.name;
            return;
        }
        menu.run(r.name);
    }

    // y, on a row that asked. Only ever the row that was asked about: the name
    // goes with the answer, so a list that changed underneath cannot turn a yes
    // to a suspend into a yes to a power off.
    function confirm() {
        if (menu.asking === "")
            return;
        menu.run(menu.asking);
    }

    // Anything that is not a yes, which is the way round this question has to
    // default. Back to the list rather than closing the surface: the person
    // meant to be here, they just did not mean that row.
    function backOut() {
        menu.asking = "";
    }

    function run(name) {
        // One at a time: a second Enter while the first answer is on its way
        // would ask logind for two of whatever it is.
        if (menu.running)
            return;
        menu.running = true;
        menu.chosen(name);
    }

    // What became of it, from the shell. Empty means it was taken, and the
    // surface has no more reason to be on the screen; anything else is zded's
    // own words about why nothing happened - a locker this machine does not
    // have, an inhibitor holding sleep, another person logged in - which is the
    // half a keypress cannot say.
    function ran(error) {
        menu.running = false;
        if (error === "") {
            menu.hide();
            return;
        }
        menu.asking = "";
        menu.failed = error;
    }

    visible: false
    color: "transparent"

    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.namespace: "zde-power"
    WlrLayershell.keyboardFocus: menu.visible ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None

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
            onClicked: menu.dismissed()
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
        width: 560
        // The rows, what is under them, and the hint line. Left out of the sum,
        // any of them draws over the last row.
        height: Math.min(list.implicitHeight + detail.implicitHeight + 58, menu.height - 80)
        color: "#11121a"
        border.color: "#2a2c37"
        border.width: 1
        radius: 6
        clip: true

        // focus lives here, and the surface only holds the keyboard while it is
        // visible, so nothing is listening for keys when the menu is not up.
        focus: true
        Keys.onPressed: event => {
            if (menu.asking !== "") {
                // A modifier on its own is not an answer. Without this, holding
                // Shift to reach a capital Y is a press of Shift that backs out
                // of the question before the Y arrives, and the Y then lands on
                // a list with nothing waiting.
                if (event.key === Qt.Key_Shift || event.key === Qt.Key_Control || event.key === Qt.Key_Alt || event.key === Qt.Key_Meta || event.key === Qt.Key_CapsLock)
                    return;
                // One key says yes and every other key says no, which is the
                // way round a question about a machine going off has to
                // default. Enter is not the one, on purpose: it is the key that
                // got here.
                //
                // Two keys, then, and space is the second for a reason. Matched
                // on event.key rather than on the letter, but that is only half
                // an answer: Qt goes looking through the other keyboard layouts
                // for a Latin keysym only while Control is down
                // (QXkbCommon::keysymToQtKey), so with a Cyrillic layout
                // active this key is Н and its text is "н", and a surface with
                // no second way could not be said yes to at all. A host's
                // layout is its own (local.kdl) and this session sets none, so
                // that is a machine somebody has. Space has no layout anywhere
                // - xkb gives the space bar XK_space in every one of them - and
                // it is not a key anybody arrives here on, since what got here
                // was Enter or a click.
                if (event.key === Qt.Key_Y || event.key === Qt.Key_Space)
                    menu.confirm();
                else
                    menu.backOut();
                event.accepted = true;
                return;
            }
            switch (event.key) {
            case Qt.Key_Escape:
                menu.dismissed();
                break;
            case Qt.Key_Return:
            case Qt.Key_Enter:
                menu.choose(menu.index);
                break;
            case Qt.Key_Down:
                menu.step(1);
                break;
            case Qt.Key_Up:
                menu.step(-1);
                break;
            default:
                // j and k for a hand on home row, and a digit for the row in
                // that position - the same three ways the picker is driven,
                // because this is the same kind of list. All three move; none
                // of them runs anything, which is Enter's job alone.
                //
                // By key and not by text, for the reason the y above is: with a
                // non-Latin layout active the letters these keys produce are
                // not j and k, and a list drivable only from the arrow keys is
                // half a list.
                if (event.key === Qt.Key_J)
                    menu.step(1);
                else if (event.key === Qt.Key_K)
                    menu.step(-1);
                else if (event.key >= Qt.Key_1 && event.key <= Qt.Key_9)
                    menu.moveTo(event.key - Qt.Key_1);
                else {
                    return;
                }
            }
            event.accepted = true;
        }

        Column {
            id: list

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.margins: 12
            spacing: 2

            Repeater {
                model: menu.rows

                Rectangle {
                    id: row

                    required property var modelData
                    required property int index

                    width: list.width
                    height: 26
                    radius: 4
                    color: row.index === menu.index ? "#2a2c37" : "transparent"

                    // Clickable, because a list you can see is a list somebody
                    // will click. It goes through choose() like the keyboard
                    // does, so a mouse cannot get past the second question.
                    MouseArea {
                        anchors.fill: parent
                        onClicked: menu.choose(row.index)
                    }

                    Text {
                        id: label

                        anchors.left: parent.left
                        anchors.leftMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        width: 160
                        text: (row.index + 1) + "  " + row.modelData.label
                        elide: Text.ElideRight
                        // A row that says why it will not work is dimmed rather
                        // than hidden, the way the palette dims one nobody has
                        // written: "there is no logind here" is the answer
                        // somebody came to this surface for. Dimmed and not
                        // barred, though, which is the difference from the
                        // palette - pressing it produces zded's refusal on the
                        // line below, and that is louder than a row that
                        // ignores the key.
                        color: (row.modelData.why ?? "") === "" ? "#c9ccd4" : "#6a6f7a"
                        font.pixelSize: 13
                        font.family: "monospace"
                    }

                    Text {
                        anchors.left: label.right
                        anchors.leftMargin: 10
                        anchors.right: asks.left
                        anchors.rightMargin: 10
                        anchors.verticalCenter: parent.verticalCenter
                        text: row.modelData.desc
                        elide: Text.ElideRight
                        color: "#7a7f8a"
                        font.pixelSize: 12
                        font.family: "monospace"
                    }

                    // The mark the queue uses for the thing worth stopping at.
                    // It says which rows ask a second question, which is a
                    // property of the row and not of the moment - so it is on
                    // the row rather than only in the question.
                    Text {
                        id: asks

                        anchors.right: parent.right
                        anchors.rightMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        text: row.modelData.confirm === true ? "!" : ""
                        color: "#e5a23d"
                        font.pixelSize: 12
                        font.family: "monospace"
                    }
                }
            }
        }

        // What the highlighted row costs, or what is about to be lost, or what
        // zded said when it would not do it. One place for the three, because
        // each is more useful than the one under it exactly when it is there.
        Column {
            id: detail

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: list.bottom
            anchors.margins: 12
            spacing: 2

            Repeater {
                model: menu.lines

                Text {
                    id: line

                    required property var modelData

                    width: detail.width
                    text: line.modelData
                    wrapMode: Text.WordWrap
                    color: {
                        if (menu.failed !== "")
                            return "#e5484d";
                        return menu.asking !== "" ? "#e5a23d" : "#7a7f8a";
                    }
                    font.pixelSize: 11
                    font.family: "monospace"
                }
            }
        }

        // What else this surface does, said on the surface: a key nobody can
        // see is a key nobody uses, and the y is the whole of the second
        // question.
        Text {
            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.margins: 12
            elide: Text.ElideRight
            text: {
                if (menu.asking !== "") {
                    const r = menu.asked;
                    return "y or space " + ((r && r.label) ? r.label : menu.asking) + "    any other key backs out";
                }
                return "1-5 j k move    enter choose    esc close";
            }
            color: menu.asking !== "" ? "#e5a23d" : "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
        }
    }
}
