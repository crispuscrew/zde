// One connection to zded, dialled again as many times as it takes.
//
// This is a workaround for a defect in quickshell 0.3.0, which is what
// `Socket` is (src/io/socket.cpp in the quickshell source). Redialling the
// obvious way - writing `connected = true` on a socket that is down - works
// only while the dial before it failed in one particular way, and the first
// time it does not, the bar goes blind until the session is restarted. The
// mechanism, written down because it is somebody else's code and a workaround
// whose reason is not recorded gets deleted as redundant:
//
//   Socket::connectPathSocket() puts the new QLocalSocket in `this->socket`
//   before it calls connectToServer, so the pointer is set whether or not the
//   dial works. Socket::onSocketError() logs the failure and emits `error`,
//   and clears nothing. Socket::onSocketDisconnected() is the only thing that
//   clears `this->socket`, and a socket that never connected never emits
//   disconnected. Socket::setConnected(true) dials only
//   `if (this->socket == nullptr)`.
//
// So a single dial into the moment when zded has deleted its socket and not
// yet recreated it - which is every home-manager switch, because they all
// restart zded.service - leaves a dead QLocalSocket behind a non-null pointer,
// and every `connected = true` after it is a no-op. That dial is the last one
// there will ever be: the bar keeps running, keeps answering its own IPC, and
// never reaches the daemon again. CI caught it as PeerClosedError when zded
// stopped, ServerNotFoundError when the redial hit the gap, and nothing after.
//
// Driving `connected` false and then true does not help, and that is worth
// recording because it is the fix that suggests itself. setConnected(false)
// calls QLocalSocket::disconnectFromServer(), which on Unix (qtbase,
// qlocalsocket_unix.cpp) forwards to QAbstractSocket::disconnectFromHost(),
// and that returns immediately for a socket in UnconnectedState - which is
// exactly what a failed dial leaves behind, since
// QLocalSocketPrivate::setErrorAndEmit() sets UnconnectedState and emits
// errorOccurred and never disconnected. No disconnected signal means
// onSocketDisconnected() still does not run, the pointer is still not cleared,
// and quickshell's own `disconnecting` flag is left true for good into the
// bargain, which poisons setPath as well.
//
// So this throws the socket object away and makes a new one. A pointer this
// shell does not own and cannot inspect is then never something it has to
// reason about: every dial starts from an object that has never dialled.
// Destroying it takes the QLocalSocket with it, because Socket::setSocket()
// parents the QLocalSocket to the Socket, so no descriptor is left behind.
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell.Io

QtObject {
    id: dialer

    // Where zded listens. Read when a socket is made, so changing it takes
    // effect on the next dial rather than on the connection that is up.
    property string path: ""

    // Whether there is a connection to write down - the same question
    // Socket.connected answers, asked of whichever socket is current, so that
    // everything reading this reads it the way it read the socket's own.
    //
    // For everyone outside this file. Inside it, live() is the one to ask:
    // this is a binding, and a binding is re-evaluated after the handlers on
    // the signal that invalidated it have run, so from inside one of those
    // handlers this still reads what was true a moment ago. That is not
    // hypothetical - it is how the first draft of this file managed to stop
    // its own retry on the way down and never dial again.
    readonly property bool connected: dialer.sock !== null && dialer.sock.connected

    // The same question asked of the socket rather than of a binding, so the
    // answer is never one signal out of date.
    function live() {
        return dialer.sock !== null && dialer.sock.connected;
    }

    // A line zded sent, one per delimiter, exactly as SplitParser hands it
    // over. The parser lives in here because it belongs to a socket that gets
    // replaced, and nothing outside should have to be replaced with it.
    signal heard(string line)

    // The socket of the moment, or null before the first dial. Not private,
    // because a QtObject has no way to make it so; nothing outside this file
    // should touch it.
    property Socket sock: null

    // The backoff, and the whole of the bound on redialling: a second after a
    // dial that failed, doubling to a ceiling of eight. So a zded that is not
    // coming back costs one socket object every eight seconds rather than a
    // spin, and a zded that does come back is noticed within eight seconds at
    // the very worst. A dial that connects puts it back to a second.
    readonly property int firstWait: 1000
    readonly property int longestWait: 8000
    property int wait: dialer.firstWait

    // Write a line, if there is a connection to write it down. Silent when
    // there is not, for the same reason Socket.write is: every caller here
    // already asks `connected` first and says its own piece about the answer.
    function write(text) {
        if (dialer.live())
            dialer.sock.write(text);
    }

    // Throw away whatever socket there is, and dial on a new one.
    function dial() {
        // Cleared before the new one is built, because the new one connects
        // and reports itself inside createObject below: settled() tells the
        // socket that matters from one that does not by comparing against
        // this, and it has to come up empty for both of these.
        const dead = dialer.sock;
        dialer.sock = null;
        dialer.sock = dialer.maker.createObject(dialer);
        // Armed before the dial can fail, because a dial that fails says
        // nothing about `connected` - it emits `error` and stops there - so
        // this timer is the only thing that will ever notice.
        dialer.retry.interval = dialer.wait;
        dialer.retry.restart();
        dialer.wait = Math.min(dialer.wait * 2, dialer.longestWait);
        // Last of all, so that a socket which will not go quietly cannot take
        // the dial replacing it down with it.
        if (dead !== null)
            dead.destroy();
    }

    // What a socket says about itself. Connecting to a unix socket finishes
    // inside connectToServer, so the socket dial() is building reports itself
    // connected before createObject has returned and before `sock` names it -
    // which is why this compares, and why the timer rather than this is what
    // confirms a dial that worked.
    function settled(s) {
        if (s !== dialer.sock)
            return;
        if (s.connected) {
            dialer.retry.stop();
            dialer.wait = dialer.firstWait;
            return;
        }
        // The daemon went away. Dial again a second from now rather than
        // after however long the last backoff had grown to: this is a
        // connection that was working, so the next one probably will too.
        dialer.wait = dialer.firstWait;
        dialer.retry.interval = dialer.wait;
        dialer.retry.restart();
    }

    // Single-shot on purpose: it is armed by the code that has just made a
    // dial or just lost one, and by nothing else, so there is no arrangement
    // of failures that can leave two of these in flight.
    readonly property Timer retry: Timer {
        repeat: false
        onTriggered: {
            if (dialer.live()) {
                // The dial this was armed for worked after all, so the next
                // failure starts the backoff from the floor.
                dialer.wait = dialer.firstWait;
                return;
            }
            dialer.dial();
        }
    }

    // A property and not a child, because QtObject has no default property to
    // put one in - and QtObject rather than Item because none of this is on
    // the screen.
    readonly property Component maker: Component {
        Socket {
            id: dialed

            path: dialer.path
            parser: SplitParser {
                onRead: line => dialer.heard(line)
            }
            // Last, so that the path is set and the parser is in place before
            // this dials - which it does before the line below has run.
            connected: true

            onConnectionStateChanged: dialer.settled(dialed)
        }
    }

    Component.onCompleted: dialer.dial()
}
