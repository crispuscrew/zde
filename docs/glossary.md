# zde - Glossary

Frozen 2026-07-22. Every zde doc uses only these words. If a concept needs a
paragraph, it gets an entry here or it does not ship.

| Term | Means |
|---|---|
| **monitor** | one physical display |
| **workspace** | niri's unit: an endless horizontal strip of columns on one monitor |
| **column** | one or more windows stacked vertically, side by side within a workspace |
| **desk** | saved state of ALL monitors at once: workspaces + apps + policies |
| **regulars** | reserved desk name; its workspaces are reachable from every desk (singletons: comms, music, personal browser) |
| **app** | a zinc-sandboxed application (one YAML file) |
| **instance** | one running copy of an app (`app@name`); same config, own state |
| **inherited app** | an app YAML that extends another (`Inherits:`); its own security posture |
| **queue** | everything waiting for your action: agent approvals, dialogs, urgent windows |
| **picker** | the visual desk chooser: a list on the screen you are looking at, driven by up/down, `j`/`k`, or a digit |
| **panic** | one action: decoy desk + mute + silence |
| **decoy** | the harmless desk panic switches to |
| **zen** | hide bar, borders, gaps; content only |
| **guest** | unlock limited to one desk; everything else needs your password |
| **ask** | quick small-model LLM: popup or panel, never an agent |
| **pass** | derived password manager: KDF from master + service, no vault |
| **attn** | the attention router: notifications, queue, display policy |
| **target** | the media player all media actions route to (always visible) |
| **vox** | desk-scoped, sandboxed voice control |
| **doctor** | state collector + runbook-armed agent launcher; `zde doctor` is the collector half |
| **zded** | the zde background daemon: desk state, queue, notification server, IPC |
| **shell** | the GUI layer (bar, overlays, widgets); a thin adapter over zded IPC |
| **palette** | the command surface: every action reachable by name |
