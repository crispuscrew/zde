# zde - The Spatial Model and the Action Map

Frozen 2026-07-22. Terms: [`glossary.md`](glossary.md). Rationale:
[`vision.md`](vision.md).

## 1. niri facts (not our design)

- Each **monitor owns its own endless vertical strip of workspaces**. No
  shared sea: two monitors, two strips. A workspace is shown on one monitor
  at a time, never two at once.
- A **workspace is an endless horizontal strip of columns** (each column =
  one or more windows stacked). The monitor is a viewport that scrolls;
  windows are never squeezed to fit.
- One empty workspace always exists at each strip's end; named workspaces
  persist when empty; on unplug workspaces migrate to a survivor, on replug
  they return.

## 2. The hierarchy

```
                    DESK "vshop"          DESK "haven"         DESK "film"
 monitor L           [ code    ]           [ code     ]         [ ambient ]
 (its strip)         [ logs    ]           [ db       ]
 monitor R           [ agent   ]           [ research ]         [ player  ]

 one workspace, the horizontal axis:
 [ code ] =  | nvim ...... | term | agent |  ......>   (endless, viewport scrolls)
```

| Level | Axis | Moving means |
|---|---|---|
| 1. **desk** | which "room" ALL monitors show | every monitor's strip switches at once, atomically |
| 2. **monitor** | viewports side by side | focus hops between monitors |
| 3. **workspace** | vertical, per monitor, in the desk's band | viewport slides up/down the strip |
| 4. **column / window** | horizontal in workspace, vertical in column | viewport slides left/right; focus moves per window |

## 3. Naming: the name IS the ownership record

```
<desk>.<monitor>.<label>          vshop.DP-1.code   vshop.DP-1.firefox
regulars.<monitor>.<label>        regulars.DP-1.comms
```

The label is the manifest's where it declares one, and otherwise the
workspace's first app: `org.mozilla.firefox` becomes `firefox`, and a second
one on that monitor becomes `firefox-2`. A name is something you read on a bar
and say out loud, so it says what is in there. An ordinal (`vshop.DP-1.1`) is
the fallback for an app id that leaves nothing readable, not the normal case.
An empty workspace is never named: niri keeps one at the end of every strip,
and claiming it would name the scratch space and make niri open another.

The regulars band is reachable from any desk. The mapping is self-describing
and crash-proof:
zded rebuilds the whole desk map from the names alone. The journal keeps only
what names cannot: last-active position per desk, queue, media target, mode.
The bar shows the friendly part.

## 4. Invariants

1. A workspace belongs to exactly one desk (or the regulars) and is shown on
   exactly one monitor at a time. Windows and whole workspaces move freely
   between monitors (monitor action group; zded renames a moved workspace so
   the name stays truthful). Only simultaneous display on two monitors is
   impossible - niri physics.
2. A window lives in one workspace, hence one desk. "Where did that window
   go" is impossible by construction.
3. **Adoption**: whatever you create while desk X is active belongs to desk
   X; fresh dynamic workspaces are named into the active band.
4. Workspace scrolling is clamped to the active desk's band; crossing desks
   is always an explicit desk-level action.
5. Desk switch restores each monitor's last-active workspace. On hotplug the
   workspaces go home, but niri is what takes them: it records the output each
   was opened on and returns it there when that output comes back
   (`Layout::add_output`). zde issues no move and the manifest re-places
   nothing - zde's whole part is that the name keeps saying home while the
   monitor is away, so the record survives the unplug. The case niri cannot
   cover is a niri that restarted while the monitor was gone, since its record
   does not outlive the session: those workspaces stay on the survivor with a
   truthful name and nothing moves them ([`roadmap.md`](roadmap.md), 0.4;
   [`verify.md`](verify.md), the lid).
6. Focus changes never rearrange anything; layout changes are explicit.

## 5. Desk manifests

Authoring order: `snapshot` captures what you arranged; the TUI editor covers
the rest; shipped LLM instructions let an agent author them.

```yaml
name: vshop
private: false            # private desk: vision.md, section 3
monitors:
  DP-1:      { workspaces: [code, agent] }   # -> vshop.DP-1.code, vshop.DP-1.agent
  HDMI-A-1:  { workspaces: [aux] }
apps:
  - { app: nvim,   instance: vshop, mounts: { work: ~/git/vshop },
      monitor: DP-1, workspace: code }
  - { app: claude, instance: vshop, mounts: { work: ~/git/vshop },
      monitor: DP-1, workspace: agent }
  - { app: browser-vshop, monitor: HDMI-A-1, workspace: aux, background: pause }
policies: { attn: work, zen: false }
on_enter: []              # e.g. pin a media target, start music
on_exit:  []
```

- `instance` + `mounts`: one app definition, many desks; per-instance state
  via `{instance}` templating in the app's mount slots. The two fields are one
  address to zinc - `nvim@vshop` - and `zde desk apps` prints it beside the
  directory zinc says that instance keeps its state in. zde asks rather than
  joining that path itself: the layout is zinc's, and a second copy of it drifts
  the first time either side moves a directory. `{instance}` templating in mount
  slots landed in zinc 0.8.2, as `{state}`, `{app}` and `{instance}`.
- `browser-vshop` is an inherited app (`Inherits: browser`): per-project
  variants needing their own security posture get their own reviewable YAML.
  Instances = identical-config multiplicity; inheritance = config variation.
- `background: keep | pause` - pause freezes off-desk (`podman pause`),
  resumes on enter. Default keep. Not implemented: nothing pauses anything yet.
- Entering the desk starts these, once, behind the switch rather than in front
  of it - `zcr run <app>@<instance> --exec` per app, and a second entry is
  refused by zinc rather than tracked here.
- `policies.attn` is the mode entering the desk puts the session in: work, focus
  or quiet, the three of [`vision.md`](vision.md) principle 3. The desk borrows
  it - what was in force is written down and comes back when you leave for
  another desk - so a desk for concentrating does not silence the rest of the
  day. Declaring nothing is not declaring work: a desk with no opinion leaves
  the mode where it is, which is most desks. A mode set by hand while you stand
  there ends the loan; it is yours, it follows you off the desk, and the desk
  takes the mode again the next time you enter it. Only a desk switch applies
  it, so a desk you arrive on another way keeps the mode you came with.
  `policies.zen` is parsed and read by nobody: zen is 0.2's security set.
- `app_id` is what the window calls itself, which is not what zinc calls the
  app: `app: browser` opens a window that says `org.mozilla.firefox`. Given it,
  the pin above becomes a niri window rule in `dynamic.kdl` and the window opens
  on that workspace. Without it the app still starts and adoption places it.
  It is a second name for the same thing and it should not have to be here -
  zinc gives the compositor a per-instance identity, and niri matches rules on
  the id the app asserts (docs/vision.md, ask 1). The rules are written when
  zded starts and on `zde desk reconcile`, so a manifest edited mid-session
  takes effect at the next one of those.

## 6. The action map

Physical keys are a scheme on top, assigned later (vision.md, principle 2).
Modes gate which actions are live. Not everything here is written, and not
everything written here has a chord. The keymap registry is the part that has a
key or a reason to be reachable by name, and that is what `palette.open` lists,
marking each row runnable or not on the build in front of you. The rest are the
CLI's: `zde desk switch`, `desk snapshot`, `desk reconcile`, `ask local` and
`attn <mode>` all work today with no row in any surface. A missing palette row
means no key, not no verb.

It does not promise a verb either. `pause` has a registry row and no key, which
is what this list does with an action that is coming: the palette carries it and
says on the row that nothing is written behind it, and the manifest key
`background: pause` is parsed and acts on nothing (section 5). `guest <name>` is
the one below with neither - no registry, no daemon handler, no CLI - because it
carries a desk name and an unbound parametric row is a name no surface can show,
and because it is a security posture rather than a verb, which is 0.2's set to
decide. Both stay in the table because this is the map and not the changelog.

The two that name a desk are worth naming here too, because how the answer is
given was the open question rather than whether the verb worked. `desk
move-workspace-to` is the only way the regulars band comes into being at all
([`roadmap.md`](roadmap.md)), and `desk move-window-to` is how one window leaves
it; both had worked from a terminal since before either had a key. They have one
now - `Mod+Ctrl+Shift+Tab` and `Mod+Ctrl+Tab` - and the desk name they take is
asked for by the same picker `Mod+Tab` draws, with a line on it saying what
choosing a row will do, since three verbs drawing the same desks look identical
without one. What that settled belongs in the keymap and not in a note here: a
verb that takes a name wants a surface that offers the names, not a chord per
name.

| Group | Actions |
|---|---|
| desk | `switcher`, `nav-down`/`nav-up` (focus stacked window, else rotate desk), `switch <name>`, `next`, `prev`, `last`, `queue-jump`, `regulars`, `move-window-next`, `move-window-prev`, `move-window-to <name>`, `move-workspace-to <name>` (the whole workspace changes hands: how the regulars are made, and how work leaves them), `snapshot`, `reconcile`, `pause`, `panic`, `block`, `guest <name>`, `zen` |
| monitor | `focus <dir>`, `move-window <dir>`, `move-workspace <dir>` |
| workspace | `next`/`prev` (band-clamped), `overview` |
| window | `focus <dir>`, `move <dir>`, `resize`, `preset-width`, `consume`/`expel`, `float`, `fullscreen`, `close`, `capture-block`, `jump-to` (pick any open window and go to where it is; travels the hierarchy, and rearranges nothing) |
| launch | `launcher.open` (zlg), `palette.open` (run any action by name, filtered as you type, with the ones nobody has written marked as such; answering an expression typed in place of a name is 0.2), `app.launch <name>`, `app.launch-at <name>` (prompts for a directory), `app.launch-here` (context fills the slot), `app.jump-or-launch`, `app.new-instance` (always fresh) |
| ask | `oneshot`, `panel`, `escalate`, `local` |
| pass | `open` (trusted secrets window; types into the focused field, never the clipboard), `type` (trusted window only) |
| clip | `history`, `clear` |
| capture | `shot-region`, `shot-window`, `shot-full`, `replay-clip`, `send-to <target>` |
| media | `play-pause`, `next`, `prev`, `panel`, `like`, `download`, `target-pick`, `target-next`, `target-pin` |
| audio | `vol-up`, `vol-down`, `mute`, `app-vol`, `sink-switch`, `app-sink`, `mic-mute` |
| net | `observe`, `kill` (global toggle, loud bar state), `app-cut <app>` |
| modes | `menu` (pick a mode), `normal`, `window`, `kb-mouse`, `one-hand`, `passthrough` |
| system | `lock`, `lock-preset` (switch BEFORE lock), `power`, `quiet` (toggle), `attn <mode>` (work / focus / quiet by name; the CLI has it, no key is free for it), `connections` (wifi, and the link you are on; `forget` drops a saved network), `bluetooth` (the radio, what is around it, and pairing; no key of its own, since a second chord for half of one surface is a key to remember for no reason), `calendar`, `wallpapers`, `brightness-up`/`brightness-dn`, `help`, `notif-center`, `notif-reach` (put the keyboard on the newest popup, which is the only way a popup ever takes it), `shortcut-grab` (native; hand the focused app zde's keys, or take them back - the one key an app holding a shortcuts inhibitor can never swallow), `doctor`, `report` (write the state snapshot down for a session that will not come up; needs `zde.debug`, and no key of its own - the day it is wanted there is nobody at the keyboard), `update`, `layout-switch` (native) |

Launch placement: manifest pin first; else adoption (focused workspace,
current scroll position, active desk); guest mode restricts launching to the
guest desk. The trio: `jump` never starts, `jump-or-launch` starts if absent,
`new-instance` always starts.
