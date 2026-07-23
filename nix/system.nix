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
  options.zde = {
    enable = lib.mkEnableOption "the zde system layer";
    laptop.enable = lib.mkEnableOption "laptop hardware support (battery, radios, brightness)";
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      # Rootless podman: what zcr runs apps with.
      virtualisation.podman.enable = true;

      # Audio: PipeWire with the usual compatibility layers; rtkit gives it
      # realtime scheduling (crackle/underrun protection under load).
      security.rtkit.enable = true;
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
    })

    # Laptop hardware. The 0.4 laptop profile (battery widgets, wifi/bt TUIs,
    # workspace placement re-applied on dock/undock) builds on these; the
    # radios and power daemons are layer 0, the UIs are layer 1.
    (lib.mkIf (cfg.enable && cfg.laptop.enable) {
      # Battery state for the shell; profile switching for power actions.
      services.upower.enable = true;
      services.power-profiles-daemon.enable = true;

      # The radios; nmtui/bluetuith and friends come with the user env.
      networking.networkmanager.enable = true;
      hardware.bluetooth.enable = true;

      # Brightness and lid need nothing extra: brightnessctl goes through
      # logind, and logind's default lid-switch action (suspend) is what we
      # want.
    })
  ];
}
