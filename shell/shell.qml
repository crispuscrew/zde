pragma ComponentBehavior: Bound

import QtQuick
import Quickshell

ShellRoot {
    id: root

    ShellSession {
        id: session
        onReceived: message => {
            if (message.event.kind === "zen")
                session.pollZen();
            else
                presenter.receive(message);
        }
    }
    ShellHardware { id: hardwareState }
    IdleState { id: idleReading }
    NetworkState {
        id: networkState
        eventChannel: session.channel
    }
    ShellSurfaces {
        id: desktopSurfaces
        channel: session.channel
    }
    SurfacePresenter {
        id: presenter
        surfaces: desktopSurfaces
        network: networkState
    }

    PickerIpc { surface: desktopSurfaces.picker }
    ActionIpc {
        palette: desktopSurfaces.palette
        powerMenu: desktopSurfaces.power
    }
    QueueIpc {
        state: session
        hardware: hardwareState
        network: networkState
        idleState: idleReading
    }

    Variants {
        model: Quickshell.screens
        StatusBar {
            id: bar
            required property var modelData
            screen: bar.modelData
            state: session
            hardware: hardwareState
            network: networkState
            idle: idleReading
        }
    }
}
