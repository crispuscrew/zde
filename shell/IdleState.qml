pragma ComponentBehavior: Bound

import QtQuick
import Quickshell

// logind's table cannot describe Wayland inhibitors held inside the compositor.
Scope {
    id: idle
    property bool known: false
    property int holds: 0

    function poll() {
        if (!channel.connected)
            return;
        channel.call({method: "system.idle"}, response => {
            idle.known = response.error === undefined && !!response.ok && response.ok.known === true;
            if (idle.known)
                idle.holds = response.ok.count ?? 0;
        });
    }

    RequestChannel {
        id: channel
        onConnectedChanged: {
            idle.known = false;
            if (channel.connected)
                idle.poll();
        }
    }
    Timer {
        interval: 5000
        running: true
        repeat: true
        onTriggered: idle.poll()
    }
}
