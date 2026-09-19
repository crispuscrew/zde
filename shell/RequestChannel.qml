pragma ComponentBehavior: Bound

import QtQuick
import Quickshell

// Callbacks retain reply ownership, never request arguments (which may be secrets).
Scope {
    id: channel
    readonly property bool connected: wire.connected && channel.ready
    property bool ready: false
    property bool legacy: false
    property double sequence: 0
    property var pending: ({})
    property var acknowledgements: []
    property double probingUntil: 0
    readonly property int replyWait: 5000
    readonly property int pendingMax: 128
    signal received(var message)

    // Only public shown tokens can wait; ordinary requests never retain arguments.
    function acknowledge(token) {
        if (!channel.connected || channel.acknowledgements.length >= channel.pendingMax)
            return false;
        channel.acknowledgements.push(token);
        channel.flushAcknowledgements();
        return true;
    }

    function flushAcknowledgements() {
        while (channel.connected && channel.acknowledgements.length > 0) {
            if (Object.keys(channel.pending).length >= (channel.legacy ? 1 : channel.pendingMax))
                return;
            channel.call({method: "shown", args: [channel.acknowledgements.shift()]});
        }
    }

    function call(request, complete) {
        const keys = Object.keys(channel.pending);
        if (!channel.connected || keys.length >= channel.pendingMax || (channel.legacy && keys.length > 0)) {
            if (complete)
                complete({error: !channel.connected ? "no connection to zded" : "zded is busy; try again"});
            return false;
        }
        const identity = String(++channel.sequence);
        channel.pending[identity] = {complete: complete, until: Date.now() + channel.replyWait};
        wire.write(JSON.stringify(Object.assign({}, request, {id: identity})) + "\n");
        return true;
    }

    function fail(message) {
        channel.ready = false;
        const waiting = channel.pending;
        channel.pending = ({});
        channel.acknowledgements = [];
        for (const identity of Object.keys(waiting)) {
            if (waiting[identity].complete)
                waiting[identity].complete({error: message});
        }
    }

    function restart(message) {
        channel.fail(message);
        wire.dial();
    }

    function accept(line) {
        let message;
        try {
            message = JSON.parse(line);
        } catch (error) {
            channel.restart("zded sent an unreadable reply");
            return;
        }
        if (!message || typeof message !== "object") {
            channel.restart("zded sent an unreadable reply");
            return;
        }
        if (message.event) {
            channel.received(message);
            return;
        }
        if (!channel.ready) {
            if (message.ok === undefined || (message.id !== undefined && message.id !== "probe")) {
                channel.restart(message.error ?? "zded did not answer the protocol probe");
                return;
            }
            channel.legacy = message.id === undefined;
            channel.ready = true;
            return;
        }
        let identity = message.id;
        if (channel.legacy) {
            // Only one request may be outstanding on an older daemon.
            identity = Object.keys(channel.pending)[0];
        } else if (typeof identity !== "string") {
            channel.restart(message.error ?? "zded sent a reply without its request ID");
            return;
        }
        const waiting = channel.pending[identity];
        if (!waiting)
            return; // Timed-out, duplicate and unknown replies cannot finish another call.
        delete channel.pending[identity];
        channel.flushAcknowledgements();
        if (waiting.complete)
            waiting.complete(message);
    }

    Dialer {
        id: wire
        path: Quickshell.env("XDG_RUNTIME_DIR") + "/zde/zded.sock"
        onConnectedChanged: {
            channel.fail("lost the connection to zded");
            if (wire.connected) {
                channel.probingUntil = Date.now() + channel.replyWait;
                // Unknown shown tokens are harmless on both protocol versions.
                wire.write('{"method":"shown","args":[""],"id":"probe"}\n');
            }
        }
        onHeard: line => channel.accept(line)
    }

    Timer {
        interval: 250
        running: wire.connected
        repeat: true
        onTriggered: {
            const now = Date.now();
            if (!channel.ready) {
                if (now >= channel.probingUntil)
                    channel.restart("zded did not answer the protocol probe");
                return;
            }
            for (const identity of Object.keys(channel.pending)) {
                const waiting = channel.pending[identity];
                if (!waiting || now < waiting.until)
                    continue;
                if (channel.legacy) {
                    // A late ID-less reply must never be mistaken for the next call.
                    channel.restart("zded did not answer in time");
                    return;
                }
                delete channel.pending[identity];
                if (waiting.complete)
                    waiting.complete({error: "zded did not answer in time", timeout: true});
            }
            channel.flushAcknowledgements();
        }
    }
}
