// The notification center (docs/roadmap.md, 0.1: shell MVP). Mod+n asks zded
// for the history, zded tells whoever is listening, and this appears on the
// screen being looked at with what arrived on it.
//
// It is the other half of principle 3 (docs/vision.md): the modes stop things
// interrupting you, and this is where you find out what they stopped. So a row
// says what became of it - waiting, done, or silent because a mode kept it off
// the queue - and never leaves that to be inferred from its absence.
//
// It is also where a notification's actions are offered, all of them: the row
// you are on lists what its sender said can be done about it, one digit each,
// and Enter is the default action where there is one. That is what makes zded
// claiming the spec's "actions" capability true rather than a promise (see
// internal/attn, GetCapabilities).
//
// The popup (AttnPopup.qml) offers the same buttons the moment something
// arrives, which is not a reason for this to stop: a popup is subject to the
// mode and to five seconds, and this is where every arrival is, on your own
// time. The two share a vocabulary on purpose - j/k, a digit per action, Enter
// for the default, d to dismiss - because they are two views of one thing and
// must not want two sets of fingers.
//
// The same shape as Picker.qml, and deliberately not the same surface. A picker
// hands back the key of a row and knows nothing else; this one has several
// verbs on a row and something to say back when one of them was not possible.
// Sharing one surface would have meant a mode flag inside it, which is the
// point where one surface becomes two badly.
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
    // body, urgent, at, queued, dismissed, action - and for the rows that came
    // back from the snapshot rather than arriving here, restored and
    // bodyClipped (internal/attn, snapshot.go). Those two are what stops this
    // surface drawing yesterday's history as though it were this session's.
    property var rows: []
    property int index: 0

    // The geometry the panel is built out of. Named because two things count
    // with them: the rows, and the panel working out how tall it should be
    // without asking the list - which takes its own height from the panel, so
    // asking would be a binding loop.
    readonly property int rowHeight: 26
    readonly property int rowGap: 2
    // The three lines under the list - the body, the actions and the hint -
    // with the margins around them. Leave one out of the sum and the last row
    // draws underneath it.
    readonly property int footer: 96

    // What this surface has to say back, shown under the rows. The one thing a
    // list cannot do on its own is explain why a key did nothing: a row whose
    // sender declared no actions, one whose sender has since exited, and a
    // digit past the end of the actions it did declare all look identical until
    // something says which it was.
    property string note: ""

    // What the event asked for, sent back once this is actually up. The asker
    // is waiting on it: without it, "shown" means the socket took the bytes,
    // which a frozen shell also does.
    property string token: ""

    // invoke(id, key) presses one of a notification's actions; drop(id) takes it
    // off. Both go to zded over the socket - this surface knows nothing about
    // sockets, and decides nothing (docs/vision.md, section 2).
    signal invoke(string id, string key)
    signal drop(string id)
    signal dismissed
    signal shown(string token)

    function show(newRows, newToken) {
        const wasUp = center.visible;
        center.rows = newRows ?? [];
        center.token = newToken ?? "";
        center.note = "";
        // The newest, which is what somebody opening this is looking for.
        center.index = 0;
        center.visible = true;
        if (wasUp && center.token !== "") {
            // Already up, so visible did not change and onVisibleChanged did not
            // fire - and Mod+n is waiting to hear that something drew it. Left
            // unacknowledged it waits out its window and takes the shell-is-dead
            // path, which prints the whole history to a keybind's stdout
            // (internal/zded, ackWait).
            center.shown(center.token);
        }
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

    // The actions of the row you are on, in the order its sender declared them.
    // Their position is what presses them: the first is 1, and there are never
    // more than nine here, because nine is what a digit can reach and zded
    // keeps no more than can be offered (internal/attn, actionsMax).
    function actionsOf(r) {
        return (r && r.actions) ? r.actions : [];
    }

    // Enter: the default action, which is the sender's own answer to "what does
    // choosing this mean". A row that declared none is refused here rather than
    // by zded, so the answer arrives with the keypress instead of after a round
    // trip - and zded refuses it too, because the shell is not where a rule
    // lives.
    //
    // "default" is the spec's own name for it and not zde's, which is why it is
    // written here as a string: the freedesktop notification spec fixes it, so
    // this cannot drift out of step with the Go side.
    function act() {
        const list = center.actionsOf(center.rowAt(center.index));
        for (const a of list) {
            if (a.key === "default") {
                center.fire(a);
                return;
            }
        }
        const r = center.rowAt(center.index);
        if (!r)
            return;
        center.note = list.length > 0 ? "no default action: press its number instead" : center.nothingToPress(r);
    }

    // Why a row has nothing to press. Two different facts, and telling them
    // apart is the point: a sender that offered no buttons, and a row from
    // before the restart, whose buttons were not kept and whose app is not on
    // the bus any more (internal/attn, Snapshot). Saying the first about the
    // second would blame an app for what a restart did.
    function nothingToPress(r) {
        if (r.restored)
            return "this arrived before the session restarted: there is nothing left to press it on";
        return "nothing to invoke: " + (r.from ?? "it") + " sent a notification, not a button";
    }

    // A digit: the action in that position. Out of range is said rather than
    // ignored, because a key that does nothing on a surface offering numbered
    // things reads as the surface being broken.
    function press(i) {
        const r = center.rowAt(center.index);
        const list = center.actionsOf(r);
        if (i < 0 || i >= list.length) {
            center.note = list.length === 0 ? (r ? center.nothingToPress(r) : "nothing here to press") : "there is no action " + (i + 1) + " on this one";
            return;
        }
        center.fire(list[i]);
    }

    // What both of them do. Optimistic by a millisecond: zded answers only when
    // it refuses, and a refusal replaces this the moment it arrives.
    function fire(a) {
        const r = center.rowAt(center.index);
        if (!r)
            return;
        center.note = "sent " + a.label + " to " + (r.from ?? "the app");
        center.invoke(String(r.id), a.key);
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
    //
    // A restored row says so as well, on the row itself, because nothing else
    // on it would: the time column is a clock with no date on it (see when),
    // and after a reboot a row from Tuesday reads as one from ten minutes ago.
    function became(r) {
        const what = r.dismissed ? "done" : (r.queued ? "waiting" : "silent");
        return r.restored ? what + " · earlier" : what;
    }

    // The clock time it arrived. A day and a time would be more precise and
    // less readable, and the history is bounded at a day or two of use.
    //
    // Cut out of the timestamp rather than parsed. Go writes RFC 3339 with up
    // to nine fractional digits and ECMA-262 specifies three, so `new Date` on
    // it is a bet on how forgiving one engine's parser happens to be - and the
    // losing side of that bet is a column of "--:--" that nobody would think to
    // blame on a date format. RFC 3339 fixes the offsets: hours at 11, minutes
    // at 14, in the sender's own local time, which is what a clock on a bar
    // means anyway.
    function when(r) {
        const s = (r && r.at) ? String(r.at) : "";
        return s.length >= 16 && s.charAt(13) === ":" ? s.substring(11, 16) : "--:--";
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
        // As tall as the rows need, up to what the screen allows. Counted from
        // the row count rather than read off the list, because the list's own
        // height comes from this one: asking it how tall its contents are would
        // be a binding loop, and the arithmetic is two numbers.
        height: Math.min(center.rows.length * (center.rowHeight + center.rowGap) + center.footer, center.height - 80)
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
                // A digit presses that action on the row you are on. In the
                // picker a digit chooses a row, and here it cannot: a row has
                // several things you might do to it, and the numbers are worth
                // more spent on those than on a list you can walk with j and k.
                if (event.text === "j")
                    center.step(1);
                else if (event.text === "k")
                    center.step(-1);
                else if (event.text === "d")
                    center.dismiss();
                else if (event.text >= "1" && event.text <= "9")
                    center.press(parseInt(event.text, 10) - 1);
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
        // cannot show: a notification lands in history with what it said and
        // not with a headline (docs/vision.md, principle 3), and this is where
        // the rest of it is. On one line here, which is the surface's doing and
        // not the record's - what was kept has its line breaks, and a paragraph
        // drawn into a one-line slot would push the hint off the panel.
        // Two elements and not one string, because the marker beside the body
        // is the half that has to survive. A body is up to 400 characters and
        // this slot shows eighty-odd of them before it elides, so a marker
        // appended to the text began at character 401 of something nobody could
        // read past 85 - which is a warning that is never on the screen. Here
        // the marker holds the right-hand end and the body elides into it.
        Item {
            id: bodyLine

            anchors.bottom: actions.top
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.margins: 12
            anchors.bottomMargin: 6
            height: body.implicitHeight

            // A body the snapshot cut short says so. Without it the front of a
            // message and the whole of one look identical, and a row that ends
            // mid-sentence reads as the app having sent that - which is the one
            // thing zde promises it does not do (docs/vision.md, principle 3).
            Text {
                id: bodyCut

                anchors.right: parent.right
                anchors.verticalCenter: parent.verticalCenter
                visible: {
                    const r = center.rowAt(center.index);
                    return center.note === "" && !!(r && r.bodyClipped);
                }
                text: "  · only the front of this was kept"
                color: "#7a7f8a"
                font.pixelSize: 12
                font.family: "monospace"
            }

            Text {
                id: body

                anchors.left: parent.left
                anchors.right: bodyCut.visible ? bodyCut.left : parent.right
                anchors.verticalCenter: parent.verticalCenter
                text: {
                    const r = center.rowAt(center.index);
                    if (center.note !== "")
                        return center.note;
                    return r && r.body ? r.body.replace(/\s+/g, " ") : "";
                }
                color: center.note !== "" ? "#e5a23d" : "#9aa0ac"
                elide: Text.ElideRight
                font.pixelSize: 12
                font.family: "monospace"
            }
        }

        // What can be done to the row you are on, with the key that does it.
        // Said on the surface rather than left to be discovered: an action
        // nobody can see is an action nobody presses, and this is the whole
        // difference between offering an app's buttons and merely holding them.
        Text {
            id: actions

            anchors.bottom: hint.top
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.margins: 12
            anchors.bottomMargin: 6
            visible: center.rows.length > 0
            text: {
                const r = center.rowAt(center.index);
                const list = center.actionsOf(r);
                if (list.length === 0)
                    return r && r.restored ? "from before the restart: nothing on it can be pressed" : "no actions on this one";
                let parts = [];
                for (let i = 0; i < list.length; i++)
                    parts.push((i + 1) + " " + list[i].label + (list[i].key === "default" ? " (enter)" : ""));
                let line = parts.join("    ");
                // An app may declare more than this surface can offer. Saying
                // how many beats a list that quietly stops, which would read as
                // the app having sent fewer than it did.
                const more = (r && r.moreActions) ? r.moreActions : 0;
                if (more > 0)
                    line += "    +" + more + " this surface cannot reach";
                return line;
            }
            elide: Text.ElideRight
            color: "#c9ccd4"
            font.pixelSize: 12
            font.family: "monospace"
        }

        Text {
            id: hint

            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.margins: 12
            text: "j k move    enter default    1-9 action    d dismiss    esc close"
            color: "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
        }

        // Where you are in what arrived. The view scrolls now, so without this
        // a full history and a short one look identical, and there is no way to
        // tell that j has more to walk through.
        Text {
            anchors.bottom: parent.bottom
            anchors.right: parent.right
            anchors.margins: 12
            visible: center.rows.length > 0
            text: (center.index + 1) + " of " + center.rows.length
            color: "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
        }

        // A view and not a column, because the history holds up to 420 records -
        // thirty from each of twelve senders, the nameless ring and zde's own
        // (internal/attn, PerSenderMax) - and a screen holds 31 of them. A
        // column inside a clipped panel drew the ones that fit and hid the rest,
        // and j walked the highlight into the hidden part - where d dismissed a
        // notification nobody could see, and told the app that sent it. So the
        // row you are on is always on the screen: the view scrolls to it, rather
        // than the list ending.
        ListView {
            id: list

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.bottom: bodyLine.top
            anchors.margins: 12
            anchors.bottomMargin: 6
            spacing: center.rowGap
            clip: true
            // Deliberately without focus: the panel holds the keyboard, and a
            // view that took it would answer the arrow keys itself and move a
            // highlight of its own that nothing else here reads.
            model: center.rows
            currentIndex: center.index
            // Contain rather than Center: walking down a long list should not
            // redraw the whole surface on every press, and the row only has to
            // be somewhere on the screen to be a row you can act on.
            onCurrentIndexChanged: list.positionViewAtIndex(list.currentIndex, ListView.Contain)
            // And when the rows themselves change, which is every time Mod+n
            // reopens this with a fresh history: a view left where the last
            // list was scrolled to would open somewhere down the middle of a
            // list whose top row is the one you came to read.
            onModelChanged: list.positionViewAtIndex(center.index, ListView.Contain)

            delegate: Rectangle {
                id: row

                required property var modelData
                required property int index

                width: list.width
                height: center.rowHeight
                radius: 4
                color: row.index === center.index ? "#2a2c37" : "transparent"

                MouseArea {
                    anchors.fill: parent
                    onClicked: center.index = row.index
                }

                // The row: when it came, whether it said it was urgent, who
                // sent it, and what it said. No leading number any more - the
                // digits press this row's actions now, and a number in front of
                // a row somebody cannot press with it is a number that lies.
                Text {
                    anchors.left: parent.left
                    anchors.leftMargin: 8
                    anchors.right: mark.left
                    anchors.rightMargin: 8
                    anchors.verticalCenter: parent.verticalCenter
                    text: center.when(row.modelData) + "  " + (row.modelData.urgent ? "! " : "  ") + (row.modelData.from ? row.modelData.from : "-") + "  " + row.modelData.text
                    elide: Text.ElideRight
                    // Dimmed once it is done, so the list reads as what is left
                    // rather than as everything that ever happened.
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
