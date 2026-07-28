# zde - Roadmap

Frozen 2026-07-22. Design: [`vision.md`](vision.md), [`model.md`](model.md).
Zinc gate (vision.md, section 4): asks 1-3 gate 0.1; ask 4 gates channel
attribution; asks 5-6 land with their consumers.

## 0.1 - Foundation: desks + attn (proves the model)

- zded: journal, IPC, name-as-ownership, regulars band.
- desk: manifests, switcher/switch/next/prev/snapshot/reconcile, adoption,
  band clamp, last-active restore.
- attn: zded as notification server; queue (actions, `zde queue add`, urgent
  hints); queue-jump; work/focus/quiet modes.
- keymap: generated niri binds (Normal only) + generated cheatsheet.
- shell MVP: bar (mode, queue, mic, clock), picker, notification center.
- ask MVP: popup + panel, provider/local tiers, escalate.

## 0.2 - Daily driver

- pass: derivation + pepper + counters, trusted window, type-out.
- clipboard history with sensitive-hint + TTL invariant.
- capture: shots, replay clip, send-to.
- media/audio: target (pick/next/pin) + bar widget, mixer widget, device
  switch, mic OSD.
- launch surfaces: palette, zlg, jump / jump-or-launch / new-instance,
  launch-here.
- modes via the input-layer daemon + device remap profiles.
- security set: guest, panic + decoy, zen, lock-preset, capture-block,
  `net.kill`.

## 0.3 - Comfort and ops

- doctor + the remaining runbooks (the update one exists: [`update.md`](update.md)).
- update: staleness/CVE collection, agent-drafted re-pin, human signs.
- resources: `desk.pause`, background policies, per-desk cost widgets.
- netview: per-app connections/rates, nft counters (zinc ask 6), `app-cut`.
- per-project browsers via `Inherits:` (ask 5) + `zde new`.
- regulars polish: focus-mode comms filtering.

## 0.4 - Media, gaming, laptop

- vox: wake word + local STT, film-desk scoped, hard toggle + indicator.
- film desk: PipeWire limiter, luma-clamp shader, end-of-film sleep, ambient
  side monitors.
- gaming desk: gamescope, auto-Passthrough + never-release set, replay.
- laptop profile: battery/brightness/power, wifi/bt TUIs, single-monitor
  degradation, workspace placement re-applied on dock/undock.

Delivery ([`delivery.md`](delivery.md)): the flake skeleton exists; the home
module grows with each 0.1 component; ISO and `install.sh` after 0.1 is
usable.

## Verify in prototype

- niri: runtime workspace renaming (adoption + monitor moves);
  block-out-from granularity; mode mechanism (config-swap vs input layer);
  X11 apps, which need xwayland-satellite (the session leaves XWayland off).
- keymap: niri key-name casing for punctuation (grave, brackets, semicolon,
  comma, period); the panic/block leader sequences (Mod+Escape then L, Mod+Tab
  then L) via the input daemon, not niri.
- niri config: `nav.*` shelling to zded per keypress feeling instant, and the
  config driving a real compositor. The smoke test settles the static half -
  niri's own parser accepts the tree, its includes resolve, and every emitted
  action name is real - which is not the same as running it.
- nav with a layer-shell surface up: one holding keyboard focus reads as
  nothing focused at all, so a press spends itself putting focus back on a
  window instead of going anywhere. Nothing zde ships reaches that yet - the
  desk switcher is the surface that will, and it should be tried the day it
  exists.
- nav latency, by hand on real hardware rather than by arithmetic: one Mod+j
  spawns a `zde`, and behind it zded asks niri for the focused window, moves
  it, asks again, and on a rotation re-reads the map and the focused name a
  further three and two times. Every one is a unix socket round trip, so the
  spawn should dominate - but "should" is why this is on the list. Fold the
  reads into one pass only if a finger says to.
- include precedence: two files binding the same chord is accepted without
  complaint, so which one wins is unverified. zded releasing a desk's clashing
  bind through `dynamic.kdl` depends on the answer being "the last include".
- keyboard layout: the config sets no xkb layout, so the session falls back to
  us while the greeter uses the console keymap. `local.kdl` is where a host's
  layout goes now; whether it should be derived from the system's own setting
  instead is the open half.
- caps:hyper landing in a usable modifier (stock xkb folds Hyper into Mod4).
- kanata mouse-button interception (fallback: evsieve).
- Quickshell under multi-monitor hotplug (swap path exists).
- netns stats readable without ask 6, as a degraded first cut.
- NixOS: greetd reaching niri-session on real hardware (CI only evaluates the
  host, it never boots it), rootless podman subuids, Quickshell from its
  flake, kanata uinput permissions, NVIDIA with niri.
- laptop: power-profiles-daemon vs TLP on real battery life.

## Cross-cutting

Every component: tests + a runnable exit check, zinc-style. Where a mechanism
is partial, the UI and docs say so.
