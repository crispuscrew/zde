pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// Quickshell provides the platform implementation of PanelWindow.
PanelWindow {
    id: bar
    required property var state
    required property var hardware
    required property var network
    required property var idle

    color: "#11121a"
    implicitHeight: 26
    WlrLayershell.namespace: "zde-bar"
    anchors {
        top: true
        left: true
        right: true
    }
    exclusiveZone: bar.implicitHeight
    // Unmap in zen to release reserved space, but never hide a live microphone.
    // Unknown zen state fails toward showing the bar.
    visible: !bar.state.zen || !bar.state.zenKnown || bar.hardware.mic.live
    // qmllint enable uncreatable-type

    Component.onCompleted: {
        bar.state.barHeight = bar.height;
        bar.state.barZone = bar.exclusiveZone;
    }

    Text {
        id: queueLine
        anchors.left: parent.left
        anchors.leftMargin: 10
        anchors.verticalCenter: parent.verticalCenter
        text: {
            if (!bar.state.linked)
                return "zded: not answering";
            if (!bar.state.known)
                return "queue: unknown";
            if (bar.state.queued === 0)
                return "queue empty";
            const waiting = bar.state.queued === 1 ? "1 waiting" : bar.state.queued + " waiting";
            return bar.state.urgent > 0 ? waiting + "  !" + bar.state.urgent : waiting;
        }
        color: bar.state.urgent > 0 ? "#e5484d" : (bar.state.linked && bar.state.known ? "#c9ccd4" : "#7a7f8a")
        font.pixelSize: 13
        font.family: "monospace"
        textFormat: Text.PlainText
    }

    Text {
        anchors.left: queueLine.right
        anchors.leftMargin: 16
        anchors.verticalCenter: parent.verticalCenter
        visible: bar.state.linked && bar.state.modeKnown
        text: "attn " + bar.state.mode
        color: bar.state.mode === "work" ? "#7a7f8a" : "#e5a23d"
        font.pixelSize: 13
        font.family: "monospace"
        textFormat: Text.PlainText
    }

    StatusIndicators {
        anchors.fill: parent
        hardware: bar.hardware
        network: bar.network
        idle: bar.idle
    }
}
