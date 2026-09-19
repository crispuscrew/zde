pragma ComponentBehavior: Bound

import QtQuick

Rectangle {
    id: panel
    required property var controller
    property alias filter: query.text

    function focusQuery() {
        query.forceActiveFocus();
    }

    function position(index, mode) {
        list.positionViewAtIndex(index, mode);
    }

    color: "#11121a"
    border.color: "#2a2c37"
    border.width: 1
    radius: 6
    clip: true

    // Consume panel clicks before they reach the dismissing backdrop.
    MouseArea {
        anchors.fill: parent
    }

    TextInput {
        id: query
        anchors.top: parent.top
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.margins: 12
        height: 20
        focus: true
        color: "#c9ccd4"
        font.pixelSize: 14
        font.family: "monospace"
        cursorVisible: panel.controller.visible

        onTextChanged: {
            panel.controller.index = 0;
            panel.controller.failed = "";
            list.positionViewAtIndex(0, ListView.Beginning);
        }
        Keys.onPressed: event => {
            switch (event.key) {
            case Qt.Key_Escape:
                panel.controller.dismissed();
                break;
            case Qt.Key_Return:
            case Qt.Key_Enter:
                panel.controller.activate();
                break;
            case Qt.Key_Down:
                panel.controller.step(1);
                break;
            case Qt.Key_Up:
                panel.controller.step(-1);
                break;
            default:
                // j/k remain filter text, unlike the non-filtered Picker.
                if ((event.modifiers & Qt.ControlModifier) && event.key === Qt.Key_N)
                    panel.controller.step(1);
                else if ((event.modifiers & Qt.ControlModifier) && event.key === Qt.Key_P)
                    panel.controller.step(-1);
                else
                    return;
            }
            event.accepted = true;
        }

        Text {
            anchors.fill: parent
            visible: query.text === ""
            text: "type to filter"
            color: "#5a5f6a"
            font.pixelSize: 14
            font.family: "monospace"
            textFormat: Text.PlainText
        }
    }

    Text {
        id: hint
        anchors.bottom: parent.bottom
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.margins: 12
        elide: Text.ElideRight
        text: panel.controller.failed || panel.controller.reason || panel.controller.hintText
        color: panel.controller.failed !== "" ? "#e5484d"
            : panel.controller.reason !== "" ? "#e5a23d" : "#7a7f8a"
        font.pixelSize: 11
        font.family: "monospace"
        textFormat: Text.PlainText
    }

    ListView {
        id: list
        anchors.top: query.bottom
        anchors.bottom: hint.top
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.margins: 12
        spacing: 2
        clip: true
        model: panel.controller.matches
        delegate: panel.controller.rowDelegate
    }
}
