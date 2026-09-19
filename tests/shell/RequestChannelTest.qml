pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import "../../shell" as Desktop

ShellRoot {
    id: test
    readonly property string scenario: Quickshell.env("ZDE_CHANNEL_SCENARIO")
    property alias channel: wire
    property bool started: false
    property bool failed: false

    function check(condition, message) {
        if (!condition && !test.failed) {
            test.failed = true;
            console.error("ZDE_CHANNEL_FAIL:" + test.scenario + " " + message);
            Qt.callLater(Qt.quit);
        }
        return condition;
    }

    function finish() {
        test.check(wire.acknowledgements.length === 0, "acknowledgement queue leaked");
        if (!test.failed && test.check(Object.keys(wire.pending).length === 0, "pending callbacks leaked")) {
            console.log("ZDE_CHANNEL_PASS:" + test.scenario);
            Qt.quit();
        }
    }

    Desktop.RequestChannel {
        id: wire
        onConnectedChanged: {
            if (test.scenario === "restart" || test.scenario === "retired-stream") {
                restarting.connected();
                return;
            }
            if (!wire.connected || test.started)
                return;
            test.started = true;
            if (test.scenario === "modern")
                modern.start();
            else if (test.scenario === "legacy")
                legacy.start();
            else if (test.scenario === "legacy-ack")
                acknowledgements.start();
            else if (test.scenario === "disconnect")
                lifecycle.disconnect();
            else if (test.scenario === "timeout")
                lifecycle.timeout();
            else
                test.check(false, "unknown scenario");
        }
        onReceived: message => {
            if (test.scenario === "legacy-ack")
                acknowledgements.receive(message);
            else if (test.scenario === "restart" || test.scenario === "retired-stream")
                restarting.receive(message);
            else
                modern.receive(message);
        }
    }
    ChannelModern { id: modern; harness: test }
    ChannelLegacy { id: legacy; harness: test }
    ChannelLifecycle { id: lifecycle; harness: test }
    ChannelAcknowledgements { id: acknowledgements; harness: test }
    ChannelRestart { id: restarting; harness: test }

    Timer {
        interval: 15000
        running: true
        onTriggered: test.check(false, "fixture timed out")
    }
}
