# zde system layer (docs/delivery.md, layer 0). NixOS reference platform.
# Each 0.1 component lands here as a small block; nothing speculative lives
# here in advance.
{
  lib,
  config,
  pkgs,
  ...
}:
let
  cfg = config.zde;
in
{
  options.zde.enable = lib.mkEnableOption "the zde system layer";

  config = lib.mkIf cfg.enable {
    # Rootless podman: what zcr runs apps with.
    virtualisation.podman.enable = true;

    # Audio: PipeWire with the usual compatibility layers.
    services.pipewire = {
      enable = true;
      alsa.enable = true;
      pulse.enable = true;
    };

    # Land with their roadmap phases (see docs/roadmap.md, verify list):
    # - niri session + portals (pinned input, not nixpkgs stable)
    # - greetd
    # - kanata + uinput/udev permissions
    # - NVIDIA / firmware quirks via nixos-hardware
  };
}
