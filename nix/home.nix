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

  # Named once because two defaults want it: the terminal itself, and the help
  # key, which is a pager in a terminal.
  defaultTerminal = [ (lib.getExe pkgs.foot) ];
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

    debug.enable = lib.mkEnableOption "writing a state snapshot when the session starts" // {
      description = ''
        Write a state snapshot into /var/log/zde when the session starts.

        The other half of `zde.debug.enable` in layer 0, which is what makes
        somewhere to write it (nix/system.nix says why it takes both). This one
        adds one unit, `zde-report.service`: a oneshot that runs `zde report`
        after the session is up.

        What it costs the session is nothing. It is pulled by
        graphical-session.target and starts after it, so nothing waits on it,
        and every probe inside it is bounded and answers "could not ask" rather
        than hanging - which is the whole design, because the machine it runs on
        is the one that is already wrong.

        What it holds: whether niri reached a real renderer and on which device,
        the versions, the hardware, and every check `zde doctor` makes. What it
        never holds: notification text, clipboard content, the queue, window
        titles, or anything naming a desk that declares `private: true`.

        `zde report` writes one by hand at any time, from a terminal or from
        Ctrl+Alt+F2, and it is the same file - so this option is about the boot
        where nobody got the chance to type it.
      '';
    };

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
        zde.apps.browser = [ "zcr" "run" "browser" "--exec" ];
        ```

        which needs zinc's own home-manager module alongside this one
        (`programs.zinc.enable`, as the host template and the live image wire
        it). Without --exec zcr prints the launch plan and exits, which from a
        keybind looks exactly like nothing happening.

        No instance in that argv: zinc 0.8.1 addresses an instance as
        `browser@work` and `zcr run` does not take one yet (`run --instance` is
        0.8.2), so a desk manifest's instance is declared and not yet threaded
        through a launch. `zde desk apps` prints the address and where zinc says
        that instance keeps its state.

        The defaults below are host commands because a machine has to be usable
        before it has any apps defined.
      '';
    };

    ask.tiers = lib.mkOption {
      type = lib.types.attrsOf (lib.types.listOf lib.types.str);
      default = { };
      example = {
        provider = [
          "zcr"
          "run"
          "ask-provider"
          "--exec"
        ];
        local = [
          "zcr"
          "run"
          "ask-local"
          "--exec"
        ];
      };
      description = ''
        What ask runs for each tier (`Mod+a`, `Mod+Shift+a`). The same seam as
        `zde.apps` and the same shape - a name to an argv - because it answers
        the same kind of question: what this machine calls that.

        A tier is a program that reads the question on stdin and writes the
        answer to stdout, streaming it if it can. zded runs it and passes what
        it says back to the window or to the terminal that asked
        (`zde ask oneshot ...`).

        **Where a question has turns before it** - the panel on `Mod+Shift+a`,
        which keeps asking - stdin carries the conversation instead, and says
        so on its first line:

        ```
        zde-ask 1
        {"who":"person","text":"what is the capital of peru"}
        {"who":"tier","text":"Lima."}
        {"who":"person","text":"and of chile"}
        ```

        One JSON object per line, oldest first, the question last. A tier that
        does not read the first line still works for everything with nothing in
        front of it: `Mod+a`, every `zde ask` in a terminal, and the panel's
        first question are the bare question and nothing else, exactly as
        before.

        `who` is a field and not a prefix on the text deliberately. A tier has
        to be able to say which words a person typed and which it produced
        itself last time, because that is the difference between context and
        instructions - and with the role written down the left margin of a
        transcript, an answer containing a line that begins `person:` would
        arrive as something a person said. JSON escaping is what makes that
        impossible, so nothing inside a turn can end it early.

        A conversation is capped at 64 KiB, everything the tier is handed on one
        run. Past that the question is refused, saying so, rather than the
        oldest turns being dropped to fit: a panel showing an exchange the tier
        can no longer see is a screen that lies about what was asked.
        `ctrl+n` in the panel starts a fresh conversation.

        Three names have keys on them: `provider` is the default and the fast
        one, `local` is the private tier, and `escalate` is for a question the
        first two got wrong. Any other name works and nothing presses it.

        **There is no default, deliberately.** An unset tier is unset, and
        asking on one says which option to set rather than quietly sending a
        question somewhere. Nothing in zde holds an API key or speaks anyone's
        API: the tier is a command, and the tier that talks to a provider
        belongs in a zinc container whose egress is that one API host, the way
        the local tier belongs in one with no network at all (docs/vision.md,
        section 2). Those app definitions are layer 2 and are still put on a
        machine by hand (docs/delivery.md), so this points at whatever that
        machine actually has:

        ```nix
        zde.ask.tiers.provider = [ "zcr" "run" "ask-provider" "--exec" ];
        ```

        No history is kept anywhere, by zded or by the window: the answer is on
        the screen until the window closes, and nothing writes it down. The
        panel's turns are held by the window that is showing them and sent again
        with each question, so zded has forgotten a conversation by the time the
        answer ends and closing the window is still the whole of forgetting.

        A run is over when the tier exits, and what the tier started is stopped
        with it: the command runs in its own process group, and the group is
        killed when the answer ends, when it runs out of time, or when there is
        nobody left to read it. A tier is therefore a program that answers a
        question and not a place to start a service from - one left running in
        the background here goes when the answer does. One answer is capped at
        256 KiB, because the window holds all of it on the thread that draws the
        bar.
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
      terminal = lib.mkDefault defaultTerminal;
      # No editor default, on purpose. Every other default here is a program
      # this module also installs, and layer 1 has no editor to install: the
      # one zde ships is a zinc container (common/apps/nvim), which is layer
      # 2's to build and pin, and putting a second editor on the host to cover
      # for it would be two editor stories and a package nobody asked for.
      #
      # What used to be here was `foot -e nvim` on a machine with no nvim, so
      # Mod+e opened a terminal that closed the instant it could not find the
      # program. That is the worst way for a key to fail: nothing is left on
      # the screen to read, and the check that a bind's command is installed
      # sees only the terminal in front of it.
      #
      # With no default the same key says `no app called "editor"; there is
      # [help lock terminal]`, which names what this machine can start. It goes
      # to niri's log rather than to a surface, which is the honest state of
      # affairs until the shell has somewhere to print - and it is the
      # difference between a machine that can be diagnosed and one that only
      # blinks. The keymap already says the editor is the one the machine names
      # rather than one zde picks (common/keymap/keymap.yaml), so this is the
      # default catching up with what the rest of the system says.
      #
      # A machine with an editor says so in one line:
      #
      #   zde.apps.editor = [ "foot" "-e" "hx" ];
      #   zde.apps.editor = [ "zcr" "run" "nvim" "--exec" ];  # when zinc has it
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
      # What Mod+slash opens. A key that shows you the keys has to draw
      # somewhere, and until the shell has a surface for it the somewhere is a
      # terminal - so help is an app like any other, which also means a machine
      # that prefers a different one says so in the same place it says
      # everything else.
      #
      # The default terminal and not cfg.apps.terminal: a default that reads
      # the option it is a default for is infinite recursion, which the module
      # system finds at eval and nobody wants to meet at a rebuild. So a
      # machine that changes its terminal and wants help in that one sets
      # zde.apps.help too - `zde app list` prints both argvs side by side,
      # which is where that is noticed.
      #
      # -e is how every terminal worth the name takes a command. The pager is
      # pointed straight at the file `zde keys` prints rather than at
      # `zde keys | less`, because a pipe needs a shell, and a shell inside a
      # default argv is a quoting problem waiting for somebody's terminal to be
      # the one that disagrees.
      #
      # Paged because the keymap is a screen and a bit: without it the answer
      # scrolls past and the terminal closes on it.
      help = lib.mkDefault (
        defaultTerminal
        ++ [
          "-e"
          (lib.getExe pkgs.less)
          "-R"
          "${config.xdg.configHome}/zde/keymap.txt"
        ]
      );
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
      # The cheatsheet the help widget (system.help) will show when there is
      # one to show it.
      "zde/keymap-cheatsheet.md".source = "${zdeConfig}/keymap-cheatsheet.md";

      # What `zde keys` prints, which is what Mod+slash opens today. Installed
      # rather than rendered by the CLI: the binds a machine actually has come
      # from this build, so the list it shows should come from the same one.
      "zde/keymap.txt".source = "${zdeConfig}/keymap.txt";

      # What `zde app launch` reads. Generated, because the keymap's names and
      # the machine's programs are two different things and this is the seam.
      "zde/apps.json".text = builtins.toJSON cfg.apps;

      # And what ask reads, in the same shape for the same reason. Written even
      # when it is empty: the file being there and saying nothing is what makes
      # `Mod+a` answer with the option to set rather than with a missing file.
      "zde/ask.json".text = builtins.toJSON cfg.ask.tiers;
    };

    # dynamic.kdl is the seam zded writes through, so home-manager seeds it and
    # then keeps its hands off. Created rather than linked: a store symlink is
    # read-only, and a missing include is fatal to the whole config.
    #
    # 0600, which is the mode zded chmods it to on every write it makes
    # (internal/zded/rules.go): only niri reads it, and what ends up in it is a
    # line per pinned app naming the desk it opens on - one of which can be a
    # private desk zde keeps out of the picker, so the file next door should not
    # be publishing the name either. Seeded at 0644 the contents were only a
    # comment and nothing leaked, but the two layers were stating different
    # modes for the same file and the first zded write silently corrected one of
    # them. The layer that creates the file should create it at the mode the
    # layer that owns it keeps it at.
    home.activation.zdeNiriDynamic = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      dynamic="${config.xdg.configHome}/niri/dynamic.kdl"
      if [ ! -e "$dynamic" ]; then
        run mkdir -p "$(dirname "$dynamic")"
        run install -m 0600 ${dynamicSeed} "$dynamic"
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
      pkgs.less # the pager Mod+slash reads the keymap in
      # wl-paste and wl-copy, which are how zded watches the clipboard and puts
      # an entry back on it (Mod+v, internal/clip/wl.go - which says why zde
      # spawns these rather than speaking the data-control protocol itself).
      # Without them the daemon says so once in its log and the history stays
      # empty, and `zde clip history` says which of the two empty it is.
      pkgs.wl-clipboard
      pkgs.wireplumber # wpctl, for audio.*
      # The bar runs from the store path in its unit, so this is not what
      # starts it. It is `qs log` and `qs list`, which are the only way to find
      # out why a bar is not on screen.
      pkgs.quickshell
    ];

    # One systemd.user.services block rather than an assignment per unit, which
    # is what statix asks for once there are three of them - the same rule
    # nix/system.nix already follows for its services block.
    systemd.user.services = {
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
      zded = {
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
      zde-bar = {
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

      # The state snapshot, written once when the session starts (zde.debug).
      #
      # Its own unit and not a line in zded, for the reason the report is its own
      # verb: zded is one of the things that can be what is wrong, and a snapshot
      # taken by the daemon would go missing on exactly the session that needed
      # it. A unit also means the attempt itself is in the journal, which is the
      # answer when there is no file at all.
      #
      # Hung off graphical-session.target the way zded and the bar are, and After
      # it rather than Before anything: a oneshot nothing waits on cannot delay a
      # login, and this must never be the reason a desktop is slow to appear.
      #
      # That target is also the honest place for it. niri.service is BindsTo and
      # Before graphical-session.target and it is Type=notify, so the target is
      # reached the moment niri says it is ready - which is precisely the state
      # the misleading failure is in: sockets open, nothing drawn. A niri that
      # never gets that far leaves no session for any user unit to run in, and
      # what answers there is greetd's journal (docs/verify.md, section 1).
      #
      # RemainAfterExit so `systemctl --user status zde-report` still says what
      # happened after it has finished, rather than reading as a unit that was
      # never started.
      zde-report = lib.mkIf cfg.debug.enable {
        Unit = {
          Description = "zde state snapshot: what this machine looked like when the session started";
          Documentation = "https://github.com/crispuscrew/zde";
          PartOf = [ "graphical-session.target" ];
          After = [ "graphical-session.target" ];
        };
        Service = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = "${zdeTools}/bin/zde report";
          # No Restart. It is a snapshot of one moment, and a retry would either
          # write a second file for the same boot or fail again for the same
          # reason - and the reason is already a line in this unit's own log.
        };
        Install.WantedBy = [ "graphical-session.target" ];
      };
    };
  };
}
