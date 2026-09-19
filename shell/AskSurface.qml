pragma ComponentBehavior: Bound

import QtQuick

AskWindow {
    id: surface
    required property var eventChannel
    onAsked: (tier, question, prior) => {
        channel.call({method: "ask.run", args: [tier, question].concat(prior)}, response => {
            // ask.text has no IDs: retire an unacknowledged stream before another ask.
            if (response.timeout)
                channel.restart(response.error);
            if (response.error !== undefined)
                surface.finished(response.error);
        });
    }
    onDismissed: surface.hide()
    onShown: token => surface.eventChannel.acknowledge(token)

    RequestChannel {
        id: channel
        onConnectedChanged: {
            if (!channel.connected)
                surface.finished("lost the connection to zded, so the answer stops there");
        }
        onReceived: message => {
            if (message.event.kind !== "ask.text")
                return;
            if (message.event.text)
                surface.chunk(message.event.text);
            if (message.event.done)
                surface.finished(message.event.error ?? "");
        }
    }
}
