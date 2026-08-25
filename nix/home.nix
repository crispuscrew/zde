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

  replayRecorder = pkgs.callPackage ./replay.nix { };

  hypridle = pkgs.callPackage ./hypridle.nix { };

  lockCommand = pkgs.writeShellScript "zde-lock" ''
    exec ${lib.escapeShellArgs cfg.apps.lock}
  '';

  # Named once because two defaults want it: the terminal itself, and the help
  # key, which is a pager in a terminal.
  defaultTerminal = [ (lib.getExe pkgs.kitty) ];
  zdeShell = pkgs.callPackage ./shell.nix { };

  # Whether anything under zde.niri.xkb was set. niri merges a later input
  # block, so writing an empty one would be harmless - but it would also put a
  # section in a person's local.kdl that says nothing, which is the kind of
  # thing that gets copied around and then wondered about.
  layoutSet = cfg.niri.xkb.layout != "" || cfg.niri.xkb.options != "";

  # local.kdl, in full: the xkb block this host asked for and whatever else it
  # wrote. Named here rather than written inline below because it is handed to
  # a parser before it is handed to home-manager (nix/niri-local.nix).
  localText = ''
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

  # And the same text after niri's parser has agreed it is a config. The build
  # fails here rather than the login: a config niri refuses is a warning it
  # carries on past, into its own compiled-in defaults, which is a session with
  # none of zde's binds in it.
  localKdl = pkgs.callPackage ./niri-local.nix {
    inherit zdeConfig;
    text = localText;
  };

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
    //
    // Which is also where anything you want *checked* belongs. A rebuild runs
    // niri's parser over local.kdl before installing it; nothing can do that
    // for this file, because the whole point of it is that it changes while
    // the session runs. And niri treats a config it cannot parse as a warning
    // and carries on with its own defaults - a session where no key works,
    // including the one that opens a terminal to find out why. So after
    // editing this by hand: niri validate -c ~/.config/niri/config.kdl
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

    capture.replay.enable = lib.mkEnableOption "the 30-second screen replay buffer" // {
      description = ''
        Keep the last 30 seconds of the screen encoded in RAM and make `zde
        capture replay-clip` save them under ~/Videos/Replays.

        This is off by default because enabling it continuously captures the
        screen and spends a GPU encoder plus about 75 MiB of RAM at the fixed
        20 Mbit/s ceiling. It records video only: sound and the microphone are
        separate privacy decisions and are never enabled here.

        Capture goes through Niri's portal, never direct KMS, so
        `block-out-from "screen-capture"` remains in force. The first start may
        open the portal's source chooser; the approved source is restored on
        later sessions. Cancelling that chooser leaves the service stopped and
        it does not restart into another prompt.
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
        terminal = [ "kitty" ];
        editor = [
          "kitty"
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

        An instance goes in the address as `browser@work` (or in `--instance
        work`). Desk launches do not use this fixed logical-name argv: zde joins
        the app and instance from the manifest and gives that address to zcr.
        `zde desk apps` prints the same address and where zinc says that instance
        keeps its state.

        The defaults below are host commands because a machine has to be usable
        before it has any apps defined.
      '';
    };

    lock.preset = lib.mkOption {
      type = lib.types.str;
      default = "";
      example = "haven";
      description = ''
        The desk `system.lock-preset` switches to before it locks, so that what
        an unlock shows - and what a shoulder reads over the lock screen - is
        that desk and not whatever you were doing.

        The name of a desk, as a manifest declares it
        (`~/.config/zde/desks/<name>.yaml`). Unset is the default and locks
        where you are: `zde system lock` and `system.lock-preset` are then the
        same thing, said differently.

        A preset error never prevents the lock. A name that is unset, gone or
        declared `private: true` - which is the one desk an unlock should not
        reveal - locks where you are and says which case it was, and so does a
        desks directory holding a manifest that will not parse. Lock readiness
        itself can still refuse. A missing locker or unavailable or stale
        `LockedHint` is found before anything moves; process exit or a missing
        transition is reported after the attempted switch.
      '';
    };

    idle.oled = lib.mkEnableOption "idle display power-off on an OLED desktop monitor" // {
      description = ''
        Allow Hypridle to power monitors off after five idle minutes even when
        this machine has no laptop battery. Leave this off for ordinary desktop
        panels; the system-layer `zde.laptop.enable` declaration enables the
        same policy through its root-owned marker.
      '';
    };

    panic.decoy = lib.mkOption {
      type = lib.types.str;
      default = "";
      example = "haven";
      description = ''
        The harmless desk `desk.panic` switches to (`Mod+Shift+Escape`), so that
        somebody walking in sees that desk instead of what you were doing. The
        key mutes the output and silences the notifications at the same time,
        and pressing it again gives all three back: the desk you were on, the
        sound as it was, the mode as it was.

        The name of a desk, as a manifest declares it
        (`~/.config/zde/desks/<name>.yaml`). Unset is the default, and unlike
        the lock preset that means the key refuses: lock-preset with no preset
        still locks the screen, which is what it is for, while panic with no
        decoy has nothing to put in front of the person who has already walked
        in - and a key that muted the sound over the work it was asked to hide
        is worse than one that says it did nothing.

        The same refusal, changing nothing, for a name that is not a desk any
        more, one that names a desk declared `private: true` - the desk with the
        most to hide is the one thing panic must never show - and a desks
        directory holding a manifest that will not parse, since nothing can then
        say whether the decoy is private.

        What it hides is a screen and a sound, and it erases nothing: everything
        that arrived while it held is in the notification center afterwards, and
        the queue is as long as it was. It is for the moment somebody walks past;
        `zde system lock` is for the moment you leave the room.
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

        It is KDL and not Nix, and the difference bites in one place: a node's
        children go on their own lines. `output "eDP-1" { scale 2 }` is not a
        thing niri parses.

        A rebuild runs niri's own parser over the whole of local.kdl before it
        will install it (nix/niri-local.nix), so a mistake here costs a build
        error with the line printed. That is worth having because the
        alternative is silent: niri treats a config it cannot parse as a
        warning and comes up on its compiled-in defaults, which is a session
        with none of these binds and no key that opens a terminal.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # A terminal, and the name the keymap uses for one. Defaults rather than
    # requirements: a desktop whose Mod+t does nothing is not one anybody can
    # start using. Kitty is native Wayland, GPU-rendered, and carries the graphics
    # protocol terminal applications use. Name another and this stops being used.
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
      #   zde.apps.editor = [ "kitty" "hx" ];
      #   zde.apps.editor = [ "zcr" "run" "nvim" "--exec" ];  # when zinc has it
      # A machine that leaves the house needs this working on its first day,
      # and it is one of the few keys whose absence is discovered at the worst
      # possible moment. Hyprlock is configured below; layer 0 provides its PAM
      # service so it can authenticate rather than merely cover the screen.
      lock = lib.mkDefault [
        (lib.getExe pkgs.hyprlock)
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
      # Kitty takes the command after its own options. The pager is pointed
      # straight at the file `zde keys` prints rather than at
      # `zde keys | less`, because a pipe needs a shell, and a shell inside a
      # default argv is a quoting problem waiting for somebody's terminal to be
      # the one that disagrees.
      #
      # Paged because the keymap is a screen and a bit: without it the answer
      # scrolls past and the terminal closes on it.
      help = lib.mkDefault (
        defaultTerminal
        ++ [
          (lib.getExe pkgs.less)
          "-R"
          "${config.xdg.configHome}/zde/keymap.txt"
        ]
      );
    };

    # One Kitty process is one niri window. There is no second layer of tabs or
    # splits to manage, and no remote-control socket to cross the host/container
    # boundary. Safe text and scrollback shortcuts are put back after clearing
    # Kitty's window-management bindings.
    programs.kitty = {
      enable = true;
      shellIntegration.mode = "disabled";
      keybindings = {
        "ctrl+shift+c" = "copy_to_clipboard";
        "ctrl+shift+v" = "paste_from_clipboard";
        "ctrl+shift+up" = "scroll_line_up";
        "ctrl+shift+down" = "scroll_line_down";
        "ctrl+shift+page_up" = "scroll_page_up";
        "ctrl+shift+page_down" = "scroll_page_down";
        "ctrl+shift+home" = "scroll_home";
        "ctrl+shift+end" = "scroll_end";
      };
      settings = {
        linux_display_server = "wayland";
        allow_remote_control = false;
        listen_on = "none";
        allow_cloning = false;
        clear_all_shortcuts = true;
        enabled_layouts = "stack";
        tab_bar_style = "hidden";
        startup_session = "none";
        confirm_os_window_close = 0;

        # The terminal uses the same dark surface and status colours as ZDE's
        # own shell. Theme changes stay declarative here; they do not need a
        # Kitty control socket.
        background = "#11121a";
        foreground = "#c9ccd4";
        cursor = "#e5a23d";
        cursor_text_color = "#11121a";
        selection_background = "#2a2c37";
        selection_foreground = "#c9ccd4";
        url_color = "#9aa0ac";
        active_border_color = "#e5a23d";
        inactive_border_color = "#2a2c37";
        bell_border_color = "#e5484d";
      };
    };

    # Zinc's terminal applications run their payload through this host Kitty.
    # It is an executable, not a socket: Zinc remains the isolation boundary and
    # no Kitty control channel is mounted into a container.
    systemd.user.sessionVariables.ZINC_TERMINAL = lib.mkDefault (lib.getExe pkgs.kitty);

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
      # The host's own half, still declarative - and the only one of the three
      # whose text this machine wrote, so it is the only one that has to be
      # parsed here. A source rather than text: what lands is the derivation
      # that ran niri over it, so there is no way to install this file without
      # having checked it.
      "niri/local.kdl" = {
        source = localKdl;
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

      # And which desk an unlock should show. Written even when it is empty,
      # for the reason ask.json is: the file being there and saying nothing is
      # what makes lock-preset lock and say which option to set, rather than
      # look at a missing file and guess.
      "zde/lock.json".text = builtins.toJSON { preset = cfg.lock.preset; };

      # The system layer supplies the laptop declaration; this file is only the
      # explicit desktop OLED override.
      "zde/idle.json".text = builtins.toJSON { oled = cfg.idle.oled; };

      "hypr/hyprlock.conf".text = ''
        general {
            hide_cursor = true
        }

        background {
            monitor =
            color = rgb(11121a)
        }

        label {
            monitor =
            text = Locked by ZDE
            color = rgb(c9ccd4)
            font_family = monospace
            font_size = 16
            position = 0, 90
            halign = center
            valign = center
        }

        label {
            monitor =
            text = $TIME
            color = rgb(e5a23d)
            font_family = monospace
            font_size = 42
            position = 0, 150
            halign = center
            valign = center
        }

        input-field {
            monitor =
            size = 420, 56
            position = 0, 0
            halign = center
            valign = center
            inner_color = rgb(1b1d27)
            outer_color = rgb(2a2c37)
            font_color = rgb(c9ccd4)
            check_color = rgb(e5a23d)
            fail_color = rgb(e5484d)
            capslock_color = rgb(e5a23d)
            outline_thickness = 2
            dots_center = true
            placeholder_text = Password
            fail_text = $FAIL ($ATTEMPTS)
        }
      '';

      "hypr/hypridle.conf".text = ''
        general {
            lock_cmd = ${zdeTools}/bin/zde system lock
        }

        listener {
            timeout = 180
            on-timeout = ${pkgs.coreutils}/bin/timeout --kill-after=1s 2s ${pkgs.systemd}/bin/systemctl --user --no-block start "zde-idle-lock@$HYPRIDLE_GENERATION.service"
            on-resume = ${pkgs.coreutils}/bin/timeout --kill-after=1s 2s ${pkgs.systemd}/bin/systemctl --user --no-block stop 'zde-idle-lock@*.service'
            ignore_inhibit = true
        }

        listener {
            timeout = 300
            on-timeout = ${pkgs.coreutils}/bin/timeout --kill-after=1s 2s ${zdeTools}/bin/zde system idle display-off
            on-resume = ${pkgs.coreutils}/bin/timeout --kill-after=1s 2s ${zdeTools}/bin/zde system idle display-on
        }
      '';

      # And which desk panic hides behind, written on the same terms and for the
      # same reason: a file that is there and says nothing is what lets the key
      # name the option to set instead of guessing at a missing file.
      "zde/panic.json".text = builtins.toJSON { decoy = cfg.panic.decoy; };
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
      pkgs.hyprlock # the default screen lock (Mod+Ctrl+semicolon)
      hypridle # strict input-idle lock and conditional display power-off
      pkgs.brightnessctl # system.brightness-up/dn
      pkgs.less # the pager Mod+slash reads the keymap in
      # wl-paste and wl-copy, which are how zded watches the clipboard and puts
      # an entry back on it (Mod+v, internal/clip/wl.go - which says why zde
      # spawns these rather than speaking the data-control protocol itself).
      # Without them the daemon says so once in its log and the history stays
      # empty, and `zde clip history` says which of the two empty it is.
      pkgs.wl-clipboard
      # The region capture (Mod+Shift+s): slurp draws the rectangle, grim takes
      # the pixels through wlr-screencopy. niri has its own region picker and it
      # is deliberately not used, because it saves the frame that goes to the
      # monitor - so a window carrying `block-out-from "screen-capture"` is in
      # the file, while every other capture path on this desktop blacks it out
      # (internal/capture/region.go). Without these two `zde capture shot-region`
      # refuses and says which is missing; it never falls back to the picker.
      pkgs.grim
      pkgs.slurp
      pkgs.wireplumber # wpctl, for audio.*
      # The bar runs from the store path in its unit, so this is not what
      # starts it. It is `qs log` and `qs list`, which are the only way to find
      # out why a bar is not on screen.
      pkgs.quickshell
    ]
    ++ lib.optionals cfg.capture.replay.enable [ replayRecorder ];

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

      zde-idle = {
        Unit = {
          Description = "zde idle policy: lock, then conditionally power displays off";
          Documentation = "https://github.com/crispuscrew/zde";
          PartOf = [ "graphical-session.target" ];
          After = [
            "graphical-session.target"
            "zded.service"
          ];
          Wants = [ "zded.service" ];
        };
        Service = {
          ExecStart = "${lib.getExe hypridle}";
          Restart = "on-failure";
          RestartSec = 1;
        };
        Install.WantedBy = [ "graphical-session.target" ];
      };

      # The lock surface belongs to the user manager, not zded's cgroup, so a
      # daemon restart cannot uncover the session.
      zde-lock = lib.mkIf (cfg.apps.lock != [ ]) {
        Unit = {
          Description = "zde managed screen locker";
          Documentation = "https://github.com/crispuscrew/zde";
          PartOf = [ "graphical-session.target" ];
          After = [ "graphical-session.target" ];
        };
        Service = {
          Type = "exec";
          ExecStart = "${lockCommand}";
          TimeoutStopSec = 5;
        };
      };

      # Hypridle names each source-ordered idle generation. Failed lock attempts
      # keep retrying until the ordered resume callback stops every older one.
      "zde-idle-lock@" = {
        Unit = {
          Description = "zde idle lock attempt for generation %i";
          Documentation = "https://github.com/crispuscrew/zde";
          PartOf = [ "graphical-session.target" ];
          # StopPropagatedFrom is stop-only: restarting Hypridle tears an old
          # generation down instead of restarting it after the new parent.
          StopPropagatedFrom = [ "zde-idle.service" ];
          After = [
            "zde-idle.service"
            "zded.service"
          ];
          Wants = [ "zded.service" ];
          StartLimitIntervalSec = 0;
        };
        Service = {
          Type = "oneshot";
          ExecStart = "${zdeTools}/bin/zde system idle lock";
          Restart = "on-failure";
          RestartSec = 10;
        };
      };

      # Replay is an explicit opt-in because this process continuously sees the
      # screen and holds an encoder. Portal capture is the load-bearing choice:
      # direct KMS bypasses niri's render target and with it capture-block.
      zde-replay = lib.mkIf cfg.capture.replay.enable {
        Unit = {
          Description = "zde replay buffer: the last 30 seconds of the screen";
          Documentation = "https://github.com/crispuscrew/zde";
          PartOf = [ "graphical-session.target" ];
          After = [ "graphical-session.target" ];
        };
        Service = {
          ExecStartPre = "${pkgs.coreutils}/bin/mkdir -p %h/Videos/Replays";
          ExecStart = lib.concatStringsSep " " [
            "${replayRecorder}/bin/gpu-screen-recorder"
            "-v no"
            "-w portal"
            "-restore-portal-session yes"
            "-portal-session-token-filepath %h/.config/zde/replay-portal-token"
            "-f 60"
            "-r 30"
            "-c mp4"
            "-bm cbr"
            "-q 20000"
            "-exclude-metadata yes"
            "-o %h/Videos/Replays"
            "-ipc %t/zde-replay.sock"
          ];
          KillSignal = "SIGINT";
          TimeoutStopSec = 10;
          UMask = "0077";
          Restart = "on-failure";
          RestartPreventExitStatus = "60";
          RestartSec = 1;
          # GSR reserves exit 60 for declining the portal chooser, so that
          # ordinary failures restart without reopening a chooser somebody
          # explicitly dismissed.
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
