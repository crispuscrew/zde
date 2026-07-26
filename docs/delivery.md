# zde - Delivery

How zde reaches a machine. Frozen 2026-07-22.

## Three layers

| Layer | Contains | Owned by |
|---|---|---|
| 0 system | kernel, drivers/firmware, modprobe options, podman + subuids, greetd, PipeWire, portals, kanata permissions, laptop hardware (power, radios), nix | `nix/system.nix` (NixOS) or `install.sh` (portable) |
| 1 user env | niri config (generated keymap), zded, shell, zinc tools, fonts, theme | `nix/home.nix` - one module, shared by both paths |
| 2 apps | zinc containers, app YAMLs, desk manifests | zcr at first run; digest-pinned |

Layer 1 is the same home-manager module everywhere, so "which distro" only
affects layer 0.

Laptop hardware (upower, power profiles, wifi/bt radios) is a toggle in the
system module: `zde.laptop.enable`. Desktops leave it off; the 0.4 laptop
profile (roadmap) builds its UIs on top of it in layer 1.

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

- flake outputs: `nixosModules.zde`, `homeModules.zde`, `packages`, `checks`,
  devShells, formatter; later `nixosConfigurations.zde`, the ISO.
- `zde.iso`: a NixOS installer preseeded with the zde configuration, CI-built
  and checksummed. Later polish: a live session booting straight into zde.
- `install.sh`: the portable bootstrapper, checksummed alongside.

## Sequencing

Flake skeleton first; the home module grows as each 0.1 component lands; the
ISO and `install.sh` ship once 0.1 is actually usable. An ISO of an
environment that does not exist yet is pointless.

First landed: the home module generates `~/.config/niri/config.kdl` from
`common/keymap/keymap.yaml` (a base config plus the generated binds, assembled
by `nix/zde-config.nix`) and installs the cheatsheet. `nix flake check` builds
the config, so a broken keymap fails CI. The base config's niri syntax is only
validated on a real niri (roadmap), since assembly just concatenates it.

On top of it, layer 0 enables niri and starts it through
greetd (tuigreet, no graphical display manager), so the machine boots into the
compositor that reads layer 1's config. What it boots into is still bare: the
generated binds spawn zde tools that land with roadmap 0.1.

Two things check that, since no dev host has nix. `nix flake check` evaluates a
throwaway host with both layers on (`nix/test-host.nix`), which is what catches
a module error, but it builds nothing. The smoke test (`nix build .#zde-smoke`,
its own workflow) boots that host in QEMU and checks that the greeter is up,
the session pieces are installed where systemd and the portals look for them,
home-manager wrote the generated config, and niri's own parser accepts it. The
compositor itself never starts there - a build sandbox has no GPU and niri has
no software renderer - so a running session stays a real-hardware item.
