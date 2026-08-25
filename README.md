# zde-niri
## Zinc Desktop Environment - Niri based

> From paranoids to paranoids)

A keyboard-first desktop environment where every user-facing app runs under
**zinc** sandboxing (rootless Podman, fail-closed) as defined by the Zinc app
schema.

This repo builds and maintains the **niri** variant only, on
[niri](https://github.com/YaLTeR/niri) (scrollable tiling).

A **hypr** variant (on [Hyprland](https://hyprland.org)) is possible on the
same zinc base and the same `common/` material, but it is not maintained here.
If you want to maintain a hypr version, you are welcome - open an issue or
reach out.

## Where it is

Early: the spatial model works, the shell is most of what 0.1 asked for, and
whole groups of the cheatsheet are still silent.

**Working.** Desks over niri's workspaces - declared in a manifest, adopted
from whatever you open, rotated between, scrolled within a band a desk cannot
be scrolled out of, and carried into and back out of the shared regulars.
Entering a desk starts the apps its manifest declares, and one that says what
its window calls itself opens on the workspace the desk pins it to rather than
in front of you. A queue you can drop a reminder into and jump back to where it
was written, and three attn modes that decide what interrupts you and never
what is kept. `zded` holds the session's notification name, so an app that has
never heard of zde arrives on the desk you were standing on rather than only in
a popup nobody was looking at - and in a popup as well, where you are looking,
carrying the sender's own buttons, gone again in a few seconds. That card never
takes the keyboard on its own: `Mod+Ctrl+n` is the one key that hands it over,
because a surface that grabbed on every arrival would be taking your keystrokes
at the choice of any app on the bus. The shell is a bar - the queue, the mode,
the mic, whether something is holding the screen awake, the link, the battery,
the clock - and the surfaces over it: the desk
picker and the window jump, the notification centre and that popup, the wifi
list you join a network from, the palette that runs any action by name and marks
the ones that do nothing yet, the power menu that says what a log out is
about to close before it asks, ask, which puts a question to the tier a machine
names and streams the answer back, and the clipboard history on `Mod+v` -
bounded, in memory only, expiring on its own, and never recording an entry a
password manager marks as a secret. One keymap file generates the niri binds and the
cheatsheet together, so the two cannot drift. The sandbox is installed rather
than described: zinc is a pinned input, so a zde machine has `zcr`, `zc` and
`zlg` and the rootless podman under them.

**Missing.** The apps: the sandbox is installed and nothing here defines
anything to put in it, so what `Mod+t` starts is an ordinary host program until
you write one. pass, media, the modes beyond Normal,
launch-at, the calendar and the wallpapers are keys that do nothing, silently - a bind whose command is not written yet prints
usage to a stderr nobody reads, which is why the palette marks them rather than
hiding them. Bluetooth is `zde system bluetooth` and nothing on a screen; ask
has no tier, and `Mod+e` no editor, until a machine names them.
[`docs/verify.md`](docs/verify.md) has the live-versus-silent split key by
key; [`docs/roadmap.md`](docs/roadmap.md) has the order the rest arrives in.

## Try it

There is no installer yet, and nothing here belongs on a machine you work on.
What there is is a live image: it boots, it touches no disk, and everything
under the session is the same two layers a real install would get.

```sh
nix build .#zde-iso     # gigabytes, and the better part of an hour
```

Boot `result/iso/zde-live.iso` in a VM or write it to a stick, log in as
**zde / zde**, and open a terminal with **Mod+Return**. To keep it,
[`docs/install.md`](docs/install.md) is the runbook.

Once installed, an update is two deliberate steps and nothing automatic
([`docs/update.md`](docs/update.md)):

```sh
cd /etc/nixos
sudo nix flake update zde
sudo nixos-rebuild switch --flake .#zdebox
```
[`docs/verify.md`](docs/verify.md) is what to try on it, in the order worth
trying, and it says plainly what is expected to be missing so an evening is
not spent rediscovering that.

## License

[Apache-2.0](LICENSE). Third-party attributions: [`NOTICE.md`](NOTICE.md).

## Docs

- [`docs/vision.md`](docs/vision.md) - what zde is: principles, components,
  security model, the scenario catalog, what zde asks of zinc.
- [`docs/model.md`](docs/model.md) - the spatial model (desks over niri) and
  the action map.
- [`docs/roadmap.md`](docs/roadmap.md) - phases 0.1-0.4 and the
  verify-in-prototype list.
- [`docs/delivery.md`](docs/delivery.md) - how zde ships: NixOS reference,
  portable path, ISO.
- [`docs/verify.md`](docs/verify.md) - the by-hand list: what only a real
  machine can answer, and the live image to answer it on.
- [`docs/install.md`](docs/install.md) - putting it on a machine and using it,
  including what "early" means before you give it a disk.
- [`docs/glossary.md`](docs/glossary.md) - the terms; every doc uses only
  these words.

## Layout

- `common/` - variant-agnostic apps and config (e.g. the nvim editor); kept
  compositor-neutral so a future hypr variant could reuse it.
  `common/keymap/keymap.yaml` is the keymap source of truth.
- `niri/` - pieces that only make sense for niri.
- `cmd/`, `internal/` - the Go tools: `zded`, the daemon that owns the journal
  and answers the one socket; `zde`, the client every desk bind spawns; and
  `zde-keymap`, which generates the binds and the cheatsheet from the keymap.
- `nix/` - what the flake is made of: the system module (layer 0), the
  home-manager module (layer 1), the live image, and the QEMU test that boots
  the lot and drives a real compositor through it.
- `dev/` - things for whoever is working on zde rather than running it.
  `dev/vm.sh` boots the live image in a VM and needs no nix, only qemu.

Apps (wherever they live) follow the same shape under `apps/<name>/`:

- `<name>.yaml` - the Zinc app definition (`SchemaVersion: 3`).
- `configs/` - files mounted into the container at the paths the app expects.
- `<name>.png` - the app icon, shipped with the app so it resolves on any host
  (the containerized program is never installed on the host). zde registers these
  under the icon name in each app's `Icon` field.
