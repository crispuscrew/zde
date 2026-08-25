# zde - Vision

Frozen 2026-07-22. Terms: [`glossary.md`](glossary.md). Model:
[`model.md`](model.md). Phases: [`roadmap.md`](roadmap.md). What zde is: the
[`README`](../README.md). Priority order, binding: **Stable, then Secure,
then Beautiful.**

## 1. Principles

1. **Containers-first, minimal host surface.** On the host only what must
   be: niri, the input daemon (evdev), the shell (layer-shell is
   privileged), zcr with podman, zded. Everything else is a zinc container.
   Host pieces install through the flake: the ones that need root - niri's
   session, greetd, portals, podman - in the system module, the rest in the
   home-manager one (delivery.md, layers 0 and 1).
2. **The keybind doctrine.** One scheme file generates the niri binds, the
   cheatsheet, and the overlay: no drift. Binds are grabbed at the
   compositor, so sandboxed apps can neither shadow nor observe them. Super
   belongs to the DE. A desk may release clashing binds, except panic, lock,
   mode exit. The input layer remaps any evdev device. Actions are the
   contract; keys are assigned once usage shows the frequencies. Modes:
   Normal, Window, Kb-mouse, One-hand, Passthrough, visible in the bar.
3. **Display policy, never data policy.** Notifications always land in
   history, timestamped, with what was sent kept to its own bound: 300
   characters of summary, 4000 of body, and an app that puts everything in the
   body keeps a body. The bounds are there because a notification is
   attacker-controlled and is what sizes the history; they are the only thing
   that ever shortens a record, and no mode decides what is kept. Quiet and
   focus modes stop popups only; review happens in the notification center.
4. **Visible state.** Whatever a keypress depends on is on the bar: media
   target, mic, mode, queue depth, mic/camera holders, net-kill.
5. **Two-lane secrets.** The clipboard has history; secrets never enter it.
   pass types them; an unavoidable clipboard entry carries the sensitive
   hint plus a 30 s wipe, honored by the history daemon.
6. **Attribution by channel, not claim.** A sender is known by the
   per-instance DBus socket it arrived on (host processes: peer creds).
   Unforgeable; env tags are display only.
7. **The zded boundary.** The zded socket is never mounted into a container;
   apps reach zde only via the notification bus and declared grants.
8. **Reconcile, not resurrection.** GUI apps die with the compositor;
   containers and tmux survive. Restore: zded diffs manifests, `zcr ps`, and
   niri reality, then converges. Snapshot is the same mechanism, manual.
9. **Fail-closed, zinc-inherited.** What cannot be enforced correctly is
   rejected. Grants live in reviewable YAML; a launch fills declared slots
   only.

## 2. Components

**zded** holds everything hot: desk map, queue, notification history,
journal. It is the notification server; everything else talks to it over one
IPC socket.

**The shell** is the GUI layer: bar, picker, palette, notification center,
system and mixer widgets, ask windows. A thin adapter over zded IPC, zero
logic inside; Quickshell for the MVP. If Quickshell disappoints, only the
adapter is ported (Go/gio or Rust/iced) - the `NetEnforcer` pattern. Escape
hatches (coppwr; pavucontrol-qt until our mixer replaces it) are
palette-only. Nothing GTK or GNOME based.

**desk**: manifests, switching, snapshot, reconcile, pause, panic, guest,
zen. Inside zded, CLI on top.

**attn**: the queue protocol and per-desk display policies. Inside zded.

**ask**: the quick LLM, popup and panel. Tiers: fast provider by default,
local for private questions, escalate for hard ones. Attaches the clipboard,
a region, or the current window. No history by default; never an agent (no
tools, no files; one action hands off to a project agent). The local tier
runs with no network; the provider tier's egress is its one API host.

**pass** derives instead of storing with standard, versioned Spectre v3 in a separate
GPL-3.0-or-later helper; it never silently adds a pepper or otherwise changes Spectre.
The host desktop UID is trusted. The broker and helper keep secret memory locked and
non-dumpable; secrets never enter argv, stdout, files, the clipboard, or a virtual
keyboard. The broker inserts only through input-method-v2 when the active field
declares a password or PIN purpose and `sensitive_data`. Same-UID processes are not
isolated, and application-declared field metadata does not prove application identity.
Implementation waits until Niri/Smithay atomically validates the same eligible text
input at commit time and ZDE approves a sole input-method owner or coexistence design.
No weaker fallback ships.

**doctor** collects state (journals, `niri msg`, `zcr ps`, failed units),
restarts the obvious, or opens an agent with the matching `runbooks/`
runbook.

**vox**: wake word plus local STT in a container granted only the mic and
the player socket, no network, running only on desks that grant it. Hard
toggle, bar indicator; off means the container is not running.

**update** collects staleness and CVE data for the pinned digests; an agent
drafts the re-pin diff and a summary; a human always signs.

**netview**: per-app connections and rates, plus allowed/blocked counters
read from each app's own nft ruleset - enforcement truth, not traffic
guesses. Owns the kill switch and the per-app cut.

**Glue scripts**: audio profiles, media dispatch, capture send-to, and
`zde new` (template + agent provider + desk, one verb).

## 3. Security highlights

- **Trusted window** (pass entry): compositor block-out, so it cannot appear
  in any capture; exclusive keyboard grab; distinct mode-colored identity.
- **Capture blocking**: per-window toggle and per-app flag. Default blocks
  all capture paths; per-window override for "OBS yes, screenshots no".
- **Private desks**: out of the picker, popups off (history only),
  capture-blocked - during screencasts and guest mode.
- **Guest mode**: one desk unlocked, the rest need your password; popups
  off, ask history cleared, clipboard history suspended.
- **Panic**: decoy desk + mute + silence, one action. Panic-lock variant
  alongside.

## 4. What zde asks of zinc

1. Stable Wayland `app_id` per app and per instance. **Landed in 0.9.0**: a
   real `wp_security_context_v1`, with `instance_id` the same string that names
   the container and that `zcr where` reports. It does not reach zde yet - niri
   26.04's IPC `Window` carries the client's own `app_id` and no security
   context - so adoption still names a workspace from a string the app chose
   for itself. The route to it is the pid niri reports, or a niri that exposes
   the context.
2. Runtime mount slots with `{instance}` templating. **Landed in 0.8.2** as
   `{state}`, `{app}` and `{instance}` on the host side of a mount. Where the
   per-instance state lives is zinc's to choose - nothing in a desk manifest
   names that path, and `$XDG_STATE_HOME/zinc/<app>/<instance>` is the shape zde
   uses for its own. Worth having it readable from `zcr` rather than shared as a
   constant, so the two projects cannot drift about it. Both the app and the
   instance become path components, and a manifest is a file somebody edits, so
   both sides should refuse a name that is a path; zde refuses one here.
3. Instance naming in zcr. **Landed**: `app@instance` addressing and `zcr
   where` in 0.8.1, `zcr run app@instance` in 0.8.2. zde's manifests already
   carry `instance`, and refuse one that is not a name, because it becomes a
   path component on both sides - which zinc now refuses too, on both halves.
4. Per-instance filtered DBus socket (notifications + MPRIS); doubles as
   attribution (principle 6). **Landed** in 0.8.0, with the attribution half in
   0.9.0: `zcr bus` maps a live bus connection to `app@instance`, published by
   zinc rather than claimed by the app. Nothing in zde reads it yet.
5. `Inherits:`, resolved at validate time. **Landed** in zinc 0.7, as
   `zc validate <app> --resolved` (the tool asked for here as `zcc` was
   renamed `zc` in that release).
6. Counters on the generated nft rules plus netns enumeration, for netview.
   **Landed in 0.9.0** as `zcr net`, which is what netview (0.3) reads.

All six remain answered in zinc 0.10.1. What is left is on this side.

## 5. Scenario catalog

The requirements record. W = performance work, C = concentrate, F = film,
G = gaming, A = added in design. Laptop is a profile over all: one monitor,
no mouse, One-hand mode, device remaps.

| ID | Scenario | Covered by |
|---|---|---|
| W1 | open a file in a project, near-fullscreen | launch-here + zen |
| W2 | rotate LLM chats, approve decisions | queue + queue-jump |
| W3 | like / skip / play / pause / download a track | media actions on the target |
| W4 | google or ask an LLM, quickly | palette web mode, ask |
| W5 | enter a secret safely | pass: types, skips the clipboard |
| W6 | clean media view until something needs me | zen + urgent overlay |
| W7 | rotate across work projects | picker + queue |
| W8 | hide everything, show harmless work | panic to decoy |
| W9 | save one project's condition | desk snapshot |
| W10 | save all desks, restart later | snapshot + reconcile |
| W11 | open on a specific monitor | manifest placement |
| W12 | capture a region and send it | capture + send-to |
| W13 | a service fell, bring it back | reconcile; doctor for hard cases |
| W14 | forgot a hotkey | generated cheatsheet; palette by name |
| W15 | switch to headphones | sink-switch |
| W16 | fully mute the mic | mic-mute, visible state |
| W17 | quiet one app | per-app volume |
| W18 | calendar, time, date | bar clock, calendar TUI |
| W19 | what eats RAM / VRAM / CPU | widgets; btop/nvtop escape |
| W20 | new project in one action | `zde new` |
| W21 | system broke, LLM help ready | doctor + runbooks |
| W22 | lock | lock |
| W23 | unlock shows a preset desk | lock-preset |
| W24 | clipboard everywhere, with history | clip history (global by Wayland) |
| W25 | per-app resource control | zinc `ResourcesMeta`; desk pause |
| W26 | disk control | automount + widgets |
| C1 | silence everything, one button | attn focus mode |
| C2 | one project's notifications only | attn filter on channel attribution |
| F1 | voice control that does not spy | vox |
| F2 | flash and loudness defence | luma-clamp shader; PipeWire limiter |
| F3 | auto sleep after the film | player end hook + idle |
| G1 | glance at the browser, game keeps focus | gamescope + Passthrough |
| G2 | screenshot to discord | capture + send-to |
| A1 | quiet during screencast, private desks hidden | attn + private flag |
| A2 | route some apps through a VPN | zinc vpn-container roadmap; zde does the UX |
| A3 | fresh images, with review | update; a human signs |
| A4 | mic and camera live indicator | the bar |
| A5 | what did I miss | notification center |
| A6 | laptop basics | widgets + laptop profile |
| A7 | timers | attn |
| A8 | quick-ask panel | ask |
| A9 | hand the PC to someone | guest mode |
| A10 | hide one window from capture | capture-block |
| A11 | media keys hit the right player | the visible media target |
| A12 | who talks on the network, what got blocked | netview |
| A13 | cut all network, one action | net kill + per-app cut |
