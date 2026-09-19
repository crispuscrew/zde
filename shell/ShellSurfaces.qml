pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Io

Scope {
    id: surfaces
    required property var channel
    property string pickMethod: "desk.switch"
    property alias picker: picker
    property alias center: center
    property alias clips: clips
    property alias popup: popup
    property alias ask: ask
    property alias palette: palette
    property alias power: power

    function acknowledge(token) {
        surfaces.channel.acknowledge(token);
    }

    // Capture the surface's generation when sending, not whichever surface is up later.
    function run(surface, request) {
        const token = surface.token;
        surfaces.channel.call(request, response => {
            if (surface.visible && surface.token === token)
                surface.ran(response.error ?? "");
        });
    }

    function notify(surface, request) {
        const token = surface.token;
        surfaces.channel.call(request, response => {
            if (response.error !== undefined && surface.visible && surface.token === token)
                surface.note = response.error;
        });
    }

    Process { id: leader }
    Picker {
        id: picker
        onChosen: key => {
            const token = picker.token;
            const accepted = surfaces.channel.call({method: surfaces.pickMethod, args: [key]}, response => {
                if (response.error !== undefined) {
                    if (picker.visible && picker.token === token)
                        picker.caption = response.error;
                    console.warn("zde: " + response.error);
                }
            });
            if (accepted)
                picker.hide();
        }
        onDismissed: picker.hide()
        onAction: name => {
            picker.hide();
            leader.command = ["zde", "system", name];
            leader.running = true;
        }
        onShown: token => surfaces.acknowledge(token)
    }
    NotifCenter {
        id: center
        onInvoke: (which, key) => surfaces.notify(center, {method: "attn.invoke", args: [which, key]})
        onDrop: which => surfaces.notify(center, {method: "queue.done", args: [which]})
        onDismissed: center.hide()
        onShown: token => surfaces.acknowledge(token)
    }
    ClipHistory {
        id: clips
        onChosen: entry => surfaces.run(clips, {method: "clip.history", args: [entry]})
        onDismissed: clips.hide()
        onShown: token => surfaces.acknowledge(token)
    }
    AttnPopup {
        id: popup
        onInvoke: (which, key) => surfaces.notify(popup, {method: "attn.invoke", args: [which, key]})
        onDrop: which => surfaces.notify(popup, {method: "queue.done", args: [which]})
        onShown: token => surfaces.acknowledge(token)
    }
    AskSurface {
        id: ask
        eventChannel: surfaces.channel
    }
    ActionPalette {
        id: palette
        onChosen: name => surfaces.run(palette, {method: "palette.run", args: [name]})
        onDismissed: palette.hide()
        onShown: token => surfaces.acknowledge(token)
    }
    PowerMenu {
        id: power
        onChosen: name => surfaces.run(power, {method: "system.power", args: [name]})
        onDismissed: power.hide()
        onShown: token => surfaces.acknowledge(token)
    }
}
