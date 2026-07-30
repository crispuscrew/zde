# zde user layer (docs/delivery.md, layer 1). The single home-manager module
# shared by the NixOS reference and the portable path - the "which distro"
# question never reaches this file.
{
  lib,
  config,
  pkgs,
  ...
}:
let
  cfg = config.zde;
  # The niri config, the generated binds and the cheatsheet, built from the
  # keymap source of truth. Same derivation the flake exposes.
  zdeConfig = pkgs.callPackage ./zde-config.nix { };
  zdeTools = pkgs.callPackage ./zde.nix { };
  zdeShell = pkgs.callPackage ./shell.nix { };

  # Whether anything under zde.niri.xkb was set. niri merges a later input
  # block, so writing an empty one would be harmless - but it would also put a
  # section in a person's local.kdl that says nothing, which is the kind of
  # thing that gets copied around and then wondered about.
  layoutSet = cfg.niri.xkb.layout != "" || cfg.niri.xkb.options != "";

  # What dynamic.kdl says before zded has written anything into it. niri treats
  # a missing include as a fatal error, so this file has to exist from the
  # first boot, and it cannot be a store symlink because zded writes it.
  dynamicSeed = pkgs.writeText "zde-niri-dynamic.kdl" ''
    // Written by zded while the session runs: capture-block window rules, the
    // binds a desk releases. Empty until then.
    //
    // home-manager creates this file once and never touches it again - it is
    // the one part of the niri config that is not generated, because it has to
    // change without a rebuild. Anything you want to keep belongs in
    // zde.niri.extraConfig, which lands in local.kdl beside this.
  '';
in
{
  options.zde = {
    enable = lib.mkEnableOption "the zde user environment";

    niri.xkb = {
      layout = lib.mkOption {
        type = lib.types.str;
        default = "";
        example = "us,ru";
        description = ''
          The xkb layouts, comma separated. Empty leaves niri on xkb's default,
          which is us.

          Mod+space is bound to niri's switch-layout, so this is what it
          switches between: with one layout that key does nothing, which is
          the state of every zde session so far. A second language is the
          difference between a machine you can write to somebody in and one
          you cannot.
        '';
      };
      options = lib.mkOption {
        type = lib.types.str;
        default = "";
        example = "grp:caps_toggle,compose:ralt";
        description = ''
          xkb options, comma separated. `fkeys:basic_13-24` belongs here on a
          machine running the input layer: without it the keys it emits arrive
          as XF86Tools rather than F13.
        '';
      };
    };

    apps = lib.mkOption {
      type = lib.types.attrsOf (lib.types.listOf lib.types.str);
      default = { };
      example = {
        terminal = [ "alacritty" ];
        editor = [
          "foot"
          "-e"
          "hx"
        ];
      };
      description = ''
        What the keymap's logical names run. `Mod+t` is `app.launch terminal`
        and `Mod+e` is `app.launch editor`, because the keymap binds actions
        rather than programs - so this is where a machine says which terminal
        it means, without editing the keymap everything else is generated from.

        An argv rather than a command line, so nothing has to agree about
        quoting.

        A sandboxed app is one of these too - the argv is the runner:

        ```nix
        zde.apps.browser = [ "zcr" "run" "browser@work" ];
        ```

        which needs zinc's own home-manager module alongside this one
        (`programs.zinc.enable`, as the host template wires it). The defaults
        below are host commands because a machine has to be usable before it
        has any apps defined.
      '';
    };

    niri.extraConfig = lib.mkOption {
      type = lib.types.lines;
      default = "";
      example = ''
        output "eDP-1" {
            scale 2
        }
      '';
      description = ''
        Host-specific niri config, written to ~/.config/niri/local.kdl and
        included after the generated binds. This is where outputs, an xkb
        layout, or a window rule go: everything zde does not fix, without
        forking the config it does.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # A terminal, and the name the keymap uses for one. Defaults rather than
    # requirements: a desktop whose Mod+t does nothing is not one anybody can
    # start using, and foot is small, starts without a GPU, and is what the live
    # image and the tests already run. Name another and this stops being used.
    zde.apps = {
      terminal = lib.mkDefault [ (lib.getExe pkgs.foot) ];
      editor = lib.mkDefault [
        (lib.getExe pkgs.foot)
        "-e"
        "nvim"
      ];
      # A machine that leaves the house needs this working on its first day,
      # and it is one of the few keys whose absence is discovered at the worst
      # possible moment. swaylock because niri's own module already configures
      # PAM for it (nixpkgs, programs/wayland/wayland-session.nix), so it can
      # actually authenticate: a locker that cannot is a locker that locks you
      # out rather than locking your screen.
      lock = lib.mkDefault [
        (lib.getExe pkgs.swaylock)
        "--daemonize"
        "--ignore-empty-password"
        "--show-failed-attempts"
      ];
    };

    # The niri config, and the binds it includes. Regenerated on every switch,
    # so keymap.yaml is the only place binds are edited (the file itself says
    # "Do not edit"). Forced, because niri writes a default config.kdl itself
    # the first time it starts without one, and an unmanaged file in that spot
    # stops every later home-manager generation.
    xdg.configFile = {
      "niri/config.kdl" = {
        source = "${zdeConfig}/niri-config.kdl";
        force = true;
      };
      "niri/binds.kdl" = {
        source = "${zdeConfig}/binds.kdl";
        force = true;
      };
      # The host's own half, still declarative.
      "niri/local.kdl" = {
        text = ''
          // Generated from zde.niri.xkb and zde.niri.extraConfig. Edit those,
          // not this.
          ${lib.optionalString layoutSet ''
            input {
                keyboard {
                    xkb {
                        ${lib.optionalString (cfg.niri.xkb.layout != "") ''layout "${cfg.niri.xkb.layout}"''}
                        ${lib.optionalString (cfg.niri.xkb.options != "") ''options "${cfg.niri.xkb.options}"''}
                    }
                }
            }
          ''}
          ${cfg.niri.extraConfig}
        '';
        force = true;
      };
      # The cheatsheet the help widget (system.help) shows.
      "zde/keymap-cheatsheet.md".source = "${zdeConfig}/keymap-cheatsheet.md";

      # What `zde app launch` reads. Generated, because the keymap's names and
      # the machine's programs are two different things and this is the seam.
      "zde/apps.json".text = builtins.toJSON cfg.apps;
    };

    # dynamic.kdl is the seam zded writes through, so home-manager seeds it and
    # then keeps its hands off. Created rather than linked: a store symlink is
    # read-only, and a missing include is fatal to the whole config.
    home.activation.zdeNiriDynamic = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      dynamic="${config.xdg.configHome}/niri/dynamic.kdl"
      if [ ! -e "$dynamic" ]; then
        run mkdir -p "$(dirname "$dynamic")"
        run install -m 0644 ${dynamicSeed} "$dynamic"
      fi
    '';

    # What the generated binds actually shell out to. The keymap is the source
    # of truth for the chords, which makes it a source of truth for the
    # binaries too: a bind whose command is not installed is a key that does
    # nothing, silently. The smoke test checks the two against each other.
    #
    # Grows with roadmap 0.1: the shell, and zinc's own tools (zcr, zc, zlt) -
    # which zde does not pin, package or install today.
    home.packages = [
      zdeTools # zded, zde
      pkgs.foot # the default terminal, and what Mod+t runs unless told otherwise
      pkgs.swaylock # the default screen lock (Mod+Ctrl+semicolon)
      pkgs.brightnessctl # system.brightness-up/dn
      pkgs.wireplumber # wpctl, for audio.*
      # The bar runs from the store path in its unit, so this is not what
      # starts it. It is `qs log` and `qs list`, which are the only way to find
      # out why a bar is not on screen.
      pkgs.quickshell
    ];

    # The daemon, started with the session. Every bind in the desk group is a
    # `zde` that talks to it, so without this the session comes up and none of
    # them answers.
    #
    # Hung off graphical-session.target rather than niri.service: niri is
    # Before= that target and imports WAYLAND_DISPLAY and NIRI_SOCKET into the
    # user manager on its way up, so a unit that waits for the target starts
    # into an environment that can already find the compositor. PartOf takes it
    # down with the session, which is what keeps a zded from an old session
    # from holding the socket the next one wants.
    systemd.user.services.zded = {
      Unit = {
        Description = "zde daemon: the journal, the desks, and the socket everything asks";
        Documentation = "https://github.com/crispuscrew/zde";
        PartOf = [ "graphical-session.target" ];
        After = [ "graphical-session.target" ];
      };
      Service = {
        ExecStart = "${zdeTools}/bin/zded";
        # It answers keys. A daemon that died on one bad reply and stayed dead
        # would leave every desk key silent until the next login, and the
        # journal it replays on the way back up is what makes restarting safe.
        Restart = "on-failure";
        RestartSec = 1;
      };
      Install.WantedBy = [ "graphical-session.target" ];
    };

    # The bar. Same shape as zded and for the same reasons, with one addition:
    # it wants zded, because everything it has to say comes from there. It
    # survives zded not being up - it says so on the bar instead, which is more
    # use than an empty strip - but starting them in the wrong order would mean
    # a bar that reads "not answering" for its first two seconds of every
    # login.
    systemd.user.services.zde-bar = {
      Unit = {
        Description = "the zde bar: the queue, and the clock";
        Documentation = "https://github.com/crispuscrew/zde";
        PartOf = [ "graphical-session.target" ];
        After = [
          "graphical-session.target"
          "zded.service"
        ];
        Wants = [ "zded.service" ];
      };
      Service = {
        ExecStart = "${pkgs.quickshell}/bin/quickshell -p ${zdeShell}/share/zde/shell/shell.qml";
        # A bar is the one thing whose absence is obvious, so restarting it is
        # never a surprise. One second, the same as zded, and not the three it
        # was: systemd gives up after 5 starts inside 10 seconds, and at three
        # seconds apart it can never see 5 in a window - so QML that fails
        # identically every time would have restarted for ever instead of
        # failing, leaving a runtime log directory behind on each try. Measured
        # on a transient unit: 3s spacing was still restarting after 45
        # seconds, 1s spacing gave up at 5.
        Restart = "on-failure";
        RestartSec = 1;
      };
      Install.WantedBy = [ "graphical-session.target" ];
    };
  };
}
