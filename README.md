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

Early: the spatial model works, and most of what you would press does not.

**Working.** Desks over niri's workspaces - declared in a manifest, adopted
from whatever you open, rotated between, scrolled within a band a desk cannot
be scrolled out of, and carried into and back out of the shared regulars. A
queue you can drop a reminder into and jump back to where it was written.
`zded` holds the session's notification name, so an app that has never heard
of zde arrives on the desk you were standing on instead of in a popup nobody
was looking at. One keymap file generates the niri binds and the cheatsheet
together, so the two cannot drift. The sandbox is installed rather than
described: zinc is a pinned input, so a zde machine has `zcr`, `zc` and `zlg`
and the rootless podman under them.

**Missing.** Most of the shell: there is a bar and a desk picker, and no
notification centre and no palette - which is what most of the cheatsheet is
waiting on, and a bind whose command is not written yet is a key that does
nothing, silently. And the apps: a desk manifest declares what it is for, in
the address zinc takes for an instance, and nothing launches it yet.
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

- `<name>.yaml` - the app definition (schema v2, see
  `../hyprzinc/common/domain/schema/schema.go`).
- `configs/` - files mounted into the container at the paths the app expects.
- `<name>.png` - the app icon, shipped with the app so it resolves on any host
  (the containerized program is never installed on the host). zde registers these
  under the icon name in each app's `Icon` field.
