pragma ComponentBehavior: Bound

import QtQuick
import Quickshell.Wayland

FilteredListWindow {
    id: clips

    signal chosen(string entry)
    function put() {
        clips.activate();
    }
    onActivated: entry => clips.chosen(String(entry.id))

    // Only previews cross into the shell; retain neither those nor the query on hide.
    rowText: entry => entry.preview ?? ""
    rowEnabled: entry => entry.kind === "text"
    clearOnHide: true
    panelWidth: 760
    hintText: clips.rows.length === 0
        ? "nothing copied recently: entries expire, and secrets are never recorded"
        : "enter copy    ctrl+n/p move    esc close"
    WlrLayershell.namespace: "zde-clip"

    rowDelegate: Rectangle {
        id: row
        required property var modelData
        required property int index
        readonly property bool putBack: row.modelData.kind === "text"

        width: row.ListView.view.width
        height: 26
        radius: 4
        color: row.index === clips.index ? "#2a2c37" : "transparent"

        MouseArea {
            anchors.fill: parent
            onClicked: {
                clips.index = row.index;
                clips.put();
            }
        }

        Text {
            anchors.left: parent.left
            anchors.leftMargin: 8
            anchors.right: when.left
            anchors.rightMargin: 10
            anchors.verticalCenter: parent.verticalCenter
            text: row.putBack
                ? (row.modelData.preview ?? "") + (row.modelData.cut ? " ..." : "")
                : row.modelData.why ?? ""
            elide: Text.ElideRight
            color: row.putBack ? "#c9ccd4" : "#6a6f7a"
            font.pixelSize: 13
            font.family: "monospace"
            textFormat: Text.PlainText
        }

        Text {
            id: when
            anchors.right: parent.right
            anchors.rightMargin: 8
            anchors.verticalCenter: parent.verticalCenter
            text: Qt.formatDateTime(new Date(row.modelData.at), "HH:mm")
            color: "#7a7f8a"
            font.pixelSize: 12
            font.family: "monospace"
            textFormat: Text.PlainText
        }
    }
}
