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
    # The flake points this at its pinned niri input; the nixpkgs build is the
    # fallback for anyone importing this module on its own.
    niri.package = lib.mkPackageOption pkgs "niri" { };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      # Rootless podman: what zcr runs apps with.
      virtualisation.podman.enable = true;

      # The compositor. Upstream's module installs the wayland-session entry
      # greetd launches, niri's user units, the portals (gnome, for
      # screencast), and the desktop basics: polkit, dconf, graphics, fonts.
      programs.niri = {
        enable = true;
        inherit (cfg.niri) package;
      };

      # No display manager: greetd on its own VT, tuigreet, then niri-session -
      # the systemd-integrated entry point niri ships. The session reads
      # ~/.config/niri/config.kdl, which layer 1 generates from the keymap.
      services.greetd = {
        enable = true;
        settings.default_session = {
          command = "${lib.getExe pkgs.greetd.tuigreet} --time --remember --cmd niri-session";
          user = "greeter";
        };
      };

      # Audio: PipeWire with the usual compatibility layers; rtkit gives it
      # realtime scheduling (crackle/underrun protection under load).
      security.rtkit.enable = true;
      services.pipewire = {
        enable = true;
        alsa.enable = true;
        pulse.enable = true;
      };

      # Land with their roadmap phases (see docs/roadmap.md, verify list):
      # - kanata + uinput/udev permissions
      # - NVIDIA / firmware quirks via nixos-hardware
      # - xwayland-satellite (the niri module leaves XWayland off)
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
