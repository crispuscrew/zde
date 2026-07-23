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
- Channel strategy: nixpkgs stable for the base; niri and Quickshell as
  pinned flake inputs bumped by hand.

## Portable path: any distro

`install.sh` bootstraps an existing system (the user's own distro): install
nix, rootless podman + subuids, kanata system bits, then
`home-manager switch --flake`. Reuses layer 1 unchanged; its maintenance
cost is the script alone.

## Artifacts

- flake outputs: `nixosModules.zde`, `homeModules.zde`, devShell, formatter;
  later `nixosConfigurations.zde`, the ISO, `checks`.
- `zde.iso`: a NixOS installer preseeded with the zde configuration, CI-built
  and checksummed. Later polish: a live session booting straight into zde.
- `install.sh`: the portable bootstrapper, checksummed alongside.

## Sequencing

Flake skeleton first; the home module grows as each 0.1 component lands; the
ISO and `install.sh` ship once 0.1 is actually usable. An ISO of an
environment that does not exist yet is pointless.
