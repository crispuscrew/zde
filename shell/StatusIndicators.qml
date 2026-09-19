pragma ComponentBehavior: Bound

import QtQuick

Item {
    id: indicators
    required property var hardware
    required property var network
    required property var idle

    // Alerts grow leftward so idle holders cannot move the microphone indicator.
    Text {
        id: mic
        anchors.right: netText.left
        anchors.rightMargin: 14
        anchors.verticalCenter: parent.verticalCenter
        visible: mic.text !== ""
        text: indicators.hardware.mic.muted ? "mic muted" : (indicators.hardware.mic.live ? "mic live" : "")
        color: indicators.hardware.mic.muted ? "#7a7f8a" : "#e5484d"
        font.pixelSize: 13
        font.family: "monospace"
        textFormat: Text.PlainText
    }

    Text {
        id: idleHold
        anchors.right: mic.left
        anchors.rightMargin: mic.visible ? 14 : 0
        anchors.verticalCenter: parent.verticalCenter
        visible: idleHold.text !== ""
        // Only logind holders are observable here, not Wayland idle inhibitors.
        text: indicators.idle.known && indicators.idle.holds > 0 ? "idle held" : ""
        color: "#e5a23d"
        font.pixelSize: 13
        font.family: "monospace"
        textFormat: Text.PlainText
    }

    Text {
        id: netText
        anchors.right: battery.left
        anchors.rightMargin: 14
        anchors.verticalCenter: parent.verticalCenter
        text: {
            if (!indicators.network.known)
                return "net: unknown";
            if (indicators.network.killed)
                return "net: cut";
            switch (indicators.network.kind) {
            case "wifi":
                return (indicators.network.ssid === "" ? "wifi" : indicators.network.ssid) + "  " + indicators.network.strength + "%";
            case "wired":
                return "wired";
            case "absent":
                return "net: no manager";
            }
            return "no network";
        }
        color: {
            if (indicators.network.killed)
                return "#e8b44a";
            return indicators.network.known && indicators.network.kind !== "absent" ? "#c9ccd4" : "#7a7f8a";
        }
        font.pixelSize: 13
        font.family: "monospace"
        textFormat: Text.PlainText
    }

    Text {
        id: battery
        readonly property bool have: indicators.hardware.battery.have
        readonly property int pct: indicators.hardware.battery.pct
        readonly property bool charging: indicators.hardware.battery.charging
        anchors.right: clock.left
        anchors.rightMargin: 14
        anchors.verticalCenter: parent.verticalCenter
        visible: battery.have
        text: {
            if (!battery.have)
                return "";
            const mark = battery.charging ? "+" : "";
            const secs = indicators.hardware.battery.secsLeft;
            if (!battery.charging && secs > 60) {
                const hours = Math.floor(secs / 3600);
                const minutes = Math.floor((secs % 3600) / 60);
                return mark + battery.pct + "%  " + hours + "h" + (minutes < 10 ? "0" : "") + minutes;
            }
            return mark + battery.pct + "%";
        }
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

        // Wake on each minute boundary and re-aim so the clock cannot drift.
        Timer {
            id: tick
            interval: 60000 - (new Date().getSeconds() * 1000 + new Date().getMilliseconds())
            running: true
            repeat: false
            onTriggered: {
                const now = new Date();
                clock.now = now;
                tick.interval = 60000 - (now.getSeconds() * 1000 + now.getMilliseconds());
                tick.restart();
            }
        }
    }
}
