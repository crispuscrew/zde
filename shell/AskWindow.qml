// The ask windows (docs/roadmap.md, 0.1: ask MVP). Mod+a is one question and
// one answer; Mod+Shift+a is the same window with room to keep asking while it
// is open, and what has been asked in it goes with the next question.
//
// One surface for both, the way one picker draws desks and windows: they differ
// in whether the last exchange is replaced or added to, and in whether the
// exchanges before it are carried. A second copy of the keyboard handling and
// the focus dance would be a second place for either to stop working.
//
// The popup carries nothing, deliberately. Mod+a is a question with no history
// in front of it - which is the whole of what it is for - and one that quietly
// remembered the last one would be the panel with a different key.
//
// The same bargain Picker.qml makes with the shell: this draws, reads keys and
// hands back a question with the tier to ask it on. It knows nothing about
// sockets (docs/vision.md, section 2 - the shell is a thin adapter with zero
// logic inside), which is also why a tier here is a name and never a command.
//
// One question does not come from the keyboard: `zde ask panel <question>`
// typed at a terminal opens the panel with it already asked. What that means
// for a conversation already on screen is the one decision this file makes
// about somebody else's words (see askFromOutside).
//
// Nothing is kept. The transcript is a property of a window, closing clears it,
// and no part of this writes anything anywhere - which is what "no history by
// default" means (vision.md, section 2). There is no history file to turn off
// because nothing makes one. The turns go back down the socket with each
// question rather than being held by the daemon, so the only copy of a
// conversation is the one on this screen.
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
    // The same exchanges again, as the tier is given them: question, answer,
    // question, answer, oldest first. Apart from the log because the log is
    // written to be read by a person and this is written to be read by a
    // program - and because who said which half has to survive the trip, which
    // "> " down the left margin of one string cannot promise (internal/zded,
    // ask.go). Flat and alternating, so the role of a turn is its position and
    // never a marker inside it.
    //
    // The panel's, and empty in the popup: a oneshot is one question with
    // nothing in front of it.
    property var turns: []
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

    // asked(tier, question, prior) is the whole output of this surface: what to
    // ask, where to ask it, and the conversation it belongs to.
    signal asked(string tier, string question, var prior)
    signal dismissed
    signal shown(string token)

    // question is set only when the panel was asked for from somewhere with no
    // window to type into: `zde ask panel <question>` at a terminal. Empty for
    // every key.
    function show(isPanel, newToken, question) {
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
        // After the acknowledgement, so what the asker hears is that a window
        // was drawn - which is all it asked and all this can promise. Whether
        // the question then went out is this window's to show.
        if ((question ?? "") !== "")
            ask.askFromOutside(question);
    }

    // A question that was not typed here: `zde ask panel <question>`.
    //
    // It starts a fresh conversation, always, and that is the decision in this
    // function rather than an accident of where it is called from. The turns a
    // panel carries are what a question is asked inside, and a question typed
    // at a terminal was not asked inside anything: joining it to what is on
    // screen would drop a shell script's question into the middle of somebody's
    // exchange, carry it into every turn after it, and hand whatever that
    // exchange contains to the tier along with it - on a provider tier, off the
    // machine. None of that is undoable except by ctrl+n, which ends the
    // conversation anyway.
    //
    // The cost is real and is not hidden: a panel with a conversation in it
    // loses it, which is the same forgetting ctrl+n and Escape already do. It
    // is visible in the two places that say what a question is being asked
    // with - an empty transcript, and no "carrying" beside the tier name.
    function askFromOutside(question) {
        if (ask.running) {
            // One answer at a time down one connection (internal/zded,
            // events.go - sink.asking), so this one cannot go now. Into the
            // field with a line saying why, rather than dropped: a question
            // that vanished silently is the failure this whole component is
            // arranged against, and Enter asks it once the last answer ends.
            // Nothing is forgotten in this branch either - the conversation on
            // screen is what the answer still arriving belongs to.
            //
            // It does replace anything half-typed in the field. That needs two
            // hands - a terminal and this window, while an answer is streaming
            // - and the alternative is a question that arrived and is nowhere.
            field.text = question;
            ask.failure = "still answering the last question: press enter to ask this one";
            return;
        }
        ask.forget();
        field.text = question;
        // The provider tier, which is what Enter in this window does. ctrl+l
        // and ctrl+e are still there for the same question, since it is on the
        // screen the moment this returns.
        ask.submit("provider", false);
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
    //
    // This is also what ctrl+n does, which is the whole of starting a fresh
    // conversation: the turns go, so the next question is asked with nothing in
    // front of it, and awaiting goes with them - an answer still on its way
    // belongs to a conversation that has ended and is dropped rather than
    // appearing at the top of a new one.
    function forget() {
        ask.log = "";
        ask.live = "";
        ask.failure = "";
        ask.lastAsked = "";
        ask.turns = [];
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
        // The turns as they stand, which is the conversation this question is
        // being asked inside: this one is not among them yet, and joins them
        // when there is an answer to pair it with.
        ask.asked(onTier, q, ask.turns);
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
            // And into the conversation, in the panel, now that the question
            // has something paired with it. Only with an answer: a question
            // nothing answered never reached anybody, so carrying it alone
            // would tell the tier that somebody spoke and it stayed silent,
            // which is not what happened. It stays on the screen with its
            // failure line, where it can be asked again.
            //
            // A new array rather than a push, because a property of this type
            // notifies on assignment and not on being mutated - and the count
            // on the prompt line is bound to it.
            if (ask.panel)
                ask.turns = ask.turns.concat([ask.lastAsked, ask.live]);
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
            //
            // And how many exchanges go with it, for the same reason and one
            // more: carrying them is what the next question costs, in money on
            // a provider tier and in seconds on a local one (docs/vision.md,
            // principle 4 - whatever a keypress depends on is visible). It is
            // also the only way to see that ctrl+n did anything.
            text: {
                const carried = ask.turns.length > 0 ? "  carrying " + (ask.turns.length / 2) : "";
                return (ask.running ? "..." : ">") + " " + ask.tier + carried;
            }
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
                } else if (ask.panel && (event.modifiers & Qt.ControlModifier) && event.key === Qt.Key_N) {
                    // A fresh conversation, without closing the window: the
                    // other way to get one is Escape and Mod+Shift+a, which is
                    // two keys and takes the window away from under somebody
                    // who only wanted to change the subject. Gated on the
                    // panel, since the popup has no conversation to end.
                    //
                    // Nothing confirms it, because nothing is lost that was not
                    // already going to be: the transcript was never written
                    // anywhere, and this is the same forgetting that closing
                    // the window does.
                    ask.forget();
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
        // presses - which goes double for ctrl+n, since the way out of a
        // conversation that has gone the wrong way has to be in front of
        // somebody at the moment it does.
        Text {
            id: hint

            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.margins: 12
            text: "enter ask    ctrl+e escalate    ctrl+l local" + (ask.panel ? "    ctrl+n new" : "") + "    ctrl+v paste    esc close"
            color: "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
        }
    }
}
