pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Wayland

// qmllint disable uncreatable-type
// Quickshell provides the platform implementation of PanelWindow.
PanelWindow {
    id: surface

    required property var rowText
    required property var rowEnabled
    required property Component rowDelegate
    required property int panelWidth
    required property string hintText
    property bool clearOnHide: false
    property var rows: []
    property int index: 0
    property string token: ""
    property bool running: false
    property string failed: ""
    signal activated(var entry)
    signal dismissed
    signal shown(string token)

    readonly property var matches: {
        const query = panel.filter.toLowerCase();
        return query === "" ? surface.rows
            : surface.rows.filter(entry => surface.rowText(entry).toLowerCase().indexOf(query) >= 0);
    }

    readonly property string reason: {
        const entry = surface.matches[surface.index];
        return entry && !surface.rowEnabled(entry) ? (entry.why ?? "") : "";
    }

    // ListView rewrites currentIndex when filtering; keep our selection separate.
    onIndexChanged: panel.position(surface.index, ListView.Contain)

    function show(newRows, token) {
        const wasUp = surface.visible;
        surface.rows = newRows;
        surface.token = token ?? "";
        panel.filter = "";
        surface.index = 0;
        surface.running = false;
        surface.failed = "";
        surface.visible = true;
        if (wasUp && surface.token !== "")
            surface.shown(surface.token);
    }

    onVisibleChanged: {
        if (surface.visible) {
            panel.focusQuery();
            if (surface.token !== "")
                surface.shown(surface.token);
        } else if (surface.clearOnHide) {
            surface.clear();
        }
    }

    function clear() {
        surface.rows = [];
        panel.filter = "";
        surface.index = 0;
        surface.failed = "";
    }

    function hide() {
        surface.visible = false;
        if (surface.clearOnHide)
            surface.clear();
    }

    function step(by) {
        const count = surface.matches.length;
        if (count === 0)
            return;
        surface.failed = "";
        surface.index = (surface.index + by + count) % count;
    }

    function activate() {
        if (surface.running)
            return;
        const entry = surface.matches[surface.index];
        if (!entry || !surface.rowEnabled(entry))
            return;
        surface.failed = "";
        surface.running = true;
        surface.activated(entry);
    }

    function ran(error) {
        surface.running = false;
        if (error === "")
            surface.hide();
        else
            surface.failed = error;
    }

    function narrow(text) {
        panel.filter = text;
    }

    visible: false
    color: "transparent"
    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.keyboardFocus: surface.visible ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None
    anchors {
        top: true
        bottom: true
        left: true
        right: true
    }
    // qmllint enable uncreatable-type

    Rectangle {
        anchors.fill: parent
        color: "#000000"
        opacity: 0.35
        MouseArea {
            anchors.fill: parent
            onClicked: surface.dismissed()
        }
    }

    FilteredListPanel {
        id: panel
        controller: surface
        anchors.centerIn: parent
        width: surface.panelWidth
        height: Math.min(84 + surface.matches.length * 28, surface.height - 80)
    }
}
