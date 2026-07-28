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
    input.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        The input layer (kanata): it rewrites what niri cannot bind - a key
        held as a layer, later the leader sequences and the modes - into keys
        no keyboard has, which niri then binds like any other.

        On by default because the keymap depends on it, and an option because
        it intercepts every keyboard on the machine, which is not something to
        do behind someone's back.
      '';
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      # Rootless podman: what zcr runs apps with.
      virtualisation.podman.enable = true;

      # The compositor, from nixpkgs stable (docs/update.md). Upstream's module
      # installs niri and its wayland-session entry, niri's user units, the
      # portals (gnome, for screencast), and the desktop basics: polkit, dconf,
      # graphics, fonts. A host that wants a different build of it sets
      # programs.niri.package, which is left alone here on purpose.
      programs.niri.enable = true;

      # rtkit gives PipeWire realtime scheduling (crackle/underrun protection
      # under load); the rest of audio is in the services block below.
      security.rtkit.enable = true;

      services = {
        # The input layer. It takes the chords niri has no way to hear - held
        # Tab today, the leader sequences and the modes later - and emits keys
        # no keyboard has, so niri binds them like anything else.
        #
        # Upstream's module runs kanata --check over this at build time, so a
        # config that does not parse fails the build rather than the session.
        # It also turns on uinput, and keeps the daemon running when it finds
        # no devices to intercept - which is what a VM, and a machine mid-boot,
        # both look like.
        kanata = lib.mkIf cfg.input.enable {
          enable = true;
          keyboards.zde = {
            # Every keyboard, including ones plugged in later. One instance, so
            # there is nothing to race with over a device.
            devices = [ ];
            config = builtins.readFile ../input/kanata.kbd;
          };
        };

        # No graphical display manager: greetd on its own VT, tuigreet, then
        # niri-session, the systemd-integrated entry point niri ships. The
        # session reads ~/.config/niri/config.kdl, which layer 1 generates from
        # the keymap.
        greetd = {
          enable = true;
          settings.default_session = {
            # mkDefault: the command is one composite string, so without it a
            # consumer wanting to add a tuigreet flag has to retype the whole
            # invocation under mkForce and stops tracking changes to it.
            command = lib.mkDefault "${lib.getExe pkgs.tuigreet} --time --remember --cmd niri-session";
            user = "greeter";
          };
        };

        # Audio: PipeWire with the usual compatibility layers.
        pipewire = {
          enable = true;
          alsa.enable = true;
          pulse.enable = true;
        };
      };

      # Land with their roadmap phases (see docs/roadmap.md, verify list):
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
