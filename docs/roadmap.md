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

Every line of that has landed, and a desk now starts what its manifest declares,
pins each app to the workspace it named, and puts the session in the attn mode
it asks for. Four things arrived beside the
list rather than in it: the connections surface (`Mod+Shift+c`), which is 0.4's
wifi TUI in the shape the shell wanted; the palette (`Mod+semicolon`), which is
0.2's and came early because a good part of the cheatsheet is still silent and
a palette is what says which part; bluetooth, as `zde system bluetooth`
with nothing on a screen; and the power menu (`Mod+Shift+x`), which belongs
here rather than in the laptop profile it was filed under - logging out of this
desktop meant opening a terminal, which is not a thing to ship a phase on. It
is lock, log out, suspend, reboot and power off, the three that end something
asking first and saying what is about to be lost - the windows that close, the
arrivals the queue never got, anybody else logged in. The lock is the same
locker `zde system lock` runs, and the rest is logind's, so a refusal - an
inhibitor holding sleep, a polkit that will not take the verb from this session
- comes back as a refusal in words rather than as a key that did nothing.

The popup has landed since, and it is what turns the "actions" capability from a
claim about reach into one about immediacy as well. A notification appears where
you are looking, with the summary, the body and every button its sender
declared, and takes itself away after a few seconds. It takes no keyboard when
it arrives: every other surface the shell draws over the bar shares one
exclusive grab, and it took a fix to keep them to one at a time, so one more
that grabbed on every arrival - at the choice of any app on the session bus -
would be the worst offender on the machine. The number of them is deliberately
not written here: it is a count several branches are each adding to, and a
sentence that has to be renumbered is a sentence that ends up wrong. Its buttons
are clickable the whole time it is up, and `Mod+Ctrl+n` is the one deliberate
key that hands it the keyboard, which it then holds only while a finger is on
it. Two things decide whether a card appears at all, and they are kept apart
because they are different people's decisions: the session's mode (quiet shows
nothing, focus shows what the sender called urgent, work shows everything) and
the desk's own `private: true`, which is popups off in every mode
([`vision.md`](vision.md), section 3). The history keeps the lot whichever
way both answer, because that is principle 3 and a mode or a desk that changed
what is recorded would be deciding what happened.

The clipboard is in that list rather than in 0.2 because it was moved here. Two
reasons, and the second is the one that decides it: `Mod+v` is a key people
press out of habit on any desktop, so it is the most expensive of the silent
ones to leave for later - and the two rules that make this zde's rather than
wl-clip-persist's are much cheaper to hold from the first line than to add to a
history that already exists. An entry an application marks as a secret is never
read at all, and every entry expires by being overwritten rather than by being
hidden; both of those decide what the daemon may hold, and a design that held
everything first would have to be taken apart to get them.

Where those lines stop short is what is left of the phase, and none of it
blocks 0.2:

- the rest of what a manifest declares. `policies.attn` is read now, and beside
  it `policies.zen`, `background: pause` and `on_enter`/`on_exit` are still
  parsed and consulted by nobody. Zen belongs to 0.2's security set and pausing
  to 0.3's resources, so what is left here is the two hooks.
- the popup is unproven in CI. The smoke test boots a real niri and counts which
  layer surfaces hold the keyboard, which is exactly the assertion this wants -
  but it stops the shell before it starts the session bus a notification needs,
  so proving the card appears without a grab is by hand for now
  ([`verify.md`](verify.md), section 5).
- history survives the daemon in part. It is a ring of 30 per sender in memory,
  for at most 12 named senders plus two rings nothing on the bus can reach, a
  nameless one and the desktop's own, and the newest 40 of it - across all of
  them, not 40 from each - are written to a file of their own beside the
  journal, bodies cut to 400 characters, 0600, rewritten every two minutes and
  on the way out. Nothing live lands in the nameless one: the bus fills in a
  name for a sender that gives none, and `zde queue add` writes the journal and
  not the history, so what is in it is a restored row whose sender field was
  empty (internal/attn, `nobody`). zde's own notification sends as `zde`, which
  is reserved: a claim off the bus that reads as that word is recorded under the
  bus address instead, and the desktop's ring is exempt from eviction, because
  it holds one record and was otherwise the cheapest thing on the machine to
  throw away. What the file buys is "what did I miss" answering across a reboot
  instead of starting every login blank. A file of its own rather than the
  journal, which is appended to on every arrival and rewritten only when a
  daemon starts, so whatever goes in it is carried until the next login
  (internal/attn, PerSenderMax). What a restart still costs is
  the rest of every ring, the rest of every long body, and the actions: nothing
  can be pressed on a row whose app was on the last session's bus, and the
  center says so rather than offering buttons that go nowhere. What arrived on a
  desk declared `private: true` never reaches that file - which is a claim about
  that file and not about the whole disk, because the journal still records the
  summary and the sender of anything the mode queued, though no longer the body.
- one loud app no longer answers the question for everybody. A ring each means
  a download posting a hundred progress updates spends its own thirty and
  nobody else's, and a notification carrying `replaces_id` is written over the
  record it supersedes instead of landing beside it, so that download is one
  row. Names are bounded too, at 12, and the ring that goes when a thirteenth
  turns up is the cheapest to lose rather than the least recently used - so an
  app that varies what it calls itself evicts its own one-record rings instead
  of your real senders, and what it costs to displace a sender is what it costs
  to out-hold it. What none of that does is say who sent anything: the name is
  still the sender's own claim, and only attribution by channel changes that
  ([`vision.md`](vision.md), principle 6 and ask 4), which waits on something in
  zde reading `zcr bus`. One name is out of the claim's reach in the meantime:
  `zde` is what the desktop's own messages carry, and an arrival that asks for
  it is recorded under its bus address instead - because "this desk could not
  start browser@vshop" is a sentence whose whole weight is that the thing
  telling you is the thing that tried (`internal/attn`, `SelfFrom`). It is a
  reservation on the word and not on one spelling of it; what it does not cover
  is a name that merely looks like it, which is the unverified column itself.
- a replacement spends a revision of the history it did not need to. The bus
  side closes the old notification before the new one arrives, so the record is
  marked dismissed and the revision counter moves, and then the arrival removes
  that record anyway (internal/attn, Replace). It costs one revision per
  replace, which at worst has the snapshot writer write the file once more than
  it had to on its two-minute clock. Written down rather than fixed: the fix is
  a second path through the sink for a close that is only half of a replace, and
  that is not obviously smaller than the problem.
- the clipboard's sensitive hint is a claim the source makes, and nothing in
  Wayland makes it. zde honours `x-kde-passwordManagerHint` by never reading an
  offer that carries it, which is everything zde can do about it and nothing at
  all about a password manager that does not send it - `pass` types instead of
  copying for exactly that reason (vision.md, principle 5). The check also costs
  a second question to the compositor, since wl-paste answers what is offered
  and what it holds on two connections rather than one, and closing that gap
  means speaking the data-control protocol here (internal/clip/wl.go).
- ask has no tier until a machine names one. The panel carries the previous
  turns now - stdin holds the conversation, a JSON object a line, and the role
  is a field rather than a prefix so that nothing in an answer can arrive as
  something a person said - but a conversation is capped at 64 KiB and the cap
  is a refusal: past it the panel says to start a fresh one (`ctrl+n`) rather
  than dropping the oldest turns to fit. Whether 64 KiB and the two minute
  deadline are the right pair is for a real tier to say.
- nothing is sandboxed until somebody defines an app. Layer 2 is still
  provisioned by hand ([`delivery.md`](delivery.md)), so a manifest can still
  name an app this machine has no way to start. What has gone is the silence:
  `zde doctor` names the desk and the app before anybody switches to it (asking
  zcr, which is the resolver a launch uses, and saying on the line which of the
  two answered, since `zde.apps` holds the keys' logical names and not zinc app
  names at all), and a launch that fails as a desk is entered arrives as a
  notification - one for the whole switch, naming what did not start and why,
  rather than a line in a log nobody reads while they are working. A switch
  back to a desk that is already up says nothing at all: zinc refusing to start
  a second copy of something running is not a launch that failed.

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
- resources: `desk.pause`, background policies, per-desk cost widgets. The
  action is registered and bound to nothing, so the palette carries the row and
  says nothing is written behind it; the manifest key `background: pause` is
  parsed and acted on by nobody until this lands.
- netview: per-app connections/rates, nft counters (zinc ask 6), `app-cut`.
- per-project browsers via `Inherits:` (ask 5) + `zde new`.
- regulars polish: focus-mode comms filtering.

## 0.4 - Media, gaming, laptop

- vox: wake word + local STT, film-desk scoped, hard toggle + indicator.
- film desk: PipeWire limiter, luma-clamp shader, end-of-film sleep, ambient
  side monitors.
- gaming desk: gamescope, auto-Passthrough + never-release set, replay.
- laptop profile: battery/brightness, single-monitor degradation, workspace
  placement re-applied on dock/undock. Three halves of this came early and in
  different shapes - wifi as a shell surface on `Mod+Shift+c`, bluetooth as
  `zde system bluetooth` with no surface at all, and the power menu on
  `Mod+Shift+x`, which moved into 0.1 because a desktop you cannot log out of
  is not one you can use - so what is left here is folding bluetooth into that
  surface, the lid and the battery thresholds, and the rest of what a laptop
  is.
- **Bringing a workspace home when niri cannot.** Re-placing workspaces on
  their home monitor after a dock is niri's, not zde's: it records the output
  each workspace was opened on and returns it there when that output comes
  back (`Layout::add_output`, niri 26.04). zde deliberately does not do this
  itself. The action exists - `MoveWorkspaceToMonitor { output, reference }` -
  but performing it *overwrites* niri's record with zde's, and zde's record is
  the workspace name, which is the weaker of the two: it is the one an unplug
  can corrupt, and niri's surviving copy is what puts things right when it
  does. What is genuinely uncovered is the case niri's record cannot reach,
  because it does not outlive a compositor restart: a workspace whose monitor
  was away when niri started sits on the survivor under a name that still says
  home, and nothing will move it when the monitor returns. `desk.Map.Displaced`
  names exactly that set and has no caller yet; surfacing it in `zde status` is
  the honest first step, and moving anything is a decision to take after a
  laptop has been lived with.

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
- keymap, the shortcuts inhibitor: `zwp_keyboard_shortcuts_inhibit_manager_v1`
  is one of the two sensitive globals niri 26.04 hands to sandboxed clients too
  - thirteen other managers are built behind its security-context filter and
  these two are not (`src/niri.rs`; the other is the idle inhibitor below) - and
  it activates a new inhibitor with no dialog, which niri's own source
  calls a FIXME. So a focused container can take any zde bind that has not said
  `allow-inhibiting=false`. Four say it: panic, lock, the mode picker, and the
  `Mod+Ctrl+Escape` that ends a grab. What a session has to settle is whether
  that is the right four ([`verify.md`](verify.md), section 10) - whether
  anything you actually run grabs the keyboard at all, whether losing `Mod+j`
  to it is tolerable or maddening, and whether the key that gives them back is
  reachable enough for the moment you need it. Upstream may also close this: a
  confirmation dialog, or the filter the other thirteen already have, would
  make the whole question smaller.
- the idle inhibitor, the second ungated global:
  `IdleInhibitManagerState::new::<State>` is built with no
  `client_is_unrestricted` where its thirteen neighbours take one, so a
  sandboxed app can hold `zwp_idle_inhibit_manager_v1`. Worse than the keyboard
  one in the part that matters: niri's `refresh_idle_inhibit` honours a surface
  that is merely **visible, not focused**, so a container on a workspace nobody
  is looking at keeps the session from ever going idle, with no interaction and
  nothing shown.

  **zde cannot see it, and that is the finding.** niri exposes it nowhere: not
  in `niri-ipc`'s `Request`, `Response` or `Event` (checked at 26.4.0 - the only
  hit for "inhibit" in 2109 lines is the keyboard-shortcuts action), not on the
  event stream, and its `org.freedesktop.ScreenSaver` has `Inhibit` with no
  getter. The computed bool goes to the idle notifier and stops.

  So what shipped is the half that is observable, labelled as a half. logind's
  own `idle` inhibitors are readable and always were - `system.idle` reads the
  table the power menu already costs its rows from - and that is what the bar's
  `idle held` counts and what `zde doctor` names. The two mechanisms do not
  overlap: a Wayland inhibitor on a mapped surface moves nothing in
  `ListInhibitors` and does not touch the session's `IdleHint`, measured rather
  than assumed. Every place the reading is drawn says which half it is, because
  a bar that implied it could see both would be worse than no bar at all.

  What is open: whether anything anybody runs takes a Wayland one in practice
  ([`verify.md`](verify.md), section 11), and what to do when the lock preset
  lands in 0.3 - that is the release where this stops being an indicator and
  starts being a way to keep a screen unlocked. Upstream adding the filter would
  close it; so would any read-back at all, which is the smaller ask and the one
  worth opening an issue for.
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
  up. It is a key now, `Mod+Ctrl+Shift+Tab`, with `Mod+Ctrl+Tab` for the window:
  a terminal was the only way to reach the one verb the band depends on, and a
  CLI-only action is right for a TTY - the console you go to when the session is
  broken - and not for a session that is running. Neither chord carries the desk
  name, because a desk is called whatever you called it: the bind spawns the
  verb bare and it opens the picker `Mod+Tab` opens, which offers the regulars
  whether or not there is a band yet. What is left of the item is the shape of it
  in use: whether promoting a workspace is the thing people reach for, or whether
  they want to name an empty one and fill it afterwards, which this refuses
  because an empty workspace is never adopted and so has no name to move - and
  now also whether one surface with three verbs behind it reads as one thing or
  as a key you have to check before you press ([`verify.md`](verify.md), a day of
  work).
- a single unreadable file in the desks directory taking every manifest with
  it: answered, by failing one file at a time. The loader returns what it read
  and what it could not, `zde doctor` names the files it could not, and the
  rest of the desks are declared.
- nav with a layer-shell surface up: one holding keyboard focus reads as
  nothing focused at all, so a press spends itself putting focus back on a
  window instead of going anywhere. Such a surface is the picker, the
  notification center, the connections list, the palette, the ask window, the
  power menu and the clipboard history - named rather than counted, for the
  reason 0.1 gives about the popup, and each of them takes the keyboard only
  while it is visible, so the thing to try is a nav key the instant after each
  one closes ([`verify.md`](verify.md), a day of work). Not settled here:
  driving a real key into a nested compositor did not work well enough to trust
  the answer.
  The notification popup is one more surface and the one that answers this
  differently: it takes no keyboard at all until `Mod+Ctrl+n`, and then gives it
  back after ten seconds without a keypress. So the thing to try on that one is
  the opposite - notifications arriving while you type, and whether a single
  keystroke ever goes anywhere but the window you were in.
- a desk's attn policy, in use. The desk borrows the mode and gives it back
  when you leave, and a mode set by hand ends the loan: it follows you off the
  desk, and the desk takes the mode again the next time you enter it. The other
  order - the hand-set mode ending when you walk away - was rejected because it
  makes `Mod+q` mean two things depending on which desk you pressed it on, but
  which one a person expects is a question for a week of use. The other half to
  feel for: the policy rides on a desk switch, so a desk reached without one
  (niri's own workspace keys, the overview, `desk.move-workspace-to`) keeps the
  mode you arrived with until the next switch settles it.
- attn actions, with "actions" claimed, every declared one offered in the center,
  and now a popup that carries them the moment something arrives. What is left is
  what only real senders answer: whether a mail client's archive and delete are
  the right two buttons to have on a card that lasts five seconds, and whether
  anybody reaches for `Mod+Ctrl+n` rather than clicking - it is the one key here
  that exists because a surface deliberately refuses to grab the keyboard, so if
  nobody presses it the answer is a different way to hand over the keys, not a
  popup that grabs. Five seconds and fifteen for the urgent are guesses;
  three cards, and a second arrival from one sender replacing that sender's card,
  are the bounds a build bot is the test of.
- the join verdict: NetworkManager can take longer to refuse a wrong password
  than the IPC deadline allows, so the surface stops waiting and watches the
  link instead. Pushing the verdict as an event is the fix and is not built;
  whether it is needed is a wrong password on real hardware.
- ask against a real tier: whether an answer arriving piece by piece reads
  well, and whether two minutes is the right cap once a local model that thinks
  before it speaks sits behind `local`. The panel now puts the conversation in
  front of the question, which makes both halves of that sharper: a model that
  reprocesses its context each turn gets slower as the panel fills, so the two
  minutes and the 64 KiB a conversation may reach are one decision and want
  measuring together. The other half is whether a tier somebody writes reads the
  frame at all, or answers only the last line and calls it a conversation.
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
