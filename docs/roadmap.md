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
- clip: the clipboard history, with the sensitive-hint and TTL invariants.

Every line of that has landed, and a desk now starts what its manifest declares
and pins each app to the workspace it named.

The clipboard is in that list rather than in 0.2 because it was moved here. Two
reasons, and the second is the one that decides it: `Mod+v` is a key people
press out of habit on any desktop, so it is the most expensive of the silent
ones to leave for later - and the two rules that make this zde's rather than
wl-clip-persist's are much cheaper to hold from the first line than to add to a
history that already exists. An entry an application marks as a secret is never
read at all, and every entry expires by being overwritten rather than by being
hidden; both of those decide what the daemon may hold, and a design that held
everything first would have to be taken apart to get them.

Three things arrived beside the list rather than in it: the connections surface (`Mod+Shift+c`), which is 0.4's
wifi TUI in the shape the shell wanted; the palette (`Mod+semicolon`), which is
0.2's and came early because a good part of the cheatsheet is still silent and
a palette is what says which part; and bluetooth, as `zde system bluetooth`
with nothing on a screen.

Where those lines stop short is what is left of the phase, and none of it
blocks 0.2:

- a desk's own attn policy. `policies.attn` in a manifest is read by nobody, so
  the mode is the session's and not the desk's, and the per-desk display
  policies [`vision.md`](vision.md) gives attn are one mode wide.
- a popup. The "actions" capability is claimed and every action a sender
  declares is offered in the center, up to the nine a digit can reach, with the
  rest counted and said rather than dropped quietly - but the offer is behind
  `Mod+n` rather than in front of you. It is a claim about reach and not about
  immediacy, and nothing in 0.1 makes it both.
- history dies with the daemon: a ring of 200 in memory, because the journal
  fsyncs per line and this is history that is stale by the next login. The queue
  is the half that survives.
- ask has no tier until a machine names one, and the panel does not carry the
  previous turns as context.
- the clipboard's sensitive hint is a claim the source makes, and nothing in
  Wayland makes it. zde honours `x-kde-passwordManagerHint` by never reading an
  offer that carries it, which is everything zde can do about it and nothing at
  all about a password manager that does not send it - `pass` types instead of
  copying for exactly that reason (vision.md, principle 5). The check also costs
  a second question to the compositor, since wl-paste answers what is offered
  and what it holds on two connections rather than one, and closing that gap
  means speaking the data-control protocol here (internal/clip/wl.go).
- nothing is sandboxed until somebody defines an app. Layer 2 is provisioned by
  hand ([`delivery.md`](delivery.md)), so a manifest can name an app that does
  not exist and the launch is a line in zded's log.

## 0.2 - Daily driver

- pass: derivation + pepper + counters, trusted window, type-out. The other lane
  of the two-lane secrets (vision.md, principle 5); the clipboard's lane moved
  into 0.1 and is above.
- capture: shots, replay clip, send-to.
- media/audio: target (pick/next/pin) + bar widget, mixer widget, device
  switch, mic OSD.
- launch surfaces: the palette (landed early, with 0.1's shell, running actions
  by name and marking the ones nobody has written), zlg, jump / jump-or-launch
  / new-instance, launch-here.
- calc, in the palette: type an expression where you would type an action name,
  and the answer is the first row - Enter copies it, and nothing has opened a
  window. It belongs to the palette rather than beside it because the whole
  point is not choosing a calculator first. The palette arriving in 0.1 does not
  bring this with it: the surface is one thing and a second engine behind the
  same field is another.
  - The engine is `qalc` (qalculate's CLI) in a zinc container with no network,
    which is what buys units, hex, and precision without zde parsing anything
    itself: a hand-rolled parser is the kind of thing that is wrong quietly.
    Fail closed - no engine, no calc row, and the palette says so rather than
    guessing.
  - A window for the sums that outgrow one line stays a normal app: a
    calculator on a desk, launched like anything else, not a second surface for
    the shell to own.
- modes via the input-layer daemon + device remap profiles.
- security set: guest, panic + decoy, zen, lock-preset, capture-block,
  `net.kill`.

## 0.3 - Comfort and ops

- doctor's other half: the runbooks, and the agent that reads them (the update
  runbook exists: [`update.md`](update.md)). The state collection landed early
  as `zde doctor`, which is one line per check and no agent at all.
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
- laptop profile: battery/brightness/power, single-monitor degradation,
  workspace placement re-applied on dock/undock. The wifi and bluetooth halves
  came early and in different shapes - wifi as a shell surface on
  `Mod+Shift+c`, bluetooth as `zde system bluetooth` with no surface at all -
  so what is left here is folding bluetooth into that surface and the rest of
  what a laptop is.

Delivery ([`delivery.md`](delivery.md)): the flake skeleton exists; the home
module grows with each 0.1 component; ISO and `install.sh` after 0.1 is
usable.

## Verify in prototype

The open questions. How to actually answer them, on which machine and in what
order, is [`verify.md`](verify.md) - and the live image it describes is what
turned "when someone has hardware" into something anyone can do this evening.

- niri: runtime workspace renaming (adoption + monitor moves);
  block-out-from granularity; mode mechanism (config-swap vs input layer);
  X11 apps, which need xwayland-satellite (the session leaves XWayland off).
- keymap: niri key-name casing for punctuation (grave, brackets, semicolon,
  comma, period); the panic leader sequence (Mod+Escape then L) via the input
  daemon, since Escape grabs nothing. Mod+Tab then L needed no daemon in the
  end: the picker holds the keyboard while it is open, so the surface reads the
  second press itself. What a leader wants is something already holding the
  keyboard, which is worth remembering before the input layer is asked for
  anything else.
- niri config: `nav.*` shelling to zded per keypress feeling instant, and the
  config driving a real compositor. The smoke test settles the static half -
  niri's own parser accepts the tree, its includes resolve, and every emitted
  action name is real - which is not the same as running it.
- work goes into the regulars and comes out one window at a time
  (`desk.move-window-to`) or a whole workspace at a time
  (`desk.move-workspace-to`), which is the answer to a band that only fills up.
  What is unverified is whether that is enough in practice: adoption still
  names into the band while you stand on it, so work arrives there faster than
  anybody moves it out, and the rotation stepping over the band means a
  workspace parked in it is out of the way rather than in your path.
- how anyone makes their regulars: answered by `desk.move-workspace-to`, which
  hands the focused workspace to another band. A manifest still cannot declare
  the regulars and adoption still names into the desk you are on, so this verb
  is the only way the band comes into being - and being a rename in either
  direction, it is also how work leaves a band that would otherwise only fill
  up. What is left of the item is the shape of it in use: whether promoting a
  workspace is the thing people reach for, or whether they want to name an
  empty one and fill it afterwards, which this refuses because an empty
  workspace is never adopted and so has no name to move.
- a single unreadable file in the desks directory taking every manifest with
  it: answered, by failing one file at a time. The loader returns what it read
  and what it could not, `zde doctor` names the files it could not, and the
  rest of the desks are declared.
- nav with a layer-shell surface up: one holding keyboard focus reads as
  nothing focused at all, so a press spends itself putting focus back on a
  window instead of going anywhere. There are six such surfaces now - the
  picker, the notification center, the connections list, the palette, the ask
  window and the clipboard history - each taking the keyboard only while it is
  visible, so the thing
  to try is a nav key the instant after each one closes
  ([`verify.md`](verify.md), a day of work). Not settled here: driving a real
  key into a nested compositor did not work well enough to trust the answer.
- attn actions, with "actions" claimed and every declared one offered in the
  center: whether a claim about reach is read by real senders as a claim about
  immediacy. The buttons are behind `Mod+n` because there is no popup, and an
  app that changes what it sends the moment it sees the capability is the case
  nobody has met yet.
- the join verdict: NetworkManager can take longer to refuse a wrong password
  than the IPC deadline allows, so the surface stops waiting and watches the
  link instead. Pushing the verdict as an event is the fix and is not built;
  whether it is needed is a wrong password on real hardware.
- ask against a real tier: whether an answer arriving piece by piece reads
  well, and whether two minutes is the right cap once a local model that thinks
  before it speaks sits behind `local`.
- nav latency, by hand on real hardware rather than by arithmetic: one Mod+j
  spawns a `zde`, and behind it zded asks niri for the focused window, moves
  it, asks again, and on a rotation re-reads the map and the focused name a
  further three and two times. Every one is a unix socket round trip, so the
  spawn should dominate - but "should" is why this is on the list. Fold the
  reads into one pass only if a finger says to.
- include precedence: answered, in niri's own source rather than by argument.
  A later `binds` block keeps the binds already read and replaces only the
  keys it names again, which upstream did deliberately so that dots can be
  imported and then overridden. So the last include wins, per key, which is
  what zded releasing a desk's clashing bind through `dynamic.kdl` needs.
  `niri validate` accepts the second block and says nothing about the clash;
  the live image puts its terminal on a key this way, so the smoke test now
  pins it. What is left is watching it happen on a running compositor.
- keyboard layout: the config sets no xkb layout, so the session falls back to
  us while the greeter uses the console keymap. `local.kdl` is where a host's
  layout goes now; whether it should be derived from the system's own setting
  instead is the open half.
- caps:hyper landing in a usable modifier (stock xkb folds Hyper into Mod4).
- kanata mouse-button interception (fallback: evsieve).
- Quickshell under multi-monitor hotplug (swap path exists).
- netns stats: answered by zinc's `zcr net` (ask 6), so netview reads the
  generated rules' own counters rather than guessing. What is left is netview,
  in 0.3.
- NixOS: greetd reaching niri-session on real hardware (CI only evaluates the
  host, it never boots it), rootless podman subuids, Quickshell from its
  flake, kanata uinput permissions, NVIDIA with niri, and BlueZ's D-Bus policy
  for a normal user calling `RegisterAgent` - if the session cannot register a
  pairing agent, incoming pairings are refused.
- laptop: power-profiles-daemon vs TLP on real battery life.

## Cross-cutting

Every component: tests + a runnable exit check, zinc-style. Where a mechanism
is partial, the UI and docs say so.
