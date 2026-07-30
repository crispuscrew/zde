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

  # The nixpkgs release zde is built and tested against. It moves with the
  # flake's own input, by hand, on a release bump (docs/update.md, Bumping) -
  # this module is a plain path module and cannot read the flake that carries
  # it, which is the same reason the warning below is worth having at all.
  testedRelease = "26.05";
in
{
  options.zde = {
    enable = lib.mkEnableOption "the zde system layer";
    laptop.enable = lib.mkEnableOption "laptop hardware support (battery, radios, brightness)";
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      # Which nixpkgs is actually evaluating this. zde pins one in its own
      # flake and that pin binds nothing here: a module is evaluated by
      # whichever nixpkgs imports it, so a machine on unstable runs zde
      # against unstable and zde's lock never gets a vote. That is fine, and
      # untested, and the only bad version of it is the silent one - a
      # renamed option or a moved niri surfaces three layers down as
      # something inexplicable. The template (nix flake init -t
      # github:crispuscrew/zde#host) is how to not be here.
      warnings = lib.optional (config.system.nixos.release != testedRelease) ''
        zde is tested against nixos-${testedRelease}, and this system is ${config.system.nixos.release}.
        Nothing is known to be broken; nothing is known to work either.
      '';

      # Rootless podman: what zcr runs apps with.
      virtualisation.podman.enable = true;

      # The backlight, which is a permission and therefore layer 0's even
      # though the binary that uses it is layer 1's. brightnessctl ships udev
      # rules that chgrp the brightness file to `video` and make it group
      # writable; installed with the package alone they go nowhere, because
      # only services.udev.packages is read. Without them the brightness keys
      # fail for the person actually pressing them, which is everyone.
      #
      # So a user who wants those keys belongs to `video`. That is the host's
      # to say, not ours: the template says it (templates/host).
      services.udev.packages = [ pkgs.brightnessctl ];

      # The compositor, from nixpkgs stable (docs/update.md). Upstream's module
      # installs niri and its wayland-session entry, niri's user units, the
      # portals (gnome, for screencast), and the desktop basics: polkit, dconf,
      # graphics, fonts. A host that wants a different build of it sets
      # programs.niri.package, which is left alone here on purpose.
      programs.niri.enable = true;

      # No graphical display manager: greetd on its own VT, tuigreet, then
      # niri-session, the systemd-integrated entry point niri ships. The
      # session reads ~/.config/niri/config.kdl, which layer 1 generates from
      # the keymap.
      services.greetd = {
        enable = true;
        settings.default_session = {
          # mkDefault: the command is one composite string, so without it a
          # consumer wanting to add a tuigreet flag has to retype the whole
          # invocation under mkForce and stops tracking changes to it.
          command = lib.mkDefault "${lib.getExe pkgs.tuigreet} --time --remember --cmd niri-session";
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
