pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Services.Pipewire
import Quickshell.Services.UPower

Scope {
    readonly property alias battery: batteryState
    readonly property alias mic: micState

    // Share one reading between the bar and IPC.
    QtObject {
        id: batteryState
        readonly property var dev: UPower.displayDevice
        readonly property bool have: batteryState.dev !== null && batteryState.dev.isLaptopBattery
        readonly property int pct: batteryState.have ? Math.round(batteryState.dev.percentage * 100) : 0
        readonly property bool charging: batteryState.have
            && (batteryState.dev.state === UPowerDeviceState.Charging
                || batteryState.dev.state === UPowerDeviceState.FullyCharged)
        readonly property int secsLeft: batteryState.have ? batteryState.dev.timeToEmpty : 0
    }

    // Match the default source toggled by Mod+Ctrl+m, not an arbitrary microphone.
    QtObject {
        id: micState
        readonly property var src: Pipewire.defaultAudioSource
        readonly property bool known: Pipewire.ready
        readonly property bool have: micState.known && micState.src !== null
        readonly property bool muted: micState.have
            && micState.src.audio !== null
            && micState.src.audio.muted
        // A paused capture still holds the mic; count holders rather than bytes.
        readonly property bool live: micState.have && micLinks.linkGroups.length > 0
    }

    // Muted is only populated after the default source has been tracked.
    PwObjectTracker {
        objects: [Pipewire.defaultAudioSource]
    }

    // This tracker excludes monitor links from the capture holders.
    PwNodeLinkTracker {
        id: micLinks
        node: Pipewire.defaultAudioSource
    }
}
