// The zde bar (docs/roadmap.md, 0.1: shell MVP). Whatever a keypress depends
// on belongs on the bar (docs/vision.md, principle 4). Today that is the
// queue, because the queue is the half of 0.1 that had nowhere to be seen:
// `zde queue add` and every notification the session receives land in it, and
// until now the only way to know was to go and ask.
//
// And the attn mode beside it, which is the other half of the same question:
// the queue says what is waiting, the mode says what is allowed to arrive. "Why
// has nothing come in all afternoon" is exactly what principle 4 means by
// something a keypress depends on, and Mod+q changes it from anywhere.
//
// The mic is here too, and it is the PipeWire subscription this note used to
// say it wanted rather than a poll of wpctl: muted, and something holding the
// microphone, are the two states worth a word on the strip, and it is empty for
// the rest.
//
// The input mode (Normal, Window, Kb-mouse, One-hand, Passthrough) is a
// different thing with the same name, and it is the one still not here: it
// needs the input daemon, so it arrives with what owns it.
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
import Quickshell.Io
import Quickshell.Services.Pipewire
import Quickshell.Services.UPower
import Quickshell.Wayland

ShellRoot {
    id: root

    // What the bar knows, and whether it knows it. `known` is the difference
    // between a number and the last number: a count left on screen after zded
    // stopped answering looks current, which is worse than saying nothing.
    property int queued: 0
    property int urgent: 0
    property bool linked: false
    property bool known: false
    // The attn mode, and whether zded has said. Same bargain as the count: a
    // mode left on the bar after the daemon stopped answering is worse than no
    // mode at all, because this is the line somebody reads to decide whether to
    // trust the silence.
    property string mode: ""
    property bool modeKnown: false
    // Zen: hide bar, borders, gaps (docs/glossary.md). The bar is the half this
    // process owns; the rest is niri's and zded writes it into niri's config.
    //
    // Held here and never decided here. zded is where the toggle lives, because
    // this process is restarted by every home-manager switch and a state kept in
    // it would be one the person set and the machine forgot. So the shell asks,
    // and `zenKnown` is the same bargain the count and the mode make - except
    // that this one fails towards showing the bar. A strip hidden because the
    // daemon that knows why has gone away is a strip nobody can get back.
    property bool zen: false
    property bool zenKnown: false
    // What a panel actually came up as, reported back for the same reason the
    // count is: a strip of zero height, or one reserving nothing, is on the
    // screen as far as the compositor's layer list is concerned and invisible
    // or in the way as far as a person is concerned.
    property int barHeight: 0
    property int barZone: 0
    // Set when a question has gone out and no answer has come back. Two of
    // those in a row and the number stops being trustworthy, which is how a
    // daemon that holds the socket open and goes quiet gets noticed.
    property int waiting: 0
    // The same watchdog for the other connection, because the mode is asked on
    // that one and needs the same suspicion. Without it, a zded that holds both
    // sockets open and stops answering - stopped, deadlocked, or blocked in
    // Dispatch on a niri that has wedged, which this connection can reach
    // through desk.switch - blanked the queue count after four seconds and went
    // on showing "attn quiet" for the rest of the session. That is the most
    // expensive stale value on the strip: it is the line somebody reads to
    // decide whether the silence is the desktop's doing.
    property int streamWaiting: 0

    // The battery, read once at the root: the bar draws it and the IPC reports
    // it, and two readings of the same thing would eventually disagree.
    QtObject {
        id: batteryState

        readonly property var dev: UPower.displayDevice
        readonly property bool have: batteryState.dev !== null && batteryState.dev.isLaptopBattery
        readonly property int pct: batteryState.have ? Math.round(batteryState.dev.percentage * 100) : 0
        readonly property bool charging: batteryState.have
            && (batteryState.dev.state === UPowerDeviceState.Charging
                || batteryState.dev.state === UPowerDeviceState.FullyCharged)
        readonly property int secsLeft: batteryState.have ? batteryState.dev.timeToEmpty : 0
    }

    // The mic, read once at the root for the same reason the battery is: the
    // bar draws it and the IPC reports it, and two readings would eventually
    // disagree.
    //
    // The default source rather than a microphone of this widget's choosing,
    // because that is the node Mod+Ctrl+m toggles - `wpctl set-mute
    // @DEFAULT_AUDIO_SOURCE@`, in internal/keymap/registry.go - and a key and a
    // strip that can disagree about which mic they mean are worse than either
    // alone. The cost is a capture from some other source going unseen, which
    // needs the whole graph and belongs with the mixer widget (0.2).
    QtObject {
        id: micState

        readonly property var src: Pipewire.defaultAudioSource
        // Whether PipeWire has answered at all - the same distinction the queue
        // makes with `known`. A machine with no microphone and a PipeWire that
        // is not there both have no source to report, and only one of the two
        // is a fact about the machine.
        readonly property bool known: Pipewire.ready
        readonly property bool have: micState.known && micState.src !== null
        // Bound below, because muted is one of the properties PipeWire sends
        // only for an object somebody asked about. Unbound it reads false,
        // which on this widget is the wrong way round.
        readonly property bool muted: micState.have
            && micState.src.audio !== null
            && micState.src.audio.muted
        // Something holding the microphone. Deliberately not "bytes are
        // moving": a stream that is open and paused starts pulling again
        // without asking anybody, so what earns a red word on the strip is that
        // something has it - the mic holders principle 4 names.
        //
        // Gated on `have` like the others, because this is the one the strip
        // reads directly: ungated it could put a red "mic live" on the bar in a
        // state where mic() answers "unknown" or "none", and the word and the
        // reading must not be able to disagree about whether a room is heard.
        readonly property bool live: micState.have && micLinks.linkGroups.length > 0
    }

    // PipeWire sends a node's name and little else until somebody asks for the
    // rest, and muted is in the rest.
    PwObjectTracker {
        objects: [Pipewire.defaultAudioSource]
    }

    // What is connected to the microphone. This type drops monitor links
    // itself, so what is left is something capturing rather than the graph's
    // own plumbing.
    PwNodeLinkTracker {
        id: micLinks

        node: Pipewire.defaultAudioSource
    }

    // One connection, held open. zded speaks line-delimited JSON, so asking
    // costs a write and a read; a `zde queue` per tick would be a process per
    // tick, and on a bar that is visible.
    //
    // XDG_RUNTIME_DIR with no fallback on purpose: zded refuses to start
    // without it (internal/zded, DefaultSocket), so a guessed path could only
    // ever point at a directory belonging to nobody, or to somebody else.
    //
    // Every connection here is a Dialer and not a Socket, which is the same
    // thing with the redial done by throwing the socket away rather than by
    // writing `connected = true` on it. Why that is necessary is in
    // Dialer.qml, and it is a defect in quickshell 0.3.0 rather than anything
    // this shell did: written the obvious way, one dial landing in the moment
    // zded is restarting leaves the connection dead for the rest of the
    // session.
    Dialer {
        id: zded

        path: Quickshell.env("XDG_RUNTIME_DIR") + "/zde/zded.sock"

        onConnectedChanged: {
            root.linked = zded.connected;
            root.waiting = 0;
            if (zded.connected) {
                ask.triggered();
            } else {
                root.known = false;
            }
        }

        onHeard: line => {
            root.waiting = 0;
            let res = null;
            try {
                res = JSON.parse(line);
            } catch (e) {
                root.known = false;
                return;
            }
            if (!res || res.error !== undefined) {
                root.known = false;
                return;
            }
            // ok is the queue, oldest first, or null when it is empty - Go
            // marshals an empty slice as null, and `null.length` is the
            // kind of thing that takes a bar down.
            const items = res.ok || [];
            root.queued = items.length;
            root.urgent = items.filter(i => i.urgent === true).length;
            root.known = true;
        }
    }

    // Two seconds, and no push. zded answers questions and does not yet raise
    // its voice, so this is the latency between something arriving and the bar
    // admitting it. An event stream is the fix, and it is worth having for the
    // notification center rather than for this.
    //
    // Only the asking. The redial is the Dialer's, on its own backoff, because
    // a redial that is a line in a poll loop is a redial that has to be
    // written into every poll loop - and it was, in three of them, each with
    // its own idea of how often.
    Timer {
        id: ask

        interval: 2000
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: {
            if (!zded.connected)
                return;
            if (root.waiting >= 2) {
                root.known = false;
            }
            root.waiting += 1;
            zded.write('{"method":"queue.list"}\n');
        }
    }

    // The events connection, separate from the one the bar polls on. Same
    // socket, second connection: a stream and a poll on one connection would
    // work, and keeping them apart means a broken parser on either side cannot
    // take the other with it.
    Dialer {
        id: stream

        path: Quickshell.env("XDG_RUNTIME_DIR") + "/zde/zded.sock"

        onConnectedChanged: {
            root.streamWaiting = 0;
            if (stream.connected) {
                stream.write('{"method":"events"}\n');
                // And zen, immediately rather than on the first tick: this
                // connection is what a restarted shell has instead of a memory,
                // and two seconds of a bar that should not be there is two
                // seconds of the toggle looking lost.
                stream.write('{"method":"desk.zen"}\n');
            } else {
                root.modeKnown = false;
                root.zenKnown = false;
            }
        }

        onHeard: line => {
            let msg = null;
            try {
                msg = JSON.parse(line);
            } catch (e) {
                // A line that is not zded's is a protocol nobody here
                // understands, so the mode this shell is holding is no
                // longer something to claim.
                root.modeKnown = false;
                return;
            }
            if (!msg) {
                root.modeKnown = false;
                return;
            }
            // The mode, which is asked on this connection rather than the
            // bar's: the bar's parser reads every reply as a queue listing.
            // Recognised by its shape, because the protocol has no request
            // ids and this connection carries the replies to everything the
            // surfaces ask for.
            if (msg.ok && msg.ok.mode !== undefined) {
                // The answer to the question the watchdog counts, and only
                // this one: an event or an acknowledgement proves the
                // daemon is alive without proving it is still answering
                // about the mode, and the mode is what this line claims.
                root.streamWaiting = 0;
                root.mode = msg.ok.mode;
                root.modeKnown = true;
                return;
            }
            // Whether the chrome is hidden, recognised the same way and for the
            // same reason. Asked for rather than read off an event, so the bar
            // and zded cannot end up holding two different answers: the event
            // below only says to ask again.
            if (msg.ok && msg.ok.zen !== undefined) {
                root.zen = msg.ok.zen === true;
                root.zenKnown = true;
                return;
            }
            // A refusal, attributed to whichever surface is up that could
            // have earned one - a refusal swallowed is a key that did
            // nothing (docs/vision.md, principle 4). One arriving from
            // somewhere else lands on that surface too, which is the price
            // of a protocol with no request ids and is worth less than the
            // silence. The center is first because it is the one that holds
            // the keyboard while it asks.
            //
            // Then the three that wait on an answer: the palette, the power
            // menu and the clipboard. `running` is the precise claim among
            // these - a run really is in flight - where the other two are
            // only "up", so they go ahead of the loosest test here.
            //
            // The popup is last and is matched on visible rather than on
            // reached, which is the case a keyboard-shaped guess would
            // miss: its buttons are clickable without anybody ever asking
            // for the keys, so a refusal owed to a mouse press had nowhere
            // to land.
            if (msg.error !== undefined) {
                if (center.visible)
                    center.note = msg.error;
                else if (palette.running)
                    palette.ran(msg.error);
                else if (powerMenu.running)
                    powerMenu.ran(msg.error);
                else if (clips.running)
                    clips.ran(msg.error);
                else if (attnPopup.visible)
                    attnPopup.note = msg.error;
                return;
            }
            // Replies to our own subscribe arrive here too; only the lines
            // carrying an event are events.
            if (!msg.event) {
                // A run that worked, which is what closes the palette: it
                // stays up until the answer comes, so that a refusal has a
                // surface to appear on rather than a parser that drops it.
                if (palette.running)
                    palette.ran("");
                // And the power menu, which waits for the same reason and
                // needs it more: logind refuses a suspend an inhibitor is
                // holding, and a surface that had already closed would make
                // that indistinguishable from a machine that slept.
                if (powerMenu.running)
                    powerMenu.ran("");
                // And the clipboard, which waits for the same reason: an entry
                // can expire between the list being drawn and Enter being
                // pressed, and only this surface can explain that refusal.
                if (clips.running)
                    clips.ran("");
                return;
            }
            if (msg.event.kind === "picker")
                root.openPicker(msg.event);
            else if (msg.event.kind === "picker.move-window")
                root.openMoveWindow(msg.event);
            else if (msg.event.kind === "picker.move-workspace")
                root.openMoveWorkspace(msg.event);
            else if (msg.event.kind === "windows")
                root.openWindows(msg.event);
            else if (msg.event.kind === "notif-center")
                root.openCenter(msg.event);
            else if (msg.event.kind === "connections")
                root.openConnections(msg.event);
            else if (msg.event.kind === "ask")
                root.openAsk(msg.event, false);
            else if (msg.event.kind === "ask.panel")
                root.openAsk(msg.event, true);
            else if (msg.event.kind === "palette")
                root.openPalette(msg.event);
            else if (msg.event.kind === "power")
                root.openPower(msg.event);
            else if (msg.event.kind === "clip")
                root.openClips(msg.event);
            else if (msg.event.kind === "attn.popup")
                root.showPopup(msg.event);
            else if (msg.event.kind === "attn.reach")
                root.reachPopup(msg.event);
            else if (msg.event.kind === "zen")
                // The one event that draws nothing and asks instead. It carries
                // no state on purpose (internal/zded, EventZen): the reply above
                // is the only place this shell learns whether zen is on, so a
                // dropped event costs a poll and never a wrong bar.
                stream.write('{"method":"desk.zen"}\n');
        }
    }

    // The ask connection, and a third one for a reason the second one only
    // half had: an answer arrives on the connection that asked for it, in
    // pieces, over as long as a model takes. On the stream's connection those
    // pieces would sit in front of the acknowledgement a picker is waiting for,
    // and on the bar's they would be read as a queue listing.
    Dialer {
        id: askLink

        path: Quickshell.env("XDG_RUNTIME_DIR") + "/zde/zded.sock"

        // A connection that goes away takes any answer on it with it, and the
        // window has to be told: it will not take another question while one is
        // on its way, so without this it sits with "..." in the prompt and
        // refuses everything for the rest of the session. Two ways in, both
        // ordinary: zded is restarted by any switch that changes it, and a
        // client that stops reading has its connection closed rather than being
        // left waiting for an end that could not be delivered either.
        onConnectedChanged: {
            if (!askLink.connected)
                askWindow.finished("lost the connection to zded, so the answer stops there");
        }

        onHeard: line => {
            let msg = null;
            try {
                msg = JSON.parse(line);
            } catch (e) {
                return;
            }
            if (!msg)
                return;
            // A refusal to run at all - no tier configured, no such tier -
            // arrives as the reply rather than as a piece of an answer.
            if (msg.error !== undefined) {
                askWindow.finished(msg.error);
                return;
            }
            if (!msg.event || msg.event.kind !== "ask.text")
                return;
            if (msg.event.text)
                askWindow.chunk(msg.event.text);
            if (msg.event.done)
                askWindow.finished(msg.event.error ?? "");
        }
    }

    // Asking the stream for the mode, on the same tick as the bar's poll. Not
    // the redial any more: each connection redials itself now (Dialer.qml),
    // including the ask connection, which this loop used to have a line of its
    // own for because zded can close one connection without closing the rest.
    Timer {
        interval: 2000
        running: true
        repeat: true
        onTriggered: {
            if (!stream.connected)
                return;
            // Two questions out with nothing back: whatever mode is on the bar
            // is the last one zded said and not the one it is in.
            if (root.streamWaiting >= 2)
                root.modeKnown = false;
            root.streamWaiting += 1;
            // And the mode, on the same tick. It changes from a keybind rather
            // than from anything the shell did, so the bar has to ask - and
            // asking here rather than on the bar's own connection keeps the
            // reply away from a parser that reads every answer as a queue.
            stream.write('{"method":"attn.mode"}\n');
            // And zen, on the same tick and for the same reason: it changes
            // from a keybind, and the event that says so can be dropped when
            // this connection is busy (internal/zded, errSinkBusy). Asking is
            // what makes a bar that missed one come back on its own.
            stream.write('{"method":"desk.zen"}\n');
        }
    }

    // What choosing a row asks zded to do. Set when the picker is opened, since
    // that is the only place that knows which kind of rows went into it - the
    // picker itself is handed rows and hands back a key, and knows nothing
    // about sockets or methods.
    property string pickMethod: "desk.switch"

    // The desks, for the three verbs that want one named: go there, send the
    // focused window there, send the whole workspace there. One list, because it
    // is one question; what differs is the method a chosen row is spent on.
    //
    // Three functions rather than one taking the method, so that each name is a
    // literal where a scan can see it. The method a picker row is sent as is the
    // one this shell never writes inside a request - it is set here and read at
    // the choice - and internal/zded's wire test finds it by looking for exactly
    // this assignment (wire_test.go, methodHandedOnInQML). Passed in as an
    // argument it is invisible to that scan, which is how the shell would come
    // to ask for a method zded does not have with every test green.
    function openPicker(ev) {
        root.pickMethod = "desk.switch";
        root.showDesks(ev, "desks", "");
    }

    function openMoveWindow(ev) {
        root.pickMethod = "desk.move-window-to";
        root.showDesks(ev, "desks-move-window", "send this window to");
    }

    function openMoveWorkspace(ev) {
        root.pickMethod = "desk.move-workspace-to";
        root.showDesks(ev, "desks-move-workspace", "send this workspace to");
    }

    // The rows the three of them share. The note on a row is where you are,
    // because that is the one thing about a desk list you cannot see from the
    // list - and on a move it is also the row that would be a no-op, which is
    // where the cursor starts (Picker.qml, show).
    //
    // The caption says what choosing a row will do, and it is not decoration:
    // three verbs draw the same desks on the same surface, so without it the
    // digit you press means whatever the last key pressed decided.
    function showDesks(ev, kind, caption) {
        const here = ev.on ?? "";
        root.showPicker(ev, kind, (ev.desks ?? []).map(d => ({
                    key: d,
                    label: d,
                    note: d === here ? "here" : ""
                })), here, caption);
    }

    // The open windows. The app id and the title together, because neither
    // alone tells two terminals apart; the workspace on the right, because the
    // zde name is how a person recognises which desk a window is on.
    function openWindows(ev) {
        root.pickMethod = "window.jump-to";
        root.showPicker(ev, "windows", (ev.windows ?? []).map(w => ({
                    key: String(w.id),
                    label: (w.appId ?? "") + (w.title ? "  " + w.title : ""),
                    note: w.workspace ?? ""
                })), "", "");
    }

    // What arrived, for the notification center. The rows go over as they came
    // off the socket: this shell decides nothing about them, and the surface
    // knows how to read one.
    function openCenter(ev) {
        root.present(center, ev);
        center.show(ev.notifications ?? [], ev.token ?? "");
    }

    // A notification, the moment it arrives. Deliberately not through present():
    // that helper takes down whatever surface is up, and an arrival is not
    // somebody asking for one - a chat message must not close the picker you
    // were choosing from. It is allowed to sit over another surface precisely
    // because it holds no keyboard, so there is no grab to fight over (see
    // AttnPopup.qml).
    function showPopup(ev) {
        const list = ev.notifications ?? [];
        if (list.length === 0)
            return;
        // The screen the person is on, the way every other surface does it - but
        // never while the popup has the keyboard: moving a focused layer surface
        // to another output is a remap, and the grab somebody deliberately asked
        // for would go with it.
        if (!attnPopup.reached)
            attnPopup.screen = root.screenFor(ev);
        attnPopup.arrived(list[0]);
    }

    // What was copied recently, newest first. The rows carry a preview and not
    // the entry: the text stays in zded until a row is picked, so what crosses
    // into this process is a couple of hundred characters a row and never the
    // history itself (internal/clip, PreviewMax).
    //
    // And what does cross is dropped when the surface closes (ClipHistory.qml,
    // hide), which is the honest version of "nothing here to expire". There is
    // no TTL in this process, so previews kept here would outlive zded's - fifty
    // of them, for the rest of the session. A surface that only stops drawing
    // them is a surface that still has them.
    function openClips(ev) {
        root.present(clips, ev);
        clips.show((ev.clips ?? []).map(c => ({
                    id: c.id,
                    preview: c.preview ?? "",
                    cut: c.cut === true,
                    kind: c.kind ?? "text",
                    why: c.why ?? "",
                    at: c.at
                })), ev.token ?? "");
    }

    // Mod+Ctrl+n: the one deliberate act that puts the keyboard on a popup, and
    // the only path in this shell that ever does. Through present(), because
    // from here on it is a surface holding an exclusive grab like any other and
    // the one-at-a-time rule is what keeps the keys where they can be seen.
    //
    // Nothing on the screen means nothing acknowledged, so the key falls through
    // to saying so rather than grabbing an empty surface (internal/zded, reach).
    function reachPopup(ev) {
        if (!attnPopup.visible)
            return;
        root.present(attnPopup, ev);
        attnPopup.reach(ev.token ?? "");
    }

    // One instance and not one per screen: it appears on the screen zded says
    // is being looked at, because a picker on every monitor is not a picker. An
    // unknown output falls back to the first screen, which on one monitor is
    // the right answer and on several is at least a screen.
    //
    // The caption travels with the rows rather than being set beside this call,
    // so that every caller has to say what choosing a row means - a surface left
    // holding the last verb's caption is the same bug as one left holding its
    // rows.
    function showPicker(ev, kind, rows, here, caption) {
        root.present(picker, ev);
        picker.show(kind, rows, here, ev.token ?? "", caption);
    }

    // The ask popup and the ask panel, on the screen being looked at for the
    // same reason the picker is. The question comes with the event only when
    // the panel was asked for from a terminal (`zde ask panel <question>`);
    // otherwise it is typed into the window, and the window still knows nothing
    // about sockets - what it does with one that arrived this way is its own
    // decision (AskWindow.qml, askFromOutside).
    function openAsk(ev, isPanel) {
        root.present(askWindow, ev);
        askWindow.show(isPanel, ev.token ?? "", ev.question ?? "");
    }

    // Every action zde has, by name. The key goes on the row because half of
    // what a palette is for is learning the key you forgot, and whether the row
    // works at all is zded's answer, not the shell's - most of the keymap is
    // bound to commands nobody has written yet.
    function openPalette(ev) {
        root.present(palette, ev);
        palette.show((ev.actions ?? []).map(a => ({
                    name: a.name,
                    desc: a.desc ?? "",
                    key: a.key ?? "",
                    live: a.live === true,
                    why: a.why ?? ""
                })), ev.token ?? "");
    }

    // The five things that end a session or a machine. What each one is about
    // to cost comes with the event and is not worked out here: how many windows
    // close, what the notification center is holding that the queue never got,
    // who else is logged in and what is holding sleep are all facts this shell
    // has no way to reach (docs/vision.md, section 2 - a thin adapter with zero
    // logic inside).
    function openPower(ev) {
        root.present(powerMenu, ev);
        powerMenu.show((ev.choices ?? []).map(c => ({
                    name: c.name,
                    label: c.label ?? c.name,
                    desc: c.desc ?? "",
                    confirm: c.confirm === true,
                    costs: c.costs ?? [],
                    why: c.why ?? ""
                })), ev.token ?? "");
    }

    // The surface that is up. Every surface here is a full-screen overlay that
    // takes an exclusive keyboard grab while it is visible, and niri hands that
    // grab to the oldest one of them - map order, no reverse - while drawing
    // the newest on top. So two of them up at once is keys going to the one
    // nobody can see: the notification center over the desk picker sends Enter
    // and the digits to the picker, which switches desk, and l locks the
    // screen; the other way round, d dismisses a notification and tells the app
    // that sent it, and over the connections surface d drops the wifi. Two
    // full-screen dims at 0.35 also composite to 0.58, so the screen darkens
    // per surface into the bargain.
    property var up: null

    // So: one at a time, decided here. Every opener calls this before it shows
    // anything - take down whatever was up, and put this one on the screen the
    // event names. In one place rather than in each surface, because a surface
    // that has to know its siblings is one that has to be edited every time
    // there is a new one, which is the coupling their own headers argue against.
    //
    // `up` is the surface put up last and not the surface that is visible: a
    // dismissed one leaves its name here, and hiding what is already hidden is
    // a no-op. Keeping it honest would mean a line in every place a surface
    // closes, which is the several places to forget that this exists to avoid.
    //
    // The notification popup is the one surface that is not always here, and
    // that is the point of it: it appears without a grab, so while it holds none
    // there is nothing for this to arbitrate and an arrival cannot close what
    // you are working in. It joins the list at the moment somebody presses
    // Mod+Ctrl+n and takes the keyboard, and leaves it when it lets go (see
    // reachPopup, and AttnPopup.qml).
    function present(surface, ev) {
        if (root.up && root.up !== surface)
            root.up.hide();
        root.up = surface;
        surface.screen = root.screenFor(ev);
    }

    // The screen an event asks for. One answer for every surface: two copies of
    // this would be two ideas about which monitor is being looked at.
    function screenFor(ev) {
        for (const s of Quickshell.screens) {
            if (s.name === ev.output)
                return s;
        }
        return Quickshell.screens[0] ?? null;
    }

    // What a surface asks zded to do, on the stream connection and not the
    // bar's: the bar's parser reads whatever arrives as a queue listing, so a
    // reply landing there made the bar say "1 waiting" about an empty queue for
    // two seconds. The stream's parser knows the difference.
    function send(req) {
        if (stream.connected)
            stream.write(JSON.stringify(req) + "\n");
        else
            console.warn("zde: " + req.method + " with no connection to zded");
    }

    // The one process this shell starts. Everything else it does is a line on a
    // socket; a leader action is a key doing what a key does.
    Process {
        id: leader
    }

    Picker {
        id: picker

        onChosen: key => {
            picker.hide();
            // The shell decides nothing: switching a desk and jumping to a
            // window are both zded's, over the same socket everything else uses
            // (docs/vision.md, section 2 - the shell is a thin adapter with
            // zero logic inside).
            root.send({
                method: root.pickMethod,
                args: [key]
            });
        }
        onDismissed: picker.hide()

        // A leader action runs the same command the key would, rather than the
        // shell learning what locking is: `zde system lock` resolves the
        // machine's locker through the same table Mod+t uses, and when that
        // becomes zcr's job it changes in one place and not two.
        onAction: name => {
            picker.hide();
            leader.command = ["zde", "system", name];
            leader.running = true;
        }

        // The asker is waiting on this, briefly, to find out whether anything
        // came of the event it sent.
        onShown: token => root.send({
            method: "shown",
            args: [token]
        })
    }

    NotifCenter {
        id: center

        // An action pressed on a row: the notification, and the key its sender
        // declared for that action. zded checks the key against what that
        // notification offered, and says so when the app has since exited -
        // which is the half only the bus knows.
        onInvoke: (which, key) => root.send({
            method: "attn.invoke",
            args: [which, key]
        })

        // d. The same verb the queue has, because it is the same act: this
        // notification is not waiting any more, and whoever sent it gets told
        // on the bus (internal/zded, queue.done).
        onDrop: which => root.send({
            method: "queue.done",
            args: [which]
        })
        onDismissed: center.hide()

        onShown: token => root.send({
            method: "shown",
            args: [token]
        })
    }

    ClipHistory {
        id: clips

        // The id of the entry, and nothing else: which entry that is and what
        // putting it back means are zded's (docs/vision.md, section 2). The
        // surface is left up until the answer comes, the way the palette is -
        // an entry can expire between this list being drawn and Enter being
        // pressed, and that refusal is the one thing only this surface can
        // explain.
        onChosen: entry => {
            if (!stream.connected) {
                clips.ran("no connection to zded");
                return;
            }
            root.send({
                method: "clip.history",
                args: [entry]
            });
        }
        onDismissed: clips.hide()
        onShown: token => root.send({
            method: "shown",
            args: [token]
        })
    }

    // The popup. The same two verbs the center has, over the same socket and to
    // the same methods, because pressing a sender's button and finishing a
    // notification are the same acts wherever they are done from - and a second
    // pair of methods would be a second place for the id check to be wrong.
    AttnPopup {
        id: attnPopup

        onInvoke: (which, key) => root.send({
            method: "attn.invoke",
            args: [which, key]
        })
        onDrop: which => root.send({
            method: "queue.done",
            args: [which]
        })
        onShown: token => root.send({
            method: "shown",
            args: [token]
        })
    }

    // ---- the network ----------------------------------------------------
    //
    // All of it in one place: what the bar says about the link, the surface
    // that joins one, and the connection both of them use.
    //
    // A connection of its own, and not the bar's. The bar's parser reads every
    // reply as a queue listing, so a second kind of question asked on it would
    // make the queue count wrong until the next tick; and a join is a request
    // whose answer is worth reading - "the password was refused" is the whole
    // point of the widget - which the events connection deliberately ignores.
    QtObject {
        id: netState

        // What zded last said the link is: wifi, wired, none, absent.
        property string kind: ""
        property string ssid: ""
        property int strength: 0
        // known is the same bargain the queue count makes. A signal reading
        // left on the bar after zded stopped answering looks current, and four
        // bars of wifi on a machine whose NetworkManager died is exactly the
        // lie this pattern exists to prevent.
        property bool known: false
    }

    Dialer {
        id: netLink

        path: Quickshell.env("XDG_RUNTIME_DIR") + "/zde/zded.sock"

        // What has been asked and not answered yet, oldest first. One
        // connection carries the bar's question and the surface's join, and
        // the answers come back in the order they were asked - without this,
        // the answer to a join would be read as a link status and the bar would
        // say something that is not a link.
        property var pending: []

        function ask(method, args) {
            if (!netLink.connected)
                return false;
            // The method and the network it is about, so a reply can be matched
            // to what it answers. The first argument only, and never the rest:
            // the second argument of a join is the password, and this is a
            // queue that outlives the write.
            netLink.pending.push({
                method: method,
                about: (args && args.length > 0) ? args[0] : ""
            });
            netLink.write(JSON.stringify({
                method: method,
                args: args ?? []
            }) + "\n");
            return true;
        }

        onConnectedChanged: {
            netLink.pending = [];
            if (netLink.connected)
                netLink.ask("net.status", []);
            else
                netState.known = false;
        }

        onHeard: line => {
            const was = netLink.pending.shift() ?? {
                method: "",
                about: ""
            };
            let res = null;
            try {
                res = JSON.parse(line);
            } catch (e) {
                netState.known = false;
                return;
            }
            if (was.method === "net.status") {
                if (!res || res.error !== undefined || !res.ok) {
                    netState.known = false;
                    return;
                }
                netState.kind = res.ok.kind ?? "";
                netState.ssid = res.ok.ssid ?? "";
                netState.strength = res.ok.signal ?? 0;
                netState.known = true;
                // And the surface, while it is up. A join often answers
                // "joining" rather than "joined" - NetworkManager takes
                // longer to decide than a keypress can wait - and the
                // header saying where you are is how that ends: it changes
                // when the link does. Without this the widget would sit
                // there naming the network you left, which is the same lie
                // the bar's known/unknown pattern exists to prevent.
                //
                // The link and not the rows. Signal moves on its own, so
                // re-listing would reorder what somebody is choosing from
                // under their hands.
                if (connections.visible)
                    connections.link = res.ok;
                return;
            }
            // A join or a disconnect. The surface is waiting to be told
            // what became of it, and a refusal is the half worth showing:
            // NetworkManager says whether the password was wrong or the
            // network went out of range, and a widget that swallowed that
            // would leave a person pressing Enter at nothing.
            if (!res)
                return;
            if (res.error !== undefined)
                connections.said = res.error;
            else if (res.ok !== undefined) {
                connections.said = String(res.ok);
                // A network that has been forgotten is not saved any more,
                // and the rows were handed over when the surface opened.
                // Without this the row would still read as saved and Enter
                // would join it without asking for the password that was
                // just thrown away, which is the whole point of the key.
                if (was.method === "net.forget")
                    connections.forgotten(was.about);
            }
            // And ask again at once, so the bar catches up with what just
            // changed rather than in five seconds' time.
            netLink.ask("net.status", []);
        }
    }

    Timer {
        // Five seconds rather than the bar's two: a link changes when somebody
        // walks out of a building, and a signal reading is not worth a question
        // a second. Asking only: the redial is the Dialer's, on its own clock,
        // so this no longer decides how long the bar stays blind.
        interval: 5000
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: {
            if (!netLink.connected)
                return;
            // Two questions outstanding means the first was never answered:
            // say nothing rather than the last thing that was true.
            if (netLink.pending.length >= 2)
                netState.known = false;
            netLink.ask("net.status", []);
        }
    }

    Links {
        id: connections

        // Straight down the socket and nowhere else. The secret is not put in
        // a property, not logged, and not kept: the surface empties its field
        // in the same breath as this, so the only copy left is the one
        // NetworkManager has.
        onJoin: (ssid, secret) => {
            if (!netLink.ask("net.connect", secret === "" ? [ssid] : [ssid, secret]))
                connections.said = "no connection to zded";
        }
        onDropped: {
            if (!netLink.ask("net.disconnect", []))
                connections.said = "no connection to zded";
        }

        // Forgetting is the way out of a password saved wrong: nothing asks for
        // one while NetworkManager has a profile, so without this a typo makes
        // a network unjoinable from zde for good.
        onForget: ssid => {
            if (!netLink.ask("net.forget", [ssid]))
                connections.said = "no connection to zded";
        }
        onDismissed: connections.hide()

        // The asker is waiting on this, briefly, to find out whether anything
        // came of the event it sent - the same handshake the picker makes, on
        // the connection the events arrive on.
        onShown: token => {
            if (stream.connected)
                stream.write(JSON.stringify({
                    method: "shown",
                    args: [token]
                }) + "\n");
        }
    }

    // One instance, on the screen zded says is being looked at: a widget on
    // every monitor is not a widget. Through the same helper as the picker,
    // because this had a copy of screenFor inlined and the copies had already
    // drifted - the one above answers with the first output that matches, this
    // one answered with the last.
    function openConnections(ev) {
        root.present(connections, ev);
        connections.show(ev.networks ?? [], ev.link ?? ({}), ev.token ?? "");
    }

    // ---- end of the network ---------------------------------------------

    // ---- the idle hold ----------------------------------------------------
    //
    // Whether something is stopping this session going idle, which on a machine
    // that blanks or locks on its own is whether that is going to happen
    // (docs/vision.md, principle 4).
    //
    // Read once at the root, like the mic and the battery, because the strip
    // draws it and the IPC reports it and two readings would eventually
    // disagree. Unlike the mic it cannot come from a Quickshell service: this
    // one is logind's inhibitor table, so it arrives down the zded socket
    // already decided (internal/zded/idle.go), which is the rule the whole shell
    // is built on.
    //
    // What it is honest about is the half it cannot see. logind's table is the
    // smaller half of holding a screen awake on Wayland; the Wayland protocol
    // itself, zwp_idle_inhibit_manager_v1, stops inside the compositor, and niri
    // 26.04 offers no way to read it back. So `held` here means "logind says
    // something is", and an empty list never means "nothing is". The word on the
    // strip is chosen to claim only the first of those, and the full statement
    // of the gap is in `zde doctor` and docs/verify.md, section 11, where there
    // is room to say it properly.
    QtObject {
        id: idleState

        // Whether zded answered and logind could say. The same bargain netState
        // makes: a machine that has lost its system bus must not read the same
        // as one where nothing is holding the screen.
        property bool known: false
        // How many holders logind named. A count and not a bool, because the
        // IPC reports it and a test that could not tell one holder from three
        // would pass on a widget that had stopped counting.
        property int holds: 0
    }

    // Its own connection, and a single-question one. The bar's parser reads
    // every reply as a queue listing and the network's is a FIFO matching
    // answers to joins; this asks one thing and nothing else ever, so every
    // reply it gets is the answer to that thing and it needs neither.
    Dialer {
        id: idleLink

        path: Quickshell.env("XDG_RUNTIME_DIR") + "/zde/zded.sock"

        onConnectedChanged: {
            if (idleLink.connected)
                idleLink.write(JSON.stringify({
                    method: "system.idle"
                }) + "\n");
            else
                idleState.known = false;
        }

        onHeard: line => {
            let res = null;
            try {
                res = JSON.parse(line);
            } catch (e) {
                idleState.known = false;
                return;
            }
            // zded answering is not logind answering. `known` is the daemon's
            // own word about whether the question reached logind at all, and a
            // reply that carries false is a machine that cannot say - which is
            // the one state this widget must not draw as "nothing is holding
            // it".
            if (!res || res.error !== undefined || !res.ok || !res.ok.known) {
                idleState.known = false;
                return;
            }
            // The count and not the length of the list beside it. zded sends at
            // most a handful of rows, because the number of them is a local
            // account's to choose and the reply goes down this socket every five
            // seconds (internal/zded/idle.go, holdsMax) - so the list is a
            // sample and `count` is how many there are. Reading the length would
            // make the strip say "held 6" on a machine holding eight thousand,
            // which is the widget inventing the one fact it reports.
            idleState.holds = res.ok.count ?? 0;
            idleState.known = true;
        }
    }

    Timer {
        // Five seconds, the network's clock rather than the bar's two: an
        // inhibitor is taken when an app starts a download or a call, not
        // several times a minute, and this is a system bus round trip on the
        // other end.
        interval: 5000
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: {
            if (!idleLink.connected)
                return;
            idleLink.write(JSON.stringify({
                method: "system.idle"
            }) + "\n");
        }
    }

    // ---- end of the idle hold ---------------------------------------------

    AskWindow {
        id: askWindow

        // The tier is a name and the shell passes it on: what a machine runs
        // for "local" is zded's answer, out of the same kind of table Mod+t
        // resolves through, and a shell that knew the command would be a second
        // place to configure one.
        //
        // The turns before the question go after it, in the order the window
        // holds them, and this shell neither reads nor bounds them: what a tier
        // is handed and how much of it there may be are zded's (docs/vision.md,
        // section 2 - the shell is a thin adapter with zero logic inside), so a
        // conversation that has grown too long is refused in one place and not
        // two.
        onAsked: (tier, question, prior) => {
            if (askLink.connected)
                askLink.write(JSON.stringify({
                    method: "ask.run",
                    args: [tier, question].concat(prior)
                }) + "\n");
            else
                askWindow.finished("no connection to zded, so there is nothing to ask");
        }
        onDismissed: askWindow.hide()

        onShown: token => {
            if (stream.connected)
                stream.write(JSON.stringify({
                    method: "shown",
                    args: [token]
                }) + "\n");
        }
    }

    ActionPalette {
        id: palette

        // The shell decides nothing here either: which action a name means, and
        // whether running it spawns a command or asks niri, are zded's
        // (docs/vision.md, section 2). On the stream connection, for the reason
        // the picker's choice is - the bar's parser reads every line it gets as
        // a queue listing.
        //
        // The surface is left up: it hides itself when the answer says the row
        // ran (see the parser above). With no connection there will be no
        // answer, so that is said here instead of leaving it waiting.
        onChosen: name => {
            if (!stream.connected) {
                palette.ran("no connection to zded");
                return;
            }
            root.send({
                method: "palette.run",
                args: [name]
            });
        }
        onDismissed: palette.hide()
        onShown: token => root.send({
            method: "shown",
            args: [token]
        })
    }

    PowerMenu {
        id: powerMenu

        // The shell decides nothing here either: which locker this machine has
        // and whether logind will take a suspend are zded's answers, on the
        // same socket everything else uses. The surface is left up until one
        // comes back - see the parser above - because a refusal is the thing
        // this menu exists to be able to show.
        onChosen: name => {
            if (!stream.connected) {
                powerMenu.ran("no connection to zded");
                return;
            }
            root.send({
                method: "system.power",
                args: [name]
            });
        }
        onDismissed: powerMenu.hide()
        onShown: token => root.send({
            method: "shown",
            args: [token]
        })
    }

    // How a test can ask the bar what it is showing, rather than only whether
    // it is running: `qs -p <config> ipc call queue count`. A bar that never
    // read the queue and a bar reading it correctly look identical from the
    // outside, and that is the mutation the smoke test could not otherwise
    // catch.
    // The picker, over the same IPC. Two reasons, and the second is the honest
    // one: a picker is driven by a keyboard, and a machine with no input devices
    // - which is every CI machine - cannot press a key. So the choice a key
    // makes is reachable from here too, which makes it testable and, as a side
    // effect, scriptable.
    IpcHandler {
        target: "picker"

        // What it is showing, how many rows, and which row you are already on -
        // a dash where there is none, so that the answer has the same shape
        // either way and a test is not comparing against a trailing space.
        function state(): string {
            if (!picker.visible)
                return "closed";
            const here = picker.on === "" ? "-" : picker.on;
            return "open " + picker.kind + " " + picker.rows.length + " " + here;
        }

        function pick(key: string): string {
            if (!picker.visible)
                return "closed";
            if (picker.rows.findIndex(r => r.key === key) < 0)
                return "no such row";
            picker.chosen(key);
            return "picked";
        }

        function dismiss(): string {
            picker.dismissed();
            return "closed";
        }

        // The leader letters, reachable the same way the pick is: a machine
        // with no input devices cannot press l, and the wiring behind it is
        // the part worth proving.
        function act(name: string): string {
            if (!picker.visible)
                return "closed";
            picker.action(name);
            return "acted";
        }
    }

    // The palette, over the same IPC and for the same reason: a machine with no
    // input devices cannot type into it, and typing is the whole interaction.
    IpcHandler {
        target: "palette"

        // What it is showing: how many actions it was handed, and how many the
        // filter leaves. Two numbers rather than one, because a filter that
        // matched everything and one that was never applied look identical from
        // a single count.
        function state(): string {
            if (!palette.visible)
                return "closed";
            return "open " + palette.rows.length + " " + palette.matches.length;
        }

        function filter(text: string): string {
            if (!palette.visible)
                return "closed";
            palette.narrow(text);
            return "filtered";
        }

        // Through run(), not straight to chosen(): the gate that refuses a row
        // nothing is written behind is the whole design, and a hatch that went
        // round it left CI never touching it. "cannot" is that gate saying no,
        // which is a thing worth being able to assert.
        function run(name: string): string {
            if (!palette.visible)
                return "closed";
            const i = palette.matches.findIndex(r => r.name === name);
            if (i < 0)
                return "no such row";
            palette.index = i;
            palette.run();
            return palette.running ? "ran" : "cannot";
        }

        function dismiss(): string {
            palette.dismissed();
            return "closed";
        }
    }

    // The power menu, over the same IPC and for the same reason the palette is
    // reachable that way: a machine with no input devices cannot press y, and
    // the second question is the whole design of this surface.
    IpcHandler {
        target: "power"

        // What it is showing: how many rows it was handed, and which one is
        // waiting for a y - a dash where none is, so the answer has the same
        // shape either way and a test is not comparing against a trailing
        // space.
        function state(): string {
            if (!powerMenu.visible)
                return "closed";
            const waiting = powerMenu.asking === "" ? "-" : powerMenu.asking;
            return "open " + powerMenu.rows.length + " " + waiting;
        }

        // Through choose(), not straight to chosen(): the gate that makes a
        // reboot ask before it happens is the whole design, and a hatch that
        // went round it would leave the one thing worth proving untouched.
        // "asks" is that gate holding.
        function choose(name: string): string {
            if (!powerMenu.visible)
                return "closed";
            const i = powerMenu.rows.findIndex(r => r.name === name);
            if (i < 0)
                return "no such row";
            powerMenu.choose(i);
            if (powerMenu.asking !== "")
                return "asks";
            return powerMenu.running ? "ran" : "nothing";
        }

        // The y. Refused when nothing asked, because a confirmation that can be
        // sent before the question is not a confirmation.
        function confirm(): string {
            if (!powerMenu.visible)
                return "closed";
            if (powerMenu.asking === "")
                return "nothing asked";
            powerMenu.confirm();
            return powerMenu.running ? "ran" : "nothing";
        }

        function dismiss(): string {
            powerMenu.dismissed();
            return "closed";
        }
    }

    IpcHandler {
        target: "queue"

        function count(): string {
            if (!root.linked)
                return "unlinked";
            if (!root.known)
                return "unknown";
            return root.queued + " " + root.urgent;
        }

        // What the battery reads, so a machine without one can be told apart
        // from a bar that failed to ask. A VM has no battery, which is the
        // case worth pinning: a desktop should say nothing here rather than
        // show a stub reading full.
        function battery(): string {
            if (!batteryState.have)
                return "none";
            return batteryState.pct + (batteryState.charging ? " charging" : " discharging");
        }

        // What the mic reads. Five words and not the three the strip shows,
        // because the two it hides for are different facts: "none" is PipeWire
        // answering that there is no source, "unknown" is PipeWire not
        // answering. A test that could not tell those apart would pass on a
        // widget that had never once worked.
        function mic(): string {
            if (!micState.known)
                return "unknown";
            if (!micState.have)
                return "none";
            if (micState.muted)
                return "muted";
            return micState.live ? "live" : "idle";
        }

        // What the bar makes of the link, so a machine with no NetworkManager
        // can be told apart from a bar that never managed to ask. The VM the
        // smoke test boots is that machine, which is why it is worth pinning:
        // from outside, three quiet states look like the same empty strip.
        function net(): string {
            if (!netState.known)
                return "unknown";
            if (netState.kind === "wifi")
                return "wifi " + netState.ssid + " " + netState.strength;
            return netState.kind;
        }

        // What the bar makes of the idle hold. Three words, and the first of
        // them is the one that matters: "unknown" is zded or logind not
        // answering, "none" is logind answering that its own table is empty.
        // Those are not the same fact and the strip hides for both, so a test
        // that could not tell them apart would pass on a widget wired to
        // nothing at all - which is exactly how this one would fail, since the
        // machine it runs on has no inhibitor to find.
        //
        // "none" and not "nothing is holding the screen awake": what is empty
        // is logind's table, and the Wayland half of the mechanism is invisible
        // from here (see the idle hold section above). The narrow word is the
        // honest one.
        function idle(): string {
            if (!idleState.known)
                return "unknown";
            return idleState.holds === 0 ? "none" : "held " + idleState.holds;
        }

        // Height and reserved space, as the panel came up. Not the same claim
        // as "the compositor honoured it" - proving that means measuring a
        // window with the bar and without it, which the smoke test does not do
        // yet - but it is the difference between a bar and a line of nothing.
        function geometry(): string {
            return root.barHeight + " " + root.barZone;
        }

        // What the bar makes of zen, and why it is on the screen anyway. Four
        // answers, because three different things put a strip up while zen is on
        // and only one of them is the widget working: "unknown" is zded not
        // having answered, "on mic" is the exception this bar makes on purpose,
        // and "on" with no bar is what zen looks like when it works.
        function zen(): string {
            if (!root.zenKnown)
                return "unknown";
            if (!root.zen)
                return "off";
            return micState.live ? "on mic" : "on";
        }
    }

    // One bar per screen. Variants rebuilds this list when screens come and
    // go, which is what makes docking work without the bar knowing anything
    // about it.
    Variants {
        model: Quickshell.screens

        // qmllint disable uncreatable-type
        // PanelWindow is exported from PanelWindowInterface, which is declared
        // uncreatable because Quickshell substitutes the platform
        // implementation (wlr-layer-shell) at runtime. Disabled for this
        // instantiation only: as a whole category it would also hide a real
        // attempt to build Quickshell itself, or a DataStream.
        PanelWindow {
            id: bar

            required property var modelData

            screen: bar.modelData
            color: "#11121a"
            implicitHeight: 26

            // Ours, not quickshell's default. It is what niri layer rules will
            // name, and it is what the smoke test looks for - matching the
            // default would have matched any quickshell on the machine.
            WlrLayershell.namespace: "zde-bar"

            anchors {
                top: true
                left: true
                right: true
            }

            // Reserve the space rather than float over the windows: niri
            // tiles to what is left, so a bar that does not reserve is a bar
            // that covers the top of whatever you are reading.
            exclusiveZone: bar.implicitHeight

            // Zen: the strip goes, and with it the space it was reserving -
            // which is why this is `visible` and not an opacity or a zero
            // height. A surface that is still mapped is still taking 26 pixels
            // off the top of the screen, and zen is not a paint job.
            //
            // Except while something is holding the microphone. Zen hides
            // chrome, and a room being heard is not chrome: it is the one line
            // on this strip that is about the world rather than about the
            // desktop, and a comfort toggle that could take it away would be a
            // way to hide a warning. Muted does not qualify - that is the safe
            // direction, and hiding it costs nothing.
            //
            // `zenKnown` is what keeps the failure the right way round: a bar
            // that hid because zded stopped answering could not be brought back
            // by anything, since the only thing that could is zded.
            visible: !root.zen || !root.zenKnown || micState.live

            // qmllint enable uncreatable-type

            Component.onCompleted: {
                root.barHeight = bar.height;
                root.barZone = bar.exclusiveZone;
            }

            Text {
                id: queueLine

                anchors.left: parent.left
                anchors.leftMargin: 10
                anchors.verticalCenter: parent.verticalCenter

                // The queue, in the words the CLI uses, so the bar and
                // `zde queue` never seem to be talking about different things.
                text: {
                    if (!root.linked)
                        return "zded: not answering";
                    if (!root.known)
                        return "queue: unknown";
                    if (root.queued === 0)
                        return "queue empty";
                    const n = root.queued === 1 ? "1 waiting" : root.queued + " waiting";
                    return root.urgent > 0 ? n + "  !" + root.urgent : n;
                }
                color: root.urgent > 0 ? "#e5484d" : (root.linked && root.known ? "#c9ccd4" : "#7a7f8a")
                font.pixelSize: 13
                font.family: "monospace"
                textFormat: Text.PlainText
            }

            // The attn mode, next to the queue it governs. Named rather than
            // shown as a symbol, and in the same word `zde attn` prints, so
            // that what is on the bar and what the CLI says are one vocabulary.
            //
            // Nothing at all until zded has answered - the `known` pattern the
            // count uses. A bar still reading "quiet" after the daemon went
            // away would be the most expensive stale value on the strip: it is
            // the line that explains an empty afternoon.
            Text {
                id: attnMode

                anchors.left: queueLine.right
                anchors.leftMargin: 16
                anchors.verticalCenter: parent.verticalCenter
                visible: root.linked && root.modeKnown

                text: "attn " + root.mode
                // Loud for the two modes that are holding things back, quiet
                // for the one that is not: work is the state nobody needs
                // reminding of, and the other two are the answer to a question
                // somebody is about to ask.
                color: root.mode === "work" ? "#7a7f8a" : "#e5a23d"
                font.pixelSize: 13
                font.family: "monospace"
                textFormat: Text.PlainText
            }

            // The right-hand chain, from the clock leftwards: clock, battery,
            // link, mic, idle hold. Each item anchors to the left edge of the
            // one before it, and three of the five can be zero-width - a desktop
            // has no battery, a machine with no sound card has no mic, and the
            // idle hold is empty whenever nothing logind can see is holding one
            // - so the order has to read the same with any of them missing.
            //
            // The two that come and go are at the end, in that order. Anchored
            // between the link and the battery either of them would push both
            // sideways every time somebody joined a call, and a bar that moves
            // while you are reading it is precisely what a keyboard-first strip
            // should not do. Out here they grow leftwards into empty bar: the
            // mic keeps a fixed position against the furniture whatever the idle
            // hold is doing, and the idle hold, being outermost and the rarer of
            // the two, is the only thing that ever moves.

            // The mic, on the bar for the reason principle 4 gives and W16 asks
            // for: whether the room is being heard is not something to find out
            // afterwards.
            //
            // Empty in every other state. No microphone, nothing holding it and
            // a PipeWire that is not answering all draw nothing, because a
            // strip that guesses here is worse than a quiet one - and a word
            // that is always on the bar is a word nobody reads.
            Text {
                id: mic

                anchors.right: netText.left
                anchors.rightMargin: 14
                anchors.verticalCenter: parent.verticalCenter
                visible: mic.text !== ""

                text: micState.muted ? "mic muted" : (micState.live ? "mic live" : "")
                // The red the urgent queue and a dying battery use, for the
                // same reason: it is what this bar keeps for the thing worth
                // interrupting yourself over. Muted is the opposite of that, so
                // it is said quietly.
                color: micState.muted ? "#7a7f8a" : "#e5484d"
                font.pixelSize: 13
                font.family: "monospace"
                textFormat: Text.PlainText
            }

            // The idle hold, outermost of the right-hand chain. Whether the
            // screen is going to do what it does when you walk away is the same
            // shape of fact as whether the room is being heard, and principle 4
            // puts both on the strip.
            //
            // Outside the mic rather than inside it, which is the whole reason
            // it is here and not next to the link. Two of these words come and
            // go now, and the one that comes and goes most often is the mic - so
            // the mic keeps the position it already had against the furniture,
            // and this one takes all of the movement by growing leftwards into
            // empty bar. The margin closes when the mic is not drawn, so the gap
            // to the link reads the same either way.
            //
            // Empty unless logind names a holder. A machine that cannot answer
            // draws nothing rather than guessing, the way the mic does - and
            // here that silence is doing more work than usual, because an empty
            // strip is also what a machine with a Wayland inhibitor nobody can
            // see looks like. The word claims only what logind said; `zde
            // doctor` is where the rest of the sentence lives.
            Text {
                id: idleHold

                anchors.right: mic.left
                anchors.rightMargin: mic.visible ? 14 : 0
                anchors.verticalCenter: parent.verticalCenter
                visible: idleHold.text !== ""

                text: idleState.known && idleState.holds > 0 ? "idle held" : ""
                // The amber the attn mode uses for the two modes that are
                // holding things back, and for the same reason: this is
                // something held off rather than something to interrupt
                // yourself over, and the red on this bar is kept for the
                // latter.
                color: "#e5a23d"
                font.pixelSize: 13
                font.family: "monospace"
                textFormat: Text.PlainText
            }

            // The link, on the bar for the reason principle 4 gives: what a
            // keypress depends on. Mod+Shift+c is that keypress, and this is
            // whether it is worth pressing.
            //
            // Three quiet states, deliberately not one. "unknown" is zded not
            // answering. "no manager" is a machine with no NetworkManager at
            // all, which is not offline - it may be online through
            // systemd-networkd, a static route or a tether, and zde simply
            // cannot say. "no network" is NetworkManager itself saying so.
            //
            // Always drawn, which is what makes it the fixed point the mic
            // hangs off: it is furniture, not an alert. On a desktop with no
            // battery this is the thing beside the clock, with two margins
            // between them rather than one - whitespace where the battery would
            // be, which reads as a gap and not as a missing widget.
            Text {
                id: netText

                anchors.right: battery.left
                anchors.rightMargin: 14
                anchors.verticalCenter: parent.verticalCenter

                text: {
                    if (!netState.known)
                        return "net: unknown";
                    switch (netState.kind) {
                    case "wifi":
                        // The name is the useful half - it is how you know
                        // which of two networks you are on - with the strength
                        // beside it, because that is what a dropout looks like
                        // before it happens.
                        return (netState.ssid === "" ? "wifi" : netState.ssid) + "  " + netState.strength + "%";
                    case "wired":
                        return "wired";
                    case "absent":
                        return "net: no manager";
                    }
                    return "no network";
                }
                color: netState.known && netState.kind !== "absent" ? "#c9ccd4" : "#7a7f8a"
                font.pixelSize: 13
                font.family: "monospace"
                textFormat: Text.PlainText
            }

            // The battery, which is on the bar for the reason principle 4 gives:
            // what a keypress depends on. On a laptop away from a desk, what
            // every decision depends on is how long it has left.
            //
            // Empty on a machine with no battery rather than a stub reading
            // 100%: a desktop should say nothing here, and UPower's display
            // device on one is not a laptop battery.
            Text {
                id: battery

                readonly property bool have: batteryState.have
                readonly property int pct: batteryState.pct
                readonly property bool charging: batteryState.charging

                anchors.right: clock.left
                anchors.rightMargin: 14
                anchors.verticalCenter: parent.verticalCenter
                visible: battery.have

                text: {
                    if (!battery.have)
                        return "";
                    const mark = battery.charging ? "+" : "";
                    // The time left, when the machine knows it and is running
                    // on it. A percentage answers "how full"; a train journey
                    // asks "how long", and they are not the same question on a
                    // battery that has aged.
                    const secs = batteryState.secsLeft;
                    if (!battery.charging && secs > 60) {
                        const h = Math.floor(secs / 3600);
                        const m = Math.floor((secs % 3600) / 60);
                        return mark + battery.pct + "%  " + h + "h" + (m < 10 ? "0" : "") + m;
                    }
                    return mark + battery.pct + "%";
                }
                // Loud below a fifth, which is where a decision has to be made
                // about the next hour rather than the next day.
                color: !battery.charging && battery.pct <= 20 ? "#e5484d" : "#c9ccd4"
                font.pixelSize: 13
                font.family: "monospace"
                textFormat: Text.PlainText
            }

            Text {
                id: clock

                anchors.right: parent.right
                anchors.rightMargin: 10
                anchors.verticalCenter: parent.verticalCenter

                color: "#c9ccd4"
                font.pixelSize: 13
                font.family: "monospace"
                textFormat: Text.PlainText

                property var now: new Date()
                text: Qt.formatDateTime(clock.now, "ddd d MMM  HH:mm")

                // Woken on the minute rather than on the second. A clock that
                // shows minutes has nothing to say 59 times out of 60, and
                // this is a project with a laptop profile on its roadmap.
                Timer {
                    id: tick

                    interval: 60000 - (new Date().getSeconds() * 1000 + new Date().getMilliseconds())
                    running: true
                    repeat: false
                    onTriggered: {
                        const d = new Date();
                        clock.now = d;
                        // Re-aimed at the next boundary each time, so it
                        // cannot drift into the middle of a minute and stay
                        // there.
                        tick.interval = 60000 - (d.getSeconds() * 1000 + d.getMilliseconds());
                        tick.restart();
                    }
                }
            }
        }
    }
}
