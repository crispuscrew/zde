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

  # Where the state snapshot lives. The root filesystem and not the ESP: the
  # ESP is small, shared with the bootloader, and vfat - which has no ownership
  # and no permission bits, so a 0600 file cannot exist on it at all and the
  # whole privacy half of that file would be a comment. It is the same string
  # internal/doctor writes to (report.go, ReportDir), and there is no way for a
  # module to read a Go constant, so the two are kept in step by the smoke test
  # asking for the file at this path by name.
  debugDir = "/var/log/zde";

  # One directory per account that could have a session, owned by that account.
  #
  # Per account rather than one shared directory because there is then nothing
  # to configure and nothing to get wrong. A single directory has to be told
  # whose it is, and an owner set to the wrong name is a snapshot that is
  # silently never written - on the one machine where somebody deliberately
  # turned this on for a failure they were expecting.
  #
  # isNormalUser is the filter: it is what a person logs in as, and it leaves
  # out root, greeter and every system account, none of which starts a graphical
  # session. An account that gains one later gets its directory on the next
  # rebuild, which is when it gained the account.
  #
  # 0700 on each, so that one person's inventory of their own machine is not
  # readable by another account on it - and 0755 on the parent, so that
  # somebody who has mounted this disk somewhere else can list what is there
  # without having to guess at names.
  humans = lib.filterAttrs (_: u: u.isNormalUser) config.users.users;
in
{
  options.zde = {
    enable = lib.mkEnableOption "the zde system layer";
    laptop.enable = lib.mkEnableOption "laptop hardware support (battery, radios, brightness)";
    # Its own switch, and off by default, because a radio is not free. A desktop
    # that gains a listening radio nobody asked for is an attack surface nobody
    # asked for - and `zde system bluetooth` says "adapter none" on a machine
    # without one rather than failing, so leaving it off costs nothing but the
    # feature. The laptop profile turns it on as part of what a laptop is.
    bluetooth.enable = lib.mkEnableOption "the bluetooth radio (zde system bluetooth)";

    debug.enable = lib.mkEnableOption "the state snapshot (${debugDir}, zde report)" // {
      description = ''
        Keep a state snapshot on disk, for the session that does not come up.

        The failure this is for is the one docs/verify.md opens with: the screen
        is black and there is no terminal to ask anything from. The events are
        already covered - `services.journald.storage` is `persistent` by
        default, so greetd, niri, zded and the shell all have their words on the
        root filesystem after a power cut. What no log holds is *state*: which
        card was in the machine, which driver was bound to it, and which system
        generation was booted against which one was current.

        With this on, layer 0 makes ${debugDir}/<user>, 0700 and owned by that
        account, and layer 1's own `zde.debug.enable` writes one file into it
        per session start: the graphics answer, the versions, the hardware, and
        the whole of `zde doctor`. Eight are kept, 64 KiB each.

        **Both layers, deliberately.** This one only makes somewhere to write -
        a directory under /var belongs to root and a user session cannot create
        it - and layer 1 is what writes. Turning on only this one leaves an
        empty directory; turning on only layer 1's leaves `zde report` printing
        its answer to a terminal instead of writing it, which is the right thing
        for a machine with nowhere to put it and not what anybody wants on a
        machine that is about to go dark.

        On the normal build rather than in a debug image, because a differently
        built artifact tells you about the differently built artifact, and the
        failure worth catching may not happen in one.

        The file is 0600 and holds no notification text, no clipboard, nothing
        from the queue, no window titles, and nothing that names a desk declared
        `private: true`. It says so in its own header, because somebody is going
        to paste it into a bug report.
      '';
    };
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

      # The compositor, from nixpkgs stable (docs/update.md). Upstream's module
      # installs niri and its wayland-session entry, niri's user units, the
      # portals (gnome, for screencast), and the desktop basics: polkit, dconf,
      # graphics, fonts. A host that wants a different build of it sets
      # programs.niri.package, which is left alone here on purpose.
      programs.niri.enable = true;

      # rtkit gives PipeWire realtime scheduling (crackle and underrun
      # protection under load); the rest of audio is in the services block.
      security.rtkit.enable = true;

      # One block rather than a services.* line per concern, which is what
      # statix asks for once there are three of them.
      services = {
        # The backlight, which is a permission and therefore layer 0's even
        # though the binary that uses it is layer 1's. brightnessctl ships udev
        # rules that chgrp the brightness file to `video` and make it group
        # writable; installed with the package alone they go nowhere, because
        # only services.udev.packages is read. Without them the brightness keys
        # fail for the person actually pressing them, which is everyone.
        #
        # So a user who wants those keys belongs to `video`. That is the host's
        # to say and not ours: the template says it (templates/host).
        udev.packages = [ pkgs.brightnessctl ];

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
      # - kanata + uinput/udev permissions
      # - NVIDIA / firmware quirks via nixos-hardware
      # - xwayland-satellite (the niri module leaves XWayland off)
    })

    # Somewhere for the state snapshot to go, and nothing else. What writes it
    # is layer 1 (nix/home.nix, zde.debug.enable) and what reads it is a person
    # with the disk in another machine (docs/verify.md, section 1).
    #
    # tmpfiles and not systemd.tmpfiles.settings, because this is a list of
    # rules built from a set of users and the line form is what reads as one -
    # the settings form would be an attrset keyed by path, which is the same
    # thing spelled so that nobody can see the shape of it.
    (lib.mkIf (cfg.enable && cfg.debug.enable) {
      systemd.tmpfiles.rules = [
        "d ${debugDir} 0755 root root - -"
      ]
      # The account's own group and not its name: isNormalUser leaves the group
      # at `users` unless a host says otherwise, so a rule naming the username
      # as a group is a tmpfiles line that fails on the ordinary machine. At
      # 0700 the group decides nothing anyway - what it has to be is a group
      # that exists.
      ++ lib.mapAttrsToList (name: u: "d ${debugDir}/${name} 0700 ${name} ${u.group} - -") humans;
    })

    # Laptop hardware. The 0.4 laptop profile (battery widgets, wifi/bt TUIs,
    # workspace placement re-applied on dock/undock) builds on these; the
    # radios and power daemons are layer 0, the UIs are layer 1.
    (lib.mkIf (cfg.enable && cfg.laptop.enable) {
      # Battery state for the shell; profile switching for power actions.
      services.upower.enable = true;
      services.power-profiles-daemon.enable = true;

      # The network radio; nmtui and friends come with the user env. The
      # bluetooth one has its own block below, because a desktop can want that
      # without wanting NetworkManager.
      networking.networkmanager.enable = true;

      # Brightness and lid need nothing extra: brightnessctl goes through
      # logind, and logind's default lid-switch action (suspend) is what we
      # want.
    })

    # The bluetooth radio. Its own block rather than a line inside the laptop
    # one, so a desktop can ask for it without taking a power daemon and
    # NetworkManager with it - and so a laptop keeps behaving exactly as it did,
    # since the laptop profile is one of the two ways to be here.
    #
    # bluetoothd is all this needs: zded speaks to it on the system bus and is
    # the session's pairing agent itself (internal/bt), so there is no applet,
    # no tray and no second daemon to install.
    (lib.mkIf (cfg.enable && (cfg.laptop.enable || cfg.bluetooth.enable)) {
      hardware.bluetooth.enable = true;

      # And it comes up off. nixpkgs powers the radio on at boot by default,
      # which quietly turns "this machine has the bluetooth verbs" into "this
      # machine has a radio talking from the moment it starts", including while
      # it sits at a login screen in a room full of strangers. `zde system
      # bluetooth` says `powered no` and points at `power on`, so what this
      # costs is one command on the days you want it - and a default nobody
      # chose is not a decision.
      #
      # mkDefault, so a host that would rather have it up at boot says so
      # without fighting this module.
      hardware.bluetooth.powerOnBoot = lib.mkDefault false;
    })
  ];
}
