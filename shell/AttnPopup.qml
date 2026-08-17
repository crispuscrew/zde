// The notification popup (docs/roadmap.md, 0.1: a popup). zded broadcasts one
// of these the moment something arrives, and this puts it on the screen the
// person is looking at with the summary, the body and the sender's own buttons
// on it - then takes itself away.
//
// It is what makes zded's "actions" capability a claim about immediacy as well
// as about reach. The center offers every action a sender declares, and it does
// so behind Mod+n; an app that reads the capability as "there will be a button
// on the screen when I send" was, until this, reading it as more than it said
// (internal/attn, GetCapabilities).
//
// **It does not take the keyboard.** That is the whole shape of this file and
// the reason for every awkward thing in it. Every other surface here is a
// full-screen overlay with an exclusive grab, niri hands that grab to the oldest
// of them, and it took a fix to keep them to one at a time (shell.qml, present).
// A surface that grabbed on arrival would take the keyboard out of whatever you
// were typing into, several times an hour, at the choice of any app on the
// session bus - which is also a way for an app to steal a keystroke. So:
//
//   - it is a small surface in a corner, not a full-screen one, so it covers
//     nothing and swallows no clicks;
//   - keyboardFocus is None until somebody presses Mod+Ctrl+n, which is the one
//     deliberate act that hands it the keys (internal/zded, EventAttnReach);
//   - the buttons are clickable the whole time, so a mouse never needs the key;
//   - and once it has the keyboard it keeps it only while a finger is on it -
//     see the holding timer, which is what stops a toast from becoming the
//     surface that owns the machine.
//
// Bounded, because notifications arrive at machine speed: a build bot sends a
// hundred in a minute and a screen holds three. Two bounds, and they are
// different things. A second arrival from a sender already on the screen
// replaces that sender's card rather than stacking beside it, which is what
// keeps one download's hundred progress updates to one card. Across senders it
// stacks, up to maxUp, and the oldest is pushed off and counted. Neither loses
// anything: every one of them is in the history and on the queue by the time
// this hears about it, which is principle 3 - display policy, never data policy
// (docs/vision.md). What this drops is a glance, not a record.
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
    id: popup

    // What is on the screen, newest first. A card is one arrival narrowed to
    // what a toast draws: id, from, self, text, body, urgent, actions, more -
    // and `until`, which is the wall-clock instant it stops being worth showing.
    //
    // `self` is the one of those that is not the sender's own claim: it says
    // zded made the record rather than an app on the bus, and it is what the
    // badge on the card is bound to. The name beside it is a string an app
    // chose, "zde" included (internal/attn, Notification.Self).
    //
    // A plain array rather than a model, because it is three items long and
    // every change to it replaces the whole thing: QML re-reads a property that
    // was assigned, and not one that was mutated in place.
    property var stack: []
    // How many were pushed off to make room. Said on the surface rather than
    // dropped quietly, because a stack that silently ends reads as the machine
    // having sent fewer than it did - the same honesty the center's "+n this
    // surface cannot reach" is there for.
    property int lost: 0
    // Whether somebody has asked for the keyboard. Nothing else sets this: it
    // is Mod+Ctrl+n and only Mod+Ctrl+n (shell.qml, reachPopup).
    property bool reached: false
    // The id the keys act on, "" for the newest. An id rather than an index
    // because the stack shifts underneath it - a card can expire or be pushed
    // off while somebody is choosing on it, and an index would silently start
    // pointing at a different notification's buttons.
    property string on: ""
    // What this surface has to say back: a refusal from zded, or what was just
    // sent. The one thing a list of buttons cannot do on its own is explain why
    // a key did nothing.
    property string note: ""
    // What the reach event asked for, sent back once the keyboard is actually
    // here. The key is waiting on it (internal/zded, ackWait).
    property string token: ""

    // Three, because that is what fits under a bar at a readable size and what
    // a person can take in without reading. The rest is what the center is for.
    readonly property int maxUp: 3
    // How long a card stays. Five seconds is a glance; the urgent one gets
    // longer because it is the one the sender said should interrupt, and a
    // critical notification that vanished before it was read is the failure this
    // whole surface exists to prevent.
    readonly property int dwell: 5000
    readonly property int urgentDwell: 15000

    readonly property int cardHeight: 86
    readonly property int gap: 8
    readonly property int footerHeight: 22
    // The gutter the card's first line starts after, wide enough for the badge
    // word in this surface's monospace 11. Kept on every card, badge or not, so
    // that the mark has a column an arrival's own text never reaches: a name is
    // a string an app chose, and a mark drawn in the same place as that string
    // is a mark an app can send.
    readonly property int badgeWidth: 30

    // invoke(id, key) presses one of a notification's actions; drop(id) takes it
    // off. Both go to zded over the socket - this surface knows nothing about
    // sockets, and decides nothing (docs/vision.md, section 2).
    signal invoke(string id, string key)
    signal drop(string id)
    signal shown(string token)

    // One arrival. Called for every popup event zded sends, whatever is already
    // on the screen.
    function arrived(r) {
        const urgent = r.urgent === true;
        const card = {
            id: String(r.id),
            from: r.from ? String(r.from) : "",
            // Taken as a boolean rather than as whatever arrived, so that a
            // field that is missing, empty or a string reads as "an app sent
            // this" - which is the safe way for this one to be wrong.
            self: r.self === true,
            text: r.text ? String(r.text) : "",
            body: r.body ? String(r.body) : "",
            urgent: urgent,
            actions: r.actions ? r.actions : [],
            more: r.moreActions ? r.moreActions : 0,
            until: Date.now() + (urgent ? popup.urgentDwell : popup.dwell)
        };
        let next = popup.stack.slice();
        // Coalesced by sender. A download sends the same notification a hundred
        // times as it goes, and without this the screen is that download three
        // times over at three different percentages. The cost is honest and
        // worth saying: two different messages from one app inside five seconds
        // show as the later one. Both are in the history, which is where the
        // earlier one is read.
        // The mark counts as part of who this is, or the desktop's card and an
        // app's card would be one card whenever the two names matched - which
        // is a thing an app chooses, since the name is its own claim.
        const same = next.findIndex(c => c.from === card.from && c.self === card.self);
        if (same >= 0)
            next.splice(same, 1);
        next.unshift(card);
        while (next.length > popup.maxUp) {
            next.pop();
            popup.lost += 1;
        }
        popup.stack = next;
        popup.note = "";
        // The selection follows the newest only while nobody is holding the
        // keys. Moving it under a finger that is about to press 2 would send an
        // action from one notification to another.
        if (!popup.reached)
            popup.on = "";
    }

    // The card the keys act on: the one `on` names while it is still up, and
    // otherwise the newest. Self-healing on purpose - the named card can expire
    // or be pushed off, and falling back to the newest beats a surface that
    // stops answering keys because it is pointing at nothing.
    function current() {
        for (const c of popup.stack) {
            if (c.id === popup.on)
                return c;
        }
        return popup.stack.length > 0 ? popup.stack[0] : null;
    }

    function indexOfCurrent() {
        const c = popup.current();
        return c ? popup.stack.findIndex(x => x.id === c.id) : -1;
    }

    // Mod+Ctrl+n. The only path to an exclusive grab in this file.
    function reach(newToken) {
        popup.token = newToken ?? "";
        popup.reached = true;
        // The newest, which is what the key names and what somebody pressing it
        // is looking at.
        popup.on = "";
        popup.note = "";
        holding.restart();
        if (popup.token !== "")
            popup.shown(popup.token);
    }

    // Give the keyboard back, and take the cards with it.
    //
    // The cards go because their dwell was frozen while the keys were here (see
    // the sweep timer), so leaving them would leave a stack that expires all at
    // once a moment later - and because Escape on a toast means "I am done with
    // this", which is the same thing.
    function release() {
        holding.stop();
        popup.reached = false;
        popup.token = "";
        popup.stack = [];
        popup.lost = 0;
        popup.on = "";
        popup.note = "";
    }

    // What shell.qml's present() calls when another surface takes over. The same
    // act: let go of the keyboard, and get off the screen.
    function hide() {
        popup.release();
    }

    // Drop the cards whose time is up. One sweep on a timer rather than a timer
    // per card, because the cards are a plain array and a Timer per element is
    // three objects to create, destroy and leak.
    function sweep() {
        const now = Date.now();
        const left = popup.stack.filter(c => c.until > now);
        if (left.length === popup.stack.length)
            return;
        popup.stack = left;
        if (left.length === 0) {
            popup.lost = 0;
            popup.note = "";
        }
    }

    function step(by) {
        const n = popup.stack.length;
        if (n === 0)
            return;
        const i = Math.max(0, popup.indexOfCurrent());
        popup.on = popup.stack[(i + by + n) % n].id;
    }

    // Enter: the sender's own answer to "what does choosing this mean". Refused
    // here rather than by zded so the answer arrives with the keypress, and zded
    // refuses it too, because the shell is not where a rule lives.
    //
    // "default" is the freedesktop spec's name for it, not zde's, which is why
    // it is a literal here: the spec fixes it, so this cannot drift out of step
    // with the Go side.
    function act() {
        const c = popup.current();
        if (!c)
            return;
        for (const a of c.actions) {
            if (a.key === "default") {
                popup.fire(c, a);
                return;
            }
        }
        popup.note = c.actions.length > 0 ? "no default action: press its number instead" : "nothing to invoke: " + (c.from === "" ? "it" : c.from) + " sent a notification, not a button";
    }

    // A digit: the action in that position. Out of range is said rather than
    // ignored - a key that does nothing on a surface offering numbered things
    // reads as the surface being broken.
    function press(i) {
        const c = popup.current();
        if (!c)
            return;
        if (i < 0 || i >= c.actions.length) {
            popup.note = c.actions.length === 0 ? "this one has no actions to press" : "there is no action " + (i + 1) + " on this one";
            return;
        }
        popup.fire(c, c.actions[i]);
    }

    // What both of them do. Optimistic by a millisecond: zded answers only when
    // it refuses, and a refusal replaces this the moment it arrives (shell.qml).
    function fire(c, a) {
        popup.note = "sent " + a.label + " to " + (c.from === "" ? "the app" : c.from);
        popup.invoke(c.id, a.key);
    }

    // d. The same verb the queue and the center have, because it is the same
    // act: this is not waiting any more, and whoever sent it gets told on the
    // bus (internal/zded, queue.done).
    function dismiss() {
        const c = popup.current();
        if (!c)
            return;
        popup.drop(c.id);
        popup.stack = popup.stack.filter(x => x.id !== c.id);
        popup.on = "";
        popup.note = "";
        if (popup.stack.length === 0)
            popup.release();
    }

    // Nothing on the stack is nothing on the screen. A binding rather than an
    // assignment, so there is exactly one thing that decides whether this
    // surface exists and no path that can leave an empty one mapped.
    visible: popup.stack.length > 0
    color: "transparent"

    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.namespace: "zde-attn-popup"
    // The line this whole file is about. None until somebody asks, and back to
    // None the moment they are done - so a notification arriving can never be
    // the reason a keystroke went somewhere else.
    WlrLayershell.keyboardFocus: popup.reached ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None

    // Top right, and only as big as it needs to be. Not full-screen like the
    // picker and the center: a full-screen surface with a transparent middle
    // still eats every click on the screen behind it, and this one is up while
    // somebody is working rather than while they are choosing.
    anchors {
        top: true
        right: true
    }
    // Zero rather than the bar's 26: this reserves nothing (it is not
    // furniture, it is a moment) and, being zero rather than -1, it is placed
    // below the space the bar does reserve instead of underneath the clock.
    exclusiveZone: 0

    implicitWidth: 420
    implicitHeight: popup.stack.length * (popup.cardHeight + popup.gap) + popup.gap + popup.footerHeight
    // qmllint enable uncreatable-type

    // The keyboard is never held for longer than somebody is using it. Restarted
    // on every keypress, so choosing an action is unhurried and walking away is
    // not: a layer surface holding an exclusive grab reads to niri as nothing
    // focused at all, so one left holding it would eat the next nav key and
    // every one after it (docs/roadmap.md, the nav-with-a-surface-up item).
    //
    // Started and stopped by hand rather than bound to `reached`, which is not a
    // style choice: restart() from the key handler assigns `running`, and an
    // assignment breaks a binding for good - so a bound one would arm itself on
    // the first reach and never again, leaving every later grab unbounded. That
    // is the exact failure this timer exists to prevent, arriving by the back
    // door.
    Timer {
        id: holding

        interval: 10000
        repeat: false
        onTriggered: popup.release()
    }

    // Not while the keyboard is here. A card that vanished under the fingers
    // choosing one of its buttons would move the selection to another
    // notification between the eye and the keypress, and send the wrong app the
    // wrong action. The holding timer above is what bounds this instead.
    Timer {
        interval: 250
        repeat: true
        running: popup.stack.length > 0 && !popup.reached
        onTriggered: popup.sweep()
    }

    Rectangle {
        id: frame

        anchors.fill: parent
        color: "transparent"

        focus: true
        Keys.onPressed: event => {
            // Every press buys more time, so the grab lasts as long as the
            // person and no longer.
            holding.restart();
            switch (event.key) {
            case Qt.Key_Escape:
                popup.release();
                break;
            case Qt.Key_Return:
            case Qt.Key_Enter:
                popup.act();
                break;
            case Qt.Key_Down:
                popup.step(1);
                break;
            case Qt.Key_Up:
                popup.step(-1);
                break;
            default:
                // The same vocabulary as the notification center, on purpose:
                // j/k walk, a digit presses that action on the one you are on,
                // d dismisses. Two surfaces about the same thing must not want
                // two sets of fingers.
                if (event.text === "j")
                    popup.step(1);
                else if (event.text === "k")
                    popup.step(-1);
                else if (event.text === "d")
                    popup.dismiss();
                else if (event.text >= "1" && event.text <= "9")
                    popup.press(parseInt(event.text, 10) - 1);
                else {
                    return;
                }
            }
            event.accepted = true;
        }

        Column {
            id: cards

            anchors.left: parent.left
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.margins: popup.gap
            spacing: popup.gap

            Repeater {
                model: popup.stack

                Rectangle {
                    id: card

                    required property var modelData
                    required property int index

                    width: cards.width
                    height: popup.cardHeight
                    radius: 6
                    color: "#11121a"
                    // The one the keys are on is outlined rather than filled:
                    // this surface is read at a glance and a filled row on a
                    // dark toast reads as an alert of its own. Only outlined
                    // while somebody is holding the keys, because with nobody
                    // holding them there is no "one you are on" to show.
                    border.color: (popup.reached && card.index === popup.indexOfCurrent()) ? "#e5a23d" : "#2a2c37"
                    border.width: 1
                    clip: true

                    // Clickable without the keyboard ever being asked for, which
                    // is the other half of not grabbing: a mouse can press any
                    // of these at any time. Clicking the card puts the keys'
                    // notion of "the one you are on" here too, so a click then a
                    // digit does what it looks like it does.
                    MouseArea {
                        anchors.fill: parent
                        onClicked: popup.on = card.modelData.id
                    }

                    // The desktop's own badge, in a column of its own at the
                    // head of the card, drawn from this element's literal and
                    // never from anything that arrived.
                    //
                    // A card off the bus can say it is called "zde" in any
                    // number of alphabets that draw the same three letters -
                    // "zdе" with a Cyrillic е, "ｚｄｅ" in fullwidth, "ᴢᴅᴇ" in
                    // small capitals - and each of them lands in the name beside
                    // this, where it belongs. What it cannot do is make this
                    // element draw anything, because the only thing it is bound
                    // to is a mark zded set when it made the record
                    // (internal/attn, Notification.Self).
                    Text {
                        id: badge

                        anchors.top: parent.top
                        anchors.left: parent.left
                        anchors.margins: 8
                        width: popup.badgeWidth
                        text: card.modelData.self ? "zde" : ""
                        color: "#e5a23d"
                        font.pixelSize: 11
                        font.family: "monospace"
                        textFormat: Text.PlainText
                    }

                    Text {
                        id: who

                        anchors.top: parent.top
                        anchors.left: badge.right
                        anchors.right: parent.right
                        anchors.topMargin: 8
                        anchors.rightMargin: 8
                        // The name is left out when the badge is up: it is the
                        // reserved word either way (internal/attn, SelfFrom), and
                        // the same fact in two places, one of them a string, is
                        // the arrangement a lookalike walks through.
                        text: (card.modelData.urgent ? "! " : "") + (card.modelData.self ? "" : (card.modelData.from === "" ? "-" : card.modelData.from))
                        elide: Text.ElideRight
                        color: card.modelData.urgent ? "#e5484d" : "#7a7f8a"
                        font.pixelSize: 11
                        font.family: "monospace"
                        textFormat: Text.PlainText
                    }

                    Text {
                        id: summary

                        anchors.top: who.bottom
                        anchors.topMargin: 2
                        anchors.left: parent.left
                        anchors.right: parent.right
                        anchors.leftMargin: 8
                        anchors.rightMargin: 8
                        text: card.modelData.text
                        elide: Text.ElideRight
                        color: "#c9ccd4"
                        font.pixelSize: 13
                        font.family: "monospace"
                        textFormat: Text.PlainText
                    }

                    // The body, on one line here. What was kept has its line
                    // breaks (internal/attn, bodyText) and this is a toast: a
                    // paragraph drawn into it would push the buttons off the
                    // card. The center is where the shape of it survives.
                    Text {
                        id: rest

                        anchors.top: summary.bottom
                        anchors.topMargin: 2
                        anchors.left: parent.left
                        anchors.right: parent.right
                        anchors.leftMargin: 8
                        anchors.rightMargin: 8
                        text: card.modelData.body === "" ? "" : card.modelData.body.replace(/\s+/g, " ")
                        elide: Text.ElideRight
                        color: "#9aa0ac"
                        font.pixelSize: 11
                        font.family: "monospace"
                        textFormat: Text.PlainText
                    }

                    // The buttons, which are the point. Every action the sender
                    // declared, with the label it wrote and the digit that
                    // presses it - and a count of the ones past what zded keeps,
                    // so a list that ends reads as a bound and not as an app
                    // that sent less than it did (internal/attn, actionsMax).
                    Row {
                        id: buttons

                        anchors.bottom: parent.bottom
                        anchors.left: parent.left
                        anchors.margins: 8
                        spacing: 6
                        visible: card.modelData.actions.length > 0

                        Repeater {
                            model: card.modelData.actions

                            Rectangle {
                                id: button

                                required property var modelData
                                required property int index

                                height: 20
                                width: label.implicitWidth + 14
                                radius: 4
                                color: "#2a2c37"

                                MouseArea {
                                    anchors.fill: parent
                                    onClicked: {
                                        popup.on = card.modelData.id;
                                        popup.fire(card.modelData, button.modelData);
                                    }
                                }

                                Text {
                                    id: label

                                    anchors.centerIn: parent
                                    text: (button.index + 1) + " " + button.modelData.label
                                    color: "#c9ccd4"
                                    font.pixelSize: 11
                                    font.family: "monospace"
                                    textFormat: Text.PlainText
                                }
                            }
                        }
                    }

                    Text {
                        anchors.bottom: parent.bottom
                        anchors.right: parent.right
                        anchors.margins: 8
                        visible: card.modelData.more > 0
                        // Named, because there are two counts on this surface
                        // and they are different facts: actions its sender
                        // declared past what zded keeps, and cards pushed off
                        // the stack (the line under it).
                        text: "+" + card.modelData.more + " actions"
                        color: "#7a7f8a"
                        font.pixelSize: 11
                        font.family: "monospace"
                        textFormat: Text.PlainText
                    }
                }
            }
        }

        // The line under the stack. It says the key, which is the whole reason
        // anybody will ever press it: a surface that quietly waits to be
        // reached is one nobody reaches. A refusal replaces it, because a key
        // that did nothing has to say so somewhere (docs/vision.md, principle 4).
        Text {
            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.margins: popup.gap
            elide: Text.ElideRight
            text: {
                if (popup.note !== "")
                    return popup.note;
                const more = popup.lost > 0 ? "    +" + popup.lost + " more in the center" : "";
                if (popup.reached)
                    return "j k move    enter default    1-9 action    d dismiss    esc let go" + more;
                return "Mod+Ctrl+n for the keys" + more;
            }
            color: popup.note !== "" ? "#e5a23d" : "#7a7f8a"
            font.pixelSize: 11
            font.family: "monospace"
            textFormat: Text.PlainText
        }
    }
}
