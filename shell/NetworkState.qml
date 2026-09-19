pragma ComponentBehavior: Bound

import QtQuick
import Quickshell

Scope {
    id: network
    required property var eventChannel
    property alias surface: connections
    property string kind: ""
    property string ssid: ""
    property int strength: 0
    property bool killed: false
    property bool known: false
    property int waiting: 0

    function poll() {
        if (!channel.connected)
            return;
        if (network.waiting >= 2)
            network.known = false;
        network.waiting += 1;
        channel.call({method: "net.status"}, response => {
            network.waiting = 0;
            network.known = response.error === undefined && !!response.ok;
            if (!network.known)
                return;
            network.kind = response.ok.kind ?? "";
            network.ssid = response.ok.ssid ?? "";
            network.strength = response.ok.signal ?? 0;
            network.killed = response.ok.killed === true;
            if (connections.visible)
                connections.link = response.ok;
        });
    }

    // Only the network name is captured; the password is never retained in pending state.
    function replyFor(method, about) {
        const token = connections.token;
        return response => {
            if (connections.visible && connections.token === token) {
                connections.said = response.error ?? String(response.ok);
                if (response.error === undefined && method === "net.forget")
                    connections.forgotten(about);
            }
            if (channel.connected)
                network.poll();
        };
    }

    function ask(method, args) {
        return channel.call({method: method, args: args}, network.replyFor(method, args[0] ?? ""));
    }

    RequestChannel {
        id: channel
        onConnectedChanged: {
            network.waiting = 0;
            network.known = false;
            if (channel.connected)
                network.poll();
        }
    }

    Timer {
        interval: 5000
        running: true
        repeat: true
        onTriggered: network.poll()
    }

    Links {
        id: connections
        onJoin: (ssid, secret) => network.ask("net.connect", secret === "" ? [ssid] : [ssid, secret])
        onDropped: network.ask("net.disconnect", [])
        onForget: ssid => network.ask("net.forget", [ssid])
        onDismissed: connections.hide()
        onShown: token => network.eventChannel.acknowledge(token)
    }
}
