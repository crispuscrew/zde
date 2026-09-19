pragma ComponentBehavior: Bound

import QtQuick
import Quickshell.Wayland

FilteredListWindow {
    id: palette

    signal chosen(string name)
    function run() {
        palette.activate();
    }
    onActivated: entry => palette.chosen(entry.name)

    rowText: entry => entry.name + " " + entry.desc
    rowEnabled: entry => entry.live
    panelWidth: 680
    hintText: "enter run    ctrl+n/p move    esc close"
    WlrLayershell.namespace: "zde-palette"

    rowDelegate: Rectangle {
        id: row
        required property var modelData
        required property int index

        width: row.ListView.view.width
        height: 26
        radius: 4
        color: row.index === palette.index ? "#2a2c37" : "transparent"

        MouseArea {
            anchors.fill: parent
            onClicked: {
                palette.index = row.index;
                palette.run();
            }
        }

        Text {
            id: name
            anchors.left: parent.left
            anchors.leftMargin: 8
            anchors.verticalCenter: parent.verticalCenter
            width: 210
            text: row.modelData.name
            elide: Text.ElideRight
            color: row.modelData.live ? "#c9ccd4" : "#6a6f7a"
            font.pixelSize: 13
            font.family: "monospace"
            textFormat: Text.PlainText
        }

        Text {
            anchors.left: name.right
            anchors.leftMargin: 10
            anchors.right: key.left
            anchors.rightMargin: 10
            anchors.verticalCenter: parent.verticalCenter
            text: row.modelData.desc
            elide: Text.ElideRight
            color: row.modelData.live ? "#7a7f8a" : "#565b66"
            font.pixelSize: 12
            font.family: "monospace"
            textFormat: Text.PlainText
        }

        Text {
            id: key
            anchors.right: parent.right
            anchors.rightMargin: 8
            anchors.verticalCenter: parent.verticalCenter
            text: row.modelData.key !== "" ? row.modelData.key : "-"
            color: row.modelData.live ? "#7a7f8a" : "#565b66"
            font.pixelSize: 12
            font.family: "monospace"
            textFormat: Text.PlainText
        }
    }
}
