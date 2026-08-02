# zde - Delivery

How zde reaches a machine. Frozen 2026-07-22.

## Three layers

| Layer | Contains | Owned by |
|---|---|---|
| 0 system | kernel, drivers/firmware, modprobe options, podman + subuids, greetd, PipeWire, portals, kanata permissions, laptop hardware (power, radios), nix | `nix/system.nix` (NixOS) or `install.sh` (portable) |
| 1 user env | niri config (generated keymap), zded, shell, zinc tools, fonts, theme | `nix/home.nix` - one module, shared by both paths |
| 2 apps | zinc containers, app YAMLs, desk manifests | zinc's own flake, installed by layer 1; digest-pinned; provisioned by hand until something seeds it |

Layer 1 is the same home-manager module everywhere, so "which distro" only
affects layer 0.

Layer 2 arrives as a flake. zinc 0.8.1 ships `homeModules.zinc`, so the tools
land on PATH the way everything else in layer 1 does - the host template pins
zinc to its own tag and enables it, and zde pins the same tag so its smoke test
runs against the binary rather than a paragraph.

Where an instance keeps state is zinc's to say and readable rather than
assumed: `zcr where <app>[@<instance>]` prints it, honouring `XDG_STATE_HOME`,
and zde asks rather than joining that path itself. Two copies of a layout drift
the first time either side moves one.

What is still by hand: an app YAML gets into `~/.config/zinc/apps` because
somebody put it there, and moving a digest is somebody retyping a tag - zinc's
resolver returns an already-pinned reference unchanged and does not record the
tag it came from. Both landed in zinc 0.8.2 - `zc init` seeds a store with
worked examples, and `zcr recheck` re-resolves a `SourceTag` and reports
whether the digest moved. Neither is wired into zde. Nothing about zde's own update
path (`update.md`) covers layer 2 yet.

Laptop hardware (upower, power profiles, the network radio) is a toggle in the
system module: `zde.laptop.enable`. The bluetooth radio has a switch of its
own, `zde.bluetooth.enable`, off by default and turned on by the laptop one
too, so a desktop can ask for bluetooth without taking a power daemon and
NetworkManager with it. Desktops leave both off, which is a machine with no
radio listening on its behalf; the 0.4 laptop profile (roadmap) builds the rest
of its UIs on top of them in layer 1.

## Reference platform: NixOS

- Rollback is the real stability: a broken update is one reboot away from
  the previous generation.
- Drivers and modprobe are declarative (`hardware.*`, nixos-hardware,
  `boot.extraModprobeConfig`) in the same reviewable file as everything else.
- A flake with pinned inputs is zinc's digest-pinning applied to the OS.
- Channel strategy: nixpkgs stable for the base (`nixos-26.05`, with
  home-manager matched to it). niri comes from it too, for as long as stable
  carries the release zde wants: that keeps the compositor on the same mesa as
  the system, which is what keeps a session off a black screen. A fast-moving
  piece becomes its own pinned input the day stable stops carrying what zde
  needs - Quickshell will, when the shell lands. Either way the lock is the
  pin, and moving it is [`update.md`](update.md).

## Portable path: any distro

`install.sh` bootstraps an existing system (the user's own distro): install
nix, rootless podman + subuids, kanata system bits, then
`home-manager switch --flake`. Reuses layer 1 unchanged; its maintenance
cost is the script alone.

## Artifacts

- flake outputs: `nixosModules.zde`, `homeModules.zde`,
  `nixosConfigurations.zde-live`, `packages`, `checks`, devShells, formatter.
- `zde-live.iso` (`nix build .#zde-iso`, [`nix/live.nix`](../nix/live.nix)): a
  live image that boots into zde and touches no disk. Not an installer - it
  exists so the by-hand list ([`verify.md`](verify.md)) has a machine to run
  on, which is the one thing CI cannot provide. Too big to build per pull
  request: being a flake package is what keeps it checked anyway, since
  `nix flake check` instantiates every package and a broken image fails there
  without anything being built.
- `zde.iso`: a NixOS installer preseeded with the zde configuration, CI-built
  and checksummed. Once 0.1 is usable. Until then the live image installs a
  machine by hand, which is [`install.md`](install.md).
- `install.sh`: the portable bootstrapper, checksummed alongside.

## Sequencing

Flake skeleton first; the home module grows as each 0.1 component lands; the
ISO and `install.sh` ship once 0.1 is actually usable. An ISO of an
environment that does not exist yet is pointless.

First landed: the home module writes `~/.config/niri/`, and the split there is
ownership. `config.kdl` holds what zde fixes and includes three files:
`binds.kdl`, generated from `common/keymap/keymap.yaml`; `local.kdl`, the
host's own settings from `zde.niri.extraConfig`; and `dynamic.kdl`, which zded
writes while the session runs. The first three are read-only symlinks into the
store, which is exactly why the fourth cannot be - capture-block rules and the
binds a desk releases have to change without a rebuild, and niri reloads the
lot when any of them is written. `nix/zde-config.nix` runs `niri validate` over
the assembled tree at build time, so a broken keymap or a base that does not
parse fails the build rather than the login.

On top of it, layer 0 enables niri and starts it through
greetd (tuigreet, no graphical display manager), so the machine boots into the
compositor that reads layer 1's config. What it boots into is no longer bare:
roadmap 0.1 has landed, so the generated binds spawn tools that are there, and
the ones nobody has written yet say so in the palette rather than in silence.

Two things check that, since no dev host has nix. `nix flake check` evaluates a
throwaway host with both layers on (`nix/test-host.nix`), which is what catches
a module error, but it builds nothing. The smoke test (`nix build .#zde-smoke`,
its own workflow) boots that host in QEMU and checks that the greeter is up,
the session pieces are installed where systemd and the portals look for them,
home-manager wrote the generated config, and niri's own parser accepts it. A
compositor does run there, nested inside a headless cage on llvmpipe, so zded
and zde are exercised against real niri IPC rather than a fake.

What that VM has no way to be is a machine: no GPU, no keyboard, one screen,
nobody watching. Everything that needs one is [`verify.md`](verify.md), and
`zde-live.iso` above is how it gets one.
