pragma ComponentBehavior: Bound

import QtQuick
import Quickshell

Scope {
    id: session
    property int queued: 0
    property int urgent: 0
    readonly property bool linked: queueLink.connected
    property bool known: false
    property string mode: ""
    property bool modeKnown: false
    property bool zen: false
    property bool zenKnown: false
    property int barHeight: 0
    property int barZone: 0
    property int waiting: 0
    property int streamWaiting: 0
    property alias channel: stream
    signal received(var message)

    function pollQueue() {
        if (!queueLink.connected)
            return;
        if (session.waiting >= 2)
            session.known = false;
        session.waiting += 1;
        queueLink.call({method: "queue.list"}, response => {
            session.waiting = 0;
            session.known = response.error === undefined;
            if (session.known) {
                const items = response.ok || [];
                session.queued = items.length;
                session.urgent = items.filter(entry => entry.urgent === true).length;
            }
        });
    }

    function pollZen() {
        stream.call({method: "desk.zen"}, response => {
            session.zenKnown = response.error === undefined && !!response.ok;
            if (session.zenKnown)
                session.zen = response.ok.zen === true;
        });
    }

    function pollState() {
        if (!stream.connected)
            return;
        if (session.streamWaiting >= 2) {
            session.modeKnown = false;
            session.zenKnown = false;
        }
        session.streamWaiting += 1;
        stream.call({method: "attn.mode"}, response => {
            session.streamWaiting = 0;
            session.modeKnown = response.error === undefined && !!response.ok;
            if (session.modeKnown)
                session.mode = response.ok.mode;
            // Sequential for legacy daemons, so each ID-less reply has one owner.
            if (stream.connected)
                session.pollZen();
        });
    }

    RequestChannel {
        id: queueLink
        onConnectedChanged: {
            session.waiting = 0;
            session.known = false;
            if (queueLink.connected)
                session.pollQueue();
        }
    }

    RequestChannel {
        id: stream
        onConnectedChanged: {
            session.streamWaiting = 0;
            session.modeKnown = false;
            session.zenKnown = false;
            if (stream.connected) {
                stream.call({method: "events"}, response => {
                    if (response.error !== undefined)
                        console.warn("zde: " + response.error);
                    if (stream.connected)
                        session.pollState();
                });
            }
        }
        onReceived: message => session.received(message)
    }

    Timer {
        interval: 2000
        running: true
        repeat: true
        onTriggered: {
            session.pollQueue();
            session.pollState();
        }
    }
}
