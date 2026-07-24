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
<desk>.<monitor>.<label-or-n>     vshop.DP-1.code   vshop.HDMI-A-1.1
regulars.<monitor>.<n>            regulars.DP-1.1
```

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
5. Desk switch restores each monitor's last-active workspace; on hotplug the
   manifest re-places workspaces on their home monitors.
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
  via `{instance}` templating in the app's mount slots.
- `browser-vshop` is an inherited app (`Inherits: browser`): per-project
  variants needing their own security posture get their own reviewable YAML.
  Instances = identical-config multiplicity; inheritance = config variation.
- `background: keep | pause` - pause freezes off-desk (`podman pause`),
  resumes on enter. Default keep.

## 6. The action map

Physical keys are a scheme on top, assigned later (vision.md, principle 2).
Modes gate which actions are live.

| Group | Actions |
|---|---|
| desk | `switcher`, `switch <name>`, `next`, `prev`, `last`, `queue-jump`, `regulars`, `move-window-next`, `move-window-prev`, `move-window-to-desk <name>`, `snapshot`, `reconcile`, `pause`, `panic`, `block`, `guest <name>`, `zen` |
| monitor | `focus <dir>`, `move-window <dir>`, `move-workspace <dir>` |
| workspace | `next`/`prev` (band-clamped), `overview` |
| window | `focus <dir>`, `move <dir>`, `resize`, `preset-width`, `consume`/`expel`, `float`, `fullscreen`, `close`, `capture-block`, `jump-to` (fuzzy over all windows; travels the hierarchy) |
| launch | `launcher.open` (zlg), `palette.open` (run any action by name), `app.launch <name>`, `app.launch-at <name>` (prompts for a directory), `app.launch-here` (context fills the slot), `app.jump-or-launch`, `app.new-instance` (always fresh) |
| ask | `oneshot`, `panel`, `escalate`, `local` |
| pass | `open` (trusted secrets window; types into the focused field, never the clipboard), `type` (trusted window only) |
| clip | `history`, `clear` |
| capture | `shot-region`, `shot-window`, `shot-full`, `replay-clip`, `send-to <target>` |
| media | `play-pause`, `next`, `prev`, `panel`, `like`, `download`, `target-pick`, `target-next`, `target-pin` |
| audio | `vol-up`, `vol-down`, `mute`, `app-vol`, `sink-switch`, `app-sink`, `mic-mute` |
| net | `observe`, `kill` (global toggle, loud bar state), `app-cut <app>` |
| modes | `normal`, `window`, `kb-mouse`, `one-hand`, `passthrough` |
| system | `lock`, `lock-preset` (switch BEFORE lock), `power`, `quiet`, `connections` (bt/wifi/eth), `calendar`, `wallpapers`, `brightness-up`/`brightness-dn`, `help`, `notif-center`, `doctor`, `update`, `layout-switch` (native) |

Launch placement: manifest pin first; else adoption (focused workspace,
current scroll position, active desk); guest mode restricts launching to the
guest desk. The trio: `jump` never starts, `jump-or-launch` starts if absent,
`new-instance` always starts.
