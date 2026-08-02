// The ask windows (docs/roadmap.md, 0.1: ask MVP). Mod+a is one question and
// one answer; Mod+Shift+a is the same window with room to keep asking while it
// is open.
//
// One surface for both, the way one picker draws desks and windows: they differ
// in whether the last exchange is replaced or added to, and in nothing else. A
// second copy of the keyboard handling and the focus dance would be a second
// place for either to stop working.
//
// The same bargain Picker.qml makes with the shell: this draws, reads keys and
// hands back a question with the tier to ask it on. It knows nothing about
// sockets (docs/vision.md, section 2 - the shell is a thin adapter with zero
// logic inside), which is also why a tier here is a name and never a command.
//
// Nothing is kept. The transcript is a property of a window, closing clears it,
// and no part of this writes anything anywhere - which is what "no history by
// default" means (vision.md, section 2). There is no history file to turn off
// because nothing makes one.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// PanelWindow is an interface Quickshell fills in per platform; see nix/shell.nix.
PanelWindow {
    id: ask

    // Whether this is the panel. The popup answers one question - a second one
    // replaces it - and the panel keeps what has been asked on screen.
    property bool panel: false

    // What has been asked and answered, as one block of text: questions marked
    // with "> ", answers as they came. One string and not a list of exchanges,
    // because nothing here does anything per exchange - it is read, and then it
    // is forgotten.
    property string log: ""
    // The answer being streamed right now, kept apart so that a piece arriving
    // costs one string append and not a rebuild of the transcript.
    property string live: ""
    // Why the last ask ended badly, said loudly and on its own line. An empty
    // window that never answers is the failure this component is arranged
    // against, so a tier nobody configured has to be visible rather than quiet.
    property string failure: ""
    // Whether an answer is on its way. A second question while one runs would
    // interleave two answers on one connection, so the keys are shut while it
    // is true. Cleared by the answer ending and by nothing else - closing the
    // window does not stop a tier that is already thinking.
    property bool running: false
    // Whether what is arriving is the answer to the question on screen.
    // Reopening the window abandons the last question, and its answer can still
    // be on its way, so what comes back after that belongs to nobody: dropped,
    // rather than appearing under a question it does not answer.
    property bool awaiting: false
    // The tier the question went to, on screen: local and provider are a
    // different promise about where the question went, and a window that does
    // not say which is a window you cannot trust with the private one.
    property string tier: "provider"
    // The last question, so escalating is a keypress rather than retyping it.
    property string lastAsked: ""

    // What the event asked for, sent back once this is actually up. The asker
    // is waiting on it: without it, "shown" means the socket took the bytes,
    // which a frozen shell also does.
    property string token: ""

    // asked(tier, question) is the whole output of this surface.
    signal asked(string tier, string question)
    signal dismissed
    signal shown(string token)

    function show(isPanel, newToken) {
        const wasUp = ask.visible;
        // The key pressed at a window that is already up is not a new window
        // and must not wipe what is on it: Mod+Shift+a with the panel open is
        // somebody reaching for the panel they are already in.
        const fresh = !wasUp || ask.panel !== isPanel;
        ask.panel = isPanel;
        ask.token = newToken ?? "";
        if (fresh) {
            ask.forget();
            field.text = "";
        }
        ask.visible = true;
        // The window has the keyboard while it is visible, and the field is
        // what should have it: a question window whose first keystroke goes
        // nowhere is one you type the first word of twice.
        field.forceActiveFocus();
        if (wasUp && ask.token !== "") {
            // Already up, so onVisibleChanged did not fire - and the key that
            // asked for this is waiting to hear that something drew it, or it
            // reports that nothing did.
            ask.shown(ask.token);
        }
    }

    function hide() {
        ask.visible = false;
        // Closing is forgetting. Nothing was written down, so dropping what is
        // on the screen is the whole of it - and it means the next Mod+a is not
        // the last question still sitting there.
        ask.forget();
    }

    // What is on the screen, and nothing else: running is what an answer ending
    // clears, because a tier that is already thinking goes on thinking whether
    // or not anybody is looking at the window.
    function forget() {
        ask.log = "";
        ask.live = "";
        ask.failure = "";
        ask.lastAsked = "";
        ask.awaiting = false;
    }

    // allowRepeat is what makes the tier keys re-ask: escalating is asking the
    // same question of something better, and having to retype it would be the
    // whole cost of the key. Enter does not repeat - an empty field and Enter
    // is a keypress that meant nothing, and asking again on the same tier would
    // spend somebody's tokens on it.
    function submit(onTier, allowRepeat) {
        // Not trimmed here. zded trims a question and refuses an empty one, so
        // a field of spaces ends in the same red line as anything else that
        // could not be asked, and the shell keeps one less rule of its own.
        let q = field.text;
        if (q === "" && allowRepeat)
            q = ask.lastAsked;
        if (q === "" || ask.running)
            return;
        if (!ask.panel)
            ask.log = "";
        ask.lastAsked = q;
        ask.tier = onTier;
        ask.failure = "";
        ask.log += "> " + q + "\n";
        ask.live = "";
        ask.running = true;
        ask.awaiting = true;
        field.text = "";
        ask.asked(onTier, q);
    }

    // A piece of the answer, as it arrives.
    function chunk(text) {
        if (!ask.awaiting)
            return;
        ask.live += text;
    }

    function finished(why) {
        ask.running = false;
        if (!ask.awaiting)
            return;
        ask.awaiting = false;
        if (ask.live !== "") {
            ask.log += ask.live + "\n";
            ask.live = "";
        }
        ask.failure = why ?? "";
    }

    // On the window becoming visible rather than at the end of show(), so what
    // is acknowledged is a window that exists.
    onVisibleChanged: {
        if (ask.visible && ask.token !== "")
            ask.shown(ask.token);
    }

    visible: false
    color: "transparent"

    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.namespace: "zde-ask"
    // Only while it is up, like the picker: a layer surface holding the
    // keyboard reads to niri as nothing focused at all, so a nav key pressed
    // afterwards would spend itself putting focus back on a window.
    WlrLayershell.keyboardFocus: ask.visible ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None

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
            onClicked: ask.dismissed()
        }
    }

    Rectangle {
        id: frame

        // Presses inside stop here, or a click on the text falls through to the
        // dim layer behind and closes the window somebody was reading.
        MouseArea {
            anchors.fill: parent
        }

        anchors.centerIn: parent
        width: Math.min(760, ask.width - 80)
        // As tall as there is to show, up to the screen. An answer longer than
        // that scrolls inside the view below rather than growing past the edge.
        height: Math.min(Math.max(120, transcript.implicitHeight + 96), ask.height - 100)
        color: "#11121a"
        border.color: "#2a2c37"
        border.width: 1
        radius: 6
        clip: true

        Text {
            id: prompt

            anchors.left: parent.left
            anchors.top: parent.top
            anchors.margins: 12
            // Which tier this is going to, said before it goes: the local tier
            // is a promise about the network, and one that is not on screen is
            // one nobody can rely on.
            text: (ask.running ? "..." : ">") + " " + ask.tier
            color: ask.tier === "provider" ? "#7a7f8a" : "#c9ccd4"
            font.pixelSize: 12
            font.family: "monospace"
        }

        TextInput {
            id: field

            anchors.left: prompt.right
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.leftMargin: 10
            anchors.rightMargin: 12
            anchors.topMargin: 11
            focus: true
            color: "#c9ccd4"
            font.pixelSize: 13
            font.family: "monospace"
            // The clipboard, without a key of zde's own: this is a text field,
            // so ctrl+v is its own paste and the question carries whatever was
            // copied. A region and a window are the attachments this does not
            // have (docs/vision.md, section 2), and they need more than text.
            onAccepted: ask.submit("provider", false)

            // Before the field's own handling, which is what Keys does by
            // default - otherwise a chord the field has a use for would never
            // reach here.
            Keys.onPressed: event => {
                if (event.key === Qt.Key_Escape) {
                    ask.dismissed();
                } else if ((event.modifiers & Qt.ControlModifier) && event.key === Qt.Key_E) {
                    ask.submit("escalate", true);
                } else if ((event.modifiers & Qt.ControlModifier) && event.key === Qt.Key_L) {
                    ask.submit("local", true);
                } else {
                    return;
                }
                event.accepted = true;
            }
        }

        Flickable {
            id: view

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: field.bottom
            // Above the failure line when there is one, so a refusal does not
            // draw over the last thing that was answered.
            anchors.bottom: ask.failure === "" ? hint.top : failed.top
            anchors.margins: 12
            contentHeight: transcript.implicitHeight
            clip: true

            // Follow the answer down as it arrives. A window that has to be
            // scrolled by hand to see the words being written into it is one
            // that looks stuck.
            onContentHeightChanged: view.contentY = Math.max(0, view.contentHeight - view.height)

            Text {
                id: transcript

                width: view.width
                text: ask.log + ask.live
                wrapMode: Text.Wrap
                color: "#c9ccd4"
                font.pixelSize: 13
                font.family: "monospace"
            }
        }

        // Why it ended badly, if it did. Loud, because the alternative is a
        // window that came up, answered nothing, and looks exactly like one
        // waiting.
        Text {
            id: failed

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.bottom: hint.top
            anchors.margins: 12
            visible: ask.failure !== ""
            text: ask.failure
            wrapMode: Text.Wrap
            color: "#e5484d"
            font.pixelSize: 12
            font.family: "monospace"
        }

        // What else this window does, said on the window. The tier keys are the
        // whole point of having tiers, and a key nobody can see is a key nobody
        // presses.
        Text {
            id: hint

            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.margins: 12
            text: "enter ask    ctrl+e escalate    ctrl+l local    ctrl+v paste    esc close"
            color: "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
        }
    }
}
