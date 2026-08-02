// The connections surface (docs/model.md, section 6: system.connections).
// Mod+Shift+c asks zded what the link is and what is in range; zded tells
// whoever is listening, and this appears with those rows on it.
//
// Built on Picker.qml's pattern - a layer surface that takes the keyboard only
// while it is visible, gives it back the moment it closes, and knows nothing
// about sockets - but not on the Picker itself: a row here is a network with a
// signal, a lock and a saved profile, and choosing one sometimes has to stop
// and ask for a password. That is a second question, and the picker's whole
// shape is one question.
//
// The password never leaves this file except down the socket. It is not
// logged, not put in a title, not kept after it is sent, and not shown while it
// is typed. What this cannot do is scrub it from the JavaScript heap - QML
// strings are the engine's - so what it does instead is not keep one.
//
// A note on the name: this is the connections surface and it is not called
// Connections.qml, because a file by that name shadows QtQuick's own
// Connections element for every other file in this directory - a local type
// wins over an imported one. Nothing in shell/ uses that element today, so the
// cost of the clash would be paid by whoever wrote the first one, looking for
// the reason somewhere other than here. Links is what it holds anyway: wifi,
// wired, and bluetooth once that section folds in.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// PanelWindow is an interface Quickshell fills in per platform; see nix/shell.nix.
PanelWindow {
    id: connections

    // A row is { ssid, signal, secure, saved, active }, exactly as zded sends
    // it (internal/link, Network). The surface adds nothing to it.
    property var rows: []
    // The link as it stands: { kind, ssid, signal, wifi }. What you are on is
    // the one thing about a network list you cannot read off the list.
    property var link: ({})
    property int index: 0
    property string token: ""
    // What zded said about the last attempt, kept on screen until the next one.
    // A join that failed has to say why (internal/link, refusal); a widget that
    // closed on failure would be the silence this exists to end.
    property string said: ""
    // True while a password is being typed. Only ever true while this surface
    // is up: hiding it puts this back to false and empties the field.
    property bool asking: false

    // join(ssid, secret) is the whole output of this surface, with drop() for
    // the one that needs no argument. The shell sends them; nothing here knows
    // what a socket is.
    signal join(string ssid, string secret)
    signal dropped
    signal dismissed
    signal shown(string token)

    function show(newRows, newLink, token) {
        const wasUp = connections.visible;
        connections.rows = newRows;
        connections.link = newLink ?? ({});
        connections.token = token ?? "";
        connections.said = "";
        connections.stopAsking();
        // Start on the network you are on, so the first thing highlighted is
        // the thing you would recognise. Nothing active starts at the top,
        // which is the strongest one and the one worth joining.
        connections.index = Math.max(0, newRows.findIndex(r => r.active === true));
        connections.visible = true;
        if (wasUp && connections.token !== "") {
            // Already up, so visible did not change and onVisibleChanged did not
            // fire - and the key is waiting to hear that something drew it.
            // Left unacknowledged it waits out its window and takes the
            // shell-is-dead path, which prints every SSID in range to a
            // keybind's stdout (internal/zded, ackWait).
            connections.shown(connections.token);
        }
    }

    // On the window becoming visible rather than at the end of show(), so what
    // is acknowledged is a surface that exists.
    onVisibleChanged: {
        if (connections.visible && connections.token !== "")
            connections.shown(connections.token);
        if (!connections.visible)
            connections.stopAsking();
    }

    function hide() {
        connections.visible = false;
    }

    // stopAsking is also how the secret goes away: one place, called from
    // everywhere that leaves the prompt, so no path out of it can forget.
    function stopAsking() {
        connections.asking = false;
        secret.text = "";
    }

    function step(by) {
        const n = connections.rows.length;
        if (n === 0)
            return;
        connections.index = (connections.index + by + n) % n;
    }

    // choose is Enter on a row: join it, or stop and ask for a password first.
    //
    // Asked for only when it is needed - a secured network NetworkManager has
    // no profile for. A prompt in front of an open network is a lie about what
    // is protecting it, and a prompt in front of a saved one is a question the
    // machine already knows the answer to.
    function choose(i) {
        if (i < 0 || i >= connections.rows.length)
            return;
        connections.index = i;
        const row = connections.rows[i];
        if (row.secure === true && row.saved !== true) {
            connections.said = "";
            connections.asking = true;
            return;
        }
        connections.said = "joining " + row.ssid;
        connections.join(row.ssid, "");
    }

    // send is Enter in the password field. The secret goes out and the field is
    // emptied in the same breath.
    function send() {
        if (connections.rows.length === 0)
            return;
        const row = connections.rows[connections.index];
        const typed = secret.text;
        connections.stopAsking();
        connections.said = "joining " + row.ssid;
        connections.join(row.ssid, typed);
    }

    visible: false
    color: "transparent"

    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.namespace: "zde-connections"
    WlrLayershell.keyboardFocus: connections.visible ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None

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
            onClicked: connections.dismissed()
        }
    }

    Rectangle {
        id: panel

        // Presses inside the panel stop here, or a click on the row you want
        // falls through to the dim layer and dismisses instead.
        MouseArea {
            anchors.fill: parent
        }

        anchors.centerIn: parent
        width: 520
        // The rows, their margins, the two lines above them and the hint under
        // them. Left out of the sum, the hint draws over the last row.
        height: Math.min(list.implicitHeight + 92, connections.height - 80)
        color: "#11121a"
        border.color: "#2a2c37"
        border.width: 1
        radius: 6
        clip: true

        // focus lives here while the list has it, and moves to the field while
        // a password is being typed - so exactly one of the two is listening.
        focus: !connections.asking
        Keys.onPressed: event => {
            switch (event.key) {
            case Qt.Key_Escape:
                connections.dismissed();
                break;
            case Qt.Key_Return:
            case Qt.Key_Enter:
                connections.choose(connections.index);
                break;
            case Qt.Key_Down:
                connections.step(1);
                break;
            case Qt.Key_Up:
                connections.step(-1);
                break;
            default:
                if (event.text === "j")
                    connections.step(1);
                else if (event.text === "k")
                    connections.step(-1);
                else if (event.text >= "1" && event.text <= "9")
                    connections.choose(parseInt(event.text, 10) - 1);
                else if (event.text === "d") {
                    connections.said = "dropping the link";
                    connections.dropped();
                } else {
                    return;
                }
            }
            event.accepted = true;
        }

        // What you are on, said rather than left to be worked out from the
        // rows: a wired machine has no row at all, and a machine with no
        // NetworkManager has no rows and is not offline.
        Text {
            id: here

            anchors.top: parent.top
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.margins: 12
            text: {
                const l = connections.link ?? {};
                if (l.kind === "wifi")
                    return l.ssid ? "on " + l.ssid + "  " + (l.signal ?? 0) + "%" : "on wifi";
                if (l.kind === "wired")
                    return "on the cable";
                if (l.kind === "absent")
                    return "no NetworkManager on this machine";
                return "not connected";
            }
            elide: Text.ElideRight
            color: "#c9ccd4"
            font.pixelSize: 13
            font.family: "monospace"
        }

        // What zded said about the last attempt, or the password prompt in the
        // same place - one line that is about what just happened either way.
        Text {
            id: prompt

            anchors.top: here.bottom
            anchors.topMargin: 6
            anchors.left: parent.left
            anchors.leftMargin: 12
            // Bounded, because this line is whatever NetworkManager said and
            // the panel is 520 wide.
            anchors.right: parent.right
            anchors.rightMargin: 12
            visible: !connections.asking
            text: connections.said
            elide: Text.ElideRight
            color: "#7a7f8a"
            font.pixelSize: 12
            font.family: "monospace"
        }

        Text {
            id: passwordLabel

            anchors.top: here.bottom
            anchors.topMargin: 6
            anchors.left: parent.left
            anchors.leftMargin: 12
            visible: connections.asking
            text: "password:"
            color: "#c9ccd4"
            font.pixelSize: 12
            font.family: "monospace"
        }

        // The password. Never echoed, never kept: emptied on send, on Escape,
        // and whenever this surface is hidden (stopAsking).
        TextInput {
            id: secret

            anchors.left: passwordLabel.right
            anchors.leftMargin: 8
            anchors.right: parent.right
            anchors.rightMargin: 12
            anchors.verticalCenter: passwordLabel.verticalCenter
            visible: connections.asking
            focus: connections.asking
            echoMode: TextInput.Password
            color: "#c9ccd4"
            font.pixelSize: 12
            font.family: "monospace"
            onAccepted: connections.send()
            Keys.onEscapePressed: connections.stopAsking()
        }

        // What else this surface does, said on the surface.
        Text {
            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.margins: 12
            text: "1-9 join    j k move    d disconnect    esc close"
            color: "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
        }

        // Nothing to list: a machine with no radio, no NetworkManager, or a
        // room with nothing in it. Three different facts, and the one thing
        // they have in common is that an empty panel would explain none of them.
        Text {
            anchors.top: prompt.bottom
            anchors.topMargin: 10
            anchors.left: parent.left
            anchors.leftMargin: 12
            visible: connections.rows.length === 0
            text: {
                const l = connections.link ?? {};
                if (l.kind === "absent")
                    return "nothing to ask: layer 0 installs NetworkManager with zde.laptop.enable";
                if (l.wifi !== true)
                    return "no wifi radio on this machine";
                return "no wifi networks in range";
            }
            color: "#7a7f8a"
            font.pixelSize: 12
            font.family: "monospace"
        }

        Column {
            id: list

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: prompt.bottom
            anchors.topMargin: 10
            anchors.leftMargin: 12
            anchors.rightMargin: 12
            spacing: 2

            Repeater {
                model: connections.rows

                Rectangle {
                    id: row

                    required property var modelData
                    required property int index

                    width: list.width
                    height: 26
                    radius: 4
                    color: row.index === connections.index ? "#2a2c37" : "transparent"

                    MouseArea {
                        anchors.fill: parent
                        onClicked: connections.choose(row.index)
                    }

                    Text {
                        anchors.left: parent.left
                        anchors.leftMargin: 8
                        anchors.right: note.left
                        anchors.rightMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        text: (row.index < 9 ? (row.index + 1) + "  " : "   ") + row.modelData.ssid
                        // An ssid is 32 bytes somebody else chose, and a long
                        // one running past the panel reads as a broken panel.
                        elide: Text.ElideRight
                        color: "#c9ccd4"
                        font.pixelSize: 13
                        font.family: "monospace"
                    }

                    // The signal, and what joining it will cost: nothing at
                    // all for open and saved networks, a password for the rest.
                    Text {
                        id: note

                        anchors.right: parent.right
                        anchors.rightMargin: 8
                        anchors.verticalCenter: parent.verticalCenter
                        text: {
                            const n = row.modelData;
                            const where = n.active === true ? "  here" : (n.saved === true ? "  saved" : (n.secure === true ? "  password" : "  open"));
                            return (n.signal ?? 0) + "%" + where;
                        }
                        color: "#7a7f8a"
                        font.pixelSize: 12
                        font.family: "monospace"
                    }
                }
            }
        }
    }
}
