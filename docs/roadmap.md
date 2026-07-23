# zde - Roadmap

Frozen 2026-07-22. Design: [`vision.md`](vision.md), [`model.md`](model.md).
Zinc gate (vision.md, section 4): asks 1-3 gate 0.1; ask 4 gates channel
attribution; asks 5-6 land with their consumers.

## 0.1 - Foundation: desks + attn (proves the model)

- zded: journal, IPC, name-as-ownership, commons band.
- desk: manifests, switch/pick/snapshot/reconcile, adoption, band clamp,
  last-active restore.
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

- doctor + first runbooks (update runbook first).
- update: staleness/CVE collection, agent-drafted re-pin, human signs.
- resources: `desk.pause`, background policies, per-desk cost widgets.
- netview: per-app connections/rates, nft counters (zinc ask 6), `app-cut`.
- per-project browsers via `Inherits:` (ask 5) + `zde new`.
- commons polish: focus-mode comms filtering.

## 0.4 - Media, gaming, laptop

- vox: wake word + local STT, film-desk scoped, hard toggle + indicator.
- film desk: PipeWire limiter, luma-clamp shader, end-of-film sleep, ambient
  side monitors.
- gaming desk: gamescope, auto-Passthrough + never-release set, replay.
- laptop profile: battery/brightness/power, wifi/bt TUIs, single-monitor
  degradation, dock re-placement.

Delivery ([`delivery.md`](delivery.md)): the flake skeleton exists; the home
module grows with each 0.1 component; ISO and `install.sh` after 0.1 is
usable.

## Verify in prototype

- niri: runtime workspace renaming (adoption + monitor moves);
  block-out-from granularity; mode mechanism (config-swap vs input layer).
- caps:hyper landing in a usable modifier (stock xkb folds Hyper into Mod4).
- kanata mouse-button interception (fallback: evsieve).
- Quickshell under multi-monitor hotplug (swap path exists).
- netns stats readable without ask 6, as a degraded first cut.
- NixOS: flake evaluates (no nix on the dev host yet), rootless podman
  subuids, Quickshell from its flake, kanata uinput permissions, NVIDIA with
  niri on stable.
- laptop: power-profiles-daemon vs TLP on real battery life.

## Cross-cutting

Every component: tests + a runnable exit check, zinc-style. Where a mechanism
is partial, the UI and docs say so.
