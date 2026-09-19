# Development map

## Entry points

- `cmd/zde/main.go` handles process exit and filtered errors; `dispatch.go`
  selects a command. Feature files contain its socket calls and terminal output.
- `cmd/zded/main.go` handles flags and bounded shutdown. `run.go` orders startup,
  background workers and the final history snapshot. The compositor adapter
  opens a fresh niri connection per call so restarts do not leave a dead client.
- `shell/shell.qml` composes session state, hardware readings, surfaces, IPC
  controls and one status bar per screen.
- `nix/zde.nix`, `nix/zde-config.nix` and `nix/shell.nix` build the binaries,
  generated keymap/configuration and checked QML respectively.

## Daemon responsibilities

`internal/zded` remains one package. Files group responsibilities rather than
introducing another service or framework:

- `server.go` owns session state and injected integrations; `socket*.go`,
  `connections.go` and `sink*.go` enforce the user boundary and bounded I/O.
- `dispatch*.go` validates command arguments and routes requests.
- `desk_*.go` connects the pure desk model to niri, manifests, the journal and
  zinc launches.
- `attn*.go`, `arrivals.go` and `queue.go` coordinate notifications. `ask*.go`
  and `clip*.go` own the separate lifetimes of answers and clipboard contents.
- `surface.go` owns the acknowledgement handshake shared by all nine surface
  request handlers. Payload preparation and response shapes stay with callers.

A surface request registers its token before broadcasting. It reports success
only after a shell acknowledges that token, and removes the waiter on every
return. The 200 ms acknowledgement wait begins after a successful broadcast;
the broadcast has its own write bound. An unsolicited notification uses the
popup path and never waits for this handshake or takes keyboard focus.

## Socket protocol

The private Unix socket speaks one JSON object per line. Request IDs are
optional opaque strings:

```json
{"method":"attn.mode","id":"12"}
{"ok":{"mode":"work"},"id":"12"}
```

Every reply to a parsed request echoes its ID, including refusals and the
asynchronous clipboard result. IDs are captured per request, not stored as a
connection's current request. Empty or absent IDs preserve the legacy wire
shape. The Go CLI remains a sequential legacy client.

Events keep their existing `event` envelope and carry no request ID. An
`ask.run` acknowledgement echoes the ID; subsequent `ask.text` events belong to
the connection's one active answer. Malformed frames and connection-level
failures may have no ID.

`shell/RequestChannel.qml` owns reply callbacks by ID. Unknown or duplicate IDs
cannot finish another request. Pending calls are bounded at 128 and expire after
five seconds; callbacks and pending state are released on disconnect. Pending
records retain callbacks and deadlines, not request arguments or passwords.

On connection, a harmless `shown` request with an unknown token probes whether
IDs are echoed. An older daemon selects single-flight operation: a concurrent
call is refused as busy rather than queued or ambiguously matched. A timeout
on such a connection redials, so an old ID-less reply cannot answer a new call.
Surface acknowledgements are the exception: their public tokens wait in a
bounded queue and are sent as soon as the current reply releases the slot.
An ask acceptance timeout retires its connection before another question can
start, because answer-stream events themselves have no request IDs.

## Shell responsibilities

- `ShellSession.qml` polls queue, attention and zen state; `ShellHardware.qml`
  reads PipeWire and UPower. `NetworkState.qml` and `IdleState.qml` own their
  independent service readings and connections.
- `SurfacePresenter.qml` handles incoming surface events and keeps keyboard-
  grabbing overlays exclusive. Passive notification popups are the exception.
- `ShellSurfaces.qml` connects each choice to its own response callback. A
  surface token also identifies its opening, so late replies cannot act on a
  reopened surface. `AskSurface.qml` owns the answer-stream connection.
- `FilteredListWindow.qml` and `FilteredListPanel.qml` share the palette and
  clipboard list interaction. Their adapters supply rows and actions. Clipboard
  previews and filter text are cleared when the surface hides.
- `StatusBar.qml` and `StatusIndicators.qml` draw readings; `*Ipc.qml` exposes
  the existing inspection/control methods used by smoke tests.

All displayed external text is plain text. Keep the bounded writes, request
validation, private-file handling, process-group cleanup and clipboard erasure
when changing these paths. Tests preserve these contracts; historical debugging
transcripts remain in Git rather than being repeated beside every caller.

## Checks

From the repository root:

```sh
go test -race ./...
go vet ./...
go build ./...
gofmt -l cmd internal
nix flake check --print-build-logs
nix develop --command statix check .
git ls-files -z '*.nix' | xargs -0 nix develop --command nixfmt --check
nix build --print-build-logs .#zde-smoke
```

`nix build .#zde-shell` runs strict qmllint and real Qt request-channel tests.
Those tests use a private fake socket and an offscreen Quickshell process; they
cover reordered replies, events, legacy single-flight, disconnect cleanup,
the pending limit, timeouts and late replies. Missing Quickshell fails this
Nix gate. A plain Go run skips only those Qt tests if Quickshell is unavailable.

The Go wiring test follows statically instantiated local QML components from
`shell.qml`, including inheritance. Unused components are excluded. This scan
checks protocol names; it does not substitute for QML loading or the VM test.

The QEMU smoke test needs KVM. Hardware-dependent verification remains in
[`verify.md`](verify.md), including real input, GPU and multi-monitor behavior.
