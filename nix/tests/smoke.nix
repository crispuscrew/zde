# Smoke test: boot zde in a QEMU VM and check that both layers actually arrive
# on a machine. This is where the host eval in `nix flake check` stops being
# enough - that one proves the modules evaluate, not that a machine comes up
# with a greeter and a config niri accepts.
#
# niri does start here, which took some finding: a build sandbox has no GPU, and
# niri has no software renderer of its own. It runs nested inside a headless
# cage on llvmpipe instead, which needs no /dev/dri at all. That is enough for
# everything zde does, because everything zde does is IPC - so the daemon and
# the client are exercised against a real compositor rather than a fake.
#
# What it still is not is a session on real hardware: no GPU, no logind seat,
# no input devices, no monitors coming and going (docs/roadmap.md, verify
# list). There is a Wayland seat - cage gives niri one and niri gives foot one
# - which is why a client can start at all.
{
  pkgs,
  zdeModule,
  homeManagerModule,
  zdeConfig,
  zincModule,
}:
let
  # A locker that locks nothing, under the name a real one has.
  #
  # The name is the part that matters: `zde doctor` looks a locker up in
  # /etc/pam.d by the basename of its binary, because a locker with no PAM
  # service takes the screen and then refuses every password. A fake called
  # `touch` made this session look like exactly that machine and failed a check
  # that was right to fail. Under the name layer 1 actually configures, both
  # stay honest - the leader chain still ends in a file on disk, and doctor
  # still has its teeth.
  fakeLocker = pkgs.writeShellScriptBin "swaylock" ''
    exec ${pkgs.coreutils}/bin/touch /tmp/zde-locked
  '';

  # One script, so the quoting lives in a shell file rather than inside a
  # Python string inside a Nix string.
  liveCheck = pkgs.writeShellScript "zde-live-check" ''
        set -euo pipefail
        # niri msg has no client-side deadline of its own: pointed at a
        # compositor that accepts and never answers it blocks for ever, and the
        # bounded loops below would never reach their second iteration.
        nirimsg() { timeout 10 niri msg "$@"; }
        # Bounded in seconds rather than in tries, because with that timeout a
        # try is worth up to eleven seconds: counting tries is not counting
        # time, and three loops of sixty could outlast the whole script's
        # budget with the driver killing it before it says why.
        waitfor() {
          secs=$1; shift
          end=$((SECONDS + secs))
          while [ "$SECONDS" -lt "$end" ]; do
            "$@" && return 0
            sleep 1
          done
          return 1
        }
        # Its own runtime directory: the zded started earlier in this test is
        # still listening on /tmp/rt, and it has never heard of a compositor.
        export XDG_RUNTIME_DIR=/tmp/live-rt
        mkdir -p "$XDG_RUNTIME_DIR" /tmp/desks
        chmod 700 "$XDG_RUNTIME_DIR"

        # A desk that does not exist yet, declared - with an app on it, which
        # nothing launches yet and which is still the half of a manifest that
        # says what the desk is for (docs/model.md, section 5).
        printf 'name: vshop
    monitors:
      winit: { workspaces: [code, notes] }
    apps:
      - { app: absent-app, instance: vshop, monitor: winit, workspace: code }
    '       > /tmp/desks/vshop.yaml

        # niri, nested and headless. cage gives it a Wayland host; pixman and
        # llvmpipe give both of them something to draw on without a GPU.
        export WLR_BACKENDS=headless WLR_RENDERER=pixman WLR_LIBINPUT_NO_DEVICES=1
        export LIBGL_ALWAYS_SOFTWARE=1
        cage -- niri >/tmp/niri.log 2>&1 &

        for i in $(seq 60); do
          set +e; NIRI_SOCKET=$(ls "$XDG_RUNTIME_DIR"/niri.*.sock 2>/dev/null | head -1); set -e
          [ -n "$NIRI_SOCKET" ] && break
          sleep 1
        done
        if [ -z "''${NIRI_SOCKET:-}" ]; then
          echo "niri never opened its IPC socket:"; cat /tmp/niri.log; exit 1
        fi
        export NIRI_SOCKET
        # The socket file appearing is not niri answering on it. Software
        # rendering in a VM is slow to get going, and zded's first question would
        # otherwise time out against a compositor that is not listening yet.
        niri_answers() { nirimsg version >/dev/null 2>&1; }
        if ! waitfor 60 niri_answers; then
          echo "niri opened $NIRI_SOCKET but never answered on it:"
          cat /tmp/niri.log; exit 1
        fi
        echo "niri is up on $NIRI_SOCKET"

        # First, the way a login actually starts the daemon, because everything
        # after this line starts it by hand and a person never does. niri is up
        # and this test is standing where niri stands: it imports the
        # environment into the user manager and brings up the target, which is
        # what niri --session does on its way to telling systemd it is ready.
        # Then zded has to appear on its own.
        #
        # Its own runtime dir is the manager's, not this script's, so the
        # daemon that comes up listens somewhere else than the one below and
        # the two never meet.
        # systemctl --user reaches the manager through XDG_RUNTIME_DIR, and
        # this script has pointed that at a directory of its own. So every call
        # here says where the manager actually is, rather than the script
        # moving to it and taking niri's socket path with it.
        mgr=/run/user/$(id -u)
        sctl() { XDG_RUNTIME_DIR=$mgr systemctl --user "$@"; }
        # niri names its IPC socket after the Wayland display it opened, so the
        # display comes back out of the socket path. The bar needs it: it is a
        # Wayland client, where zded only needs the IPC socket.
        #
        # Absolute, which is the part that matters here. A bare "wayland-1" is
        # resolved against XDG_RUNTIME_DIR, and the two directories in this
        # script are not the same one: niri opened its socket in the script's
        # own runtime dir, and the unit below runs with the user manager's.
        # Pointed at the wrong directory, Qt does not report a missing socket -
        # it fails to initialise its Wayland plugin and aborts with a backtrace
        # about platform plugins, which is a long way from the cause.
        # libwayland takes an absolute WAYLAND_DISPLAY as the socket itself.
        export WAYLAND_DISPLAY="$XDG_RUNTIME_DIR/$(basename "$NIRI_SOCKET" | cut -d. -f2)"
        if [ ! -S "$WAYLAND_DISPLAY" ]; then
          echo "niri's IPC socket is $NIRI_SOCKET but there is no wayland socket at $WAYLAND_DISPLAY:"
          ls -la "$XDG_RUNTIME_DIR"; exit 1
        fi
        sctl import-environment NIRI_SOCKET WAYLAND_DISPLAY
        # This machine has no GPU and QtQuick defaults to wanting one. On real
        # hardware the bar gets the same renderer niri does; here it gets Qt's
        # software one, set on the manager rather than in the unit so that the
        # unit stays honest about what it needs.
        sctl set-environment QT_QUICK_BACKEND=software

        # Nothing is on a layer yet, which is what makes "the bar is on a
        # layer" mean anything further down. It has to be asked before the
        # target starts, because the target is what starts the bar - asking
        # afterwards is asking whether the thing that just happened had not
        # happened yet. The json form, because the human one prints nothing at
        # all for an empty list and "nothing" is not something to grep for.
        nirimsg --json layers 2>&1 | tee /tmp/layers-before.txt
        if [ "$(cat /tmp/layers-before.txt)" != "[]" ]; then
          echo "something was already on a layer before the session started:"
          cat /tmp/layers-before.txt; exit 1
        fi

        # Pulled in rather than started: graphical-session.target refuses a
        # manual start, by design - it is meant to arrive as somebody's
        # dependency, which on a login is niri.service. NixOS ships this stand-in
        # for sessions that do not speak systemd, and it binds to the target the
        # same way, so what starts here is the real one.
        sctl start nixos-fake-graphical-session.target
        # Active and listening, not just active. systemd calls a Type=simple
        # unit active the moment it has forked, which is before zded has bound
        # anything - so waiting on the unit alone and then asking zde a question
        # is a race, and one this test lost on its third run after passing
        # twice. The socket is the readiness signal zded actually has.
        #
        # It is a real property of the system and not only of the test: anything
        # the session starts alongside zded can be up before the socket is. The
        # bar tolerates it by saying so and asking again; socket activation
        # would remove it, and would mean zded taking a passed fd.
        zded_up() { sctl is-active --quiet zded.service && [ -S "$mgr/zde/zded.sock" ]; }
        if ! waitfor 30 zded_up; then
          echo "the target came up and zded did not follow it:"
          sctl status zded.service || true
          ls -la "$mgr/zde" 2>&1 || true
          journalctl --user -u zded.service --no-pager | tail -20; exit 1
        fi
        # And it can see the compositor, which is the whole reason the unit
        # waits for the target rather than racing niri: NIRI_SOCKET is in the
        # environment only after niri has put it there.
        XDG_RUNTIME_DIR=$mgr zde status 2>&1 | tee /tmp/unit-status.txt
        grep -qx 'compositor connected' /tmp/unit-status.txt
        # The bar is up and listening, so status has to say so. Waited for rather
        # than asked once: the target starts zded and the bar together, and Qt
        # takes a second or two to reach the socket, so a single question here
        # asks whether something that is still starting has started.
        #
        # This is the line somebody reads when a key drew nothing, and it is only
        # worth having if it is true in both directions - the false case is
        # asserted further down on the zded that has no shell at all.
        shell_seen() {
          XDG_RUNTIME_DIR=$mgr zde status 2>/dev/null | grep -qx 'shell      yes'
        }
        if ! waitfor 30 shell_seen; then
          echo "the bar is running and status does not say a shell is listening:"
          XDG_RUNTIME_DIR=$mgr zde status; sctl status zde-bar.service || true; exit 1
        fi

        # And the bar, which the same target starts. Asserted against niri
        # rather than against systemd: an active unit proves quickshell did not
        # exit, and what a bar has to do is be on the screen. By name, not by
        # "something appeared" - quickshell names its surfaces after itself, and
        # a check that passes on any layer surface at all would pass on a
        # notification popup or on whatever comes next.
        # zde-bar is our namespace and not quickshell's default, which is the
        # point: the default would match any quickshell instance on the machine,
        # and would keep matching after zde grew a second surface.
        bar_layer() {
          nirimsg --json layers >/tmp/layers.txt 2>&1 &&
            grep -q '"namespace":"zde-bar"' /tmp/layers.txt
        }
        if ! waitfor 60 bar_layer; then
          echo "the bar never reached the screen:"
          cat /tmp/layers.txt
          sctl status zde-bar.service || true
          journalctl --user -u zde-bar.service --no-pager | tail -25; exit 1
        fi
        cat /tmp/layers.txt

        # And then what it shows, which is the half a layer surface says nothing
        # about: a bar that never read the queue, one stuck on a number from
        # five minutes ago, and one doing its job all look identical from
        # outside. So the bar answers for itself, over quickshell's own IPC.
        # Addressed by pid, so this needs neither the instance id nor the store
        # path the unit was built with.
        # --pid belongs to `ipc`, not before it: quickshell rejects it outright
        # in front of the subcommand. And stderr is kept rather than dropped,
        # because a failing call otherwise sets an empty answer that fails the
        # comparison below with nothing to say why - which is exactly how this
        # cost a CI round trip.
        barpid=$(sctl show -p MainPID --value zde-bar.service)
        barq() {
          XDG_RUNTIME_DIR=$mgr quickshell ipc --pid "$barpid" call queue "$1" 2>&1 |
            tr -d '\n' || true
        }
        pickerq() {
          XDG_RUNTIME_DIR=$mgr quickshell ipc --pid "$barpid" call picker "$@" 2>&1 |
            tr -d '\n' || true
        }

        # Height and reserved space first: zero of either is a surface the
        # compositor lists and a person cannot see, or one that quietly covers
        # the top of every window.
        geom=$(barq geometry)
        [ "$geom" = "26 26" ] || {
          echo "the bar came up as height/zone '$geom', wanted '26 26'"; exit 1
        }

        # No battery here, and the bar has to say so rather than show a stub. A
        # desktop is the same case as this VM, and a strip claiming 100% on a
        # machine with no battery is worse than one saying nothing.
        [ "$(barq battery)" = "none" ] || {
          echo "no battery on this machine and the bar reads '$(barq battery)'"; exit 1
        }

        # Then the count, against a queue that changes underneath it. "0 0"
        # before, "1 0" after: the poll is two seconds, so this waits rather
        # than sleeping once and hoping.
        empty=$(barq count)
        [ "$empty" = "0 0" ] || { echo "the bar says '$empty' for an empty queue"; exit 1; }
        XDG_RUNTIME_DIR=$mgr zde queue add something for the bar to count >/dev/null
        counted() { [ "$(barq count)" = "1 0" ]; }
        if ! waitfor 20 counted; then
          echo "the queue has one item and the bar says '$(barq count)'"
          sctl status zde-bar.service || true
          journalctl --user -u zde-bar.service --no-pager | tail -25; exit 1
        fi

        # The picker: the surface Mod+Tab opens. A key spawns `zde desk switcher`,
        # zded tells whoever is listening, and the shell draws it - so this
        # exercises the whole path, including the event stream that did not exist
        # until the picker needed it.
        #
        # A desk to pick, first. Nothing is named yet at this point in the test,
        # and a picker with no desks is a refusal rather than a surface. Named
        # `probe` and unnamed again afterwards, because a desk left behind here
        # would join the rotation the later tests count on: alphabetically it
        # would sit between haven and vshop, and `desk next` would stop
        # answering what those tests expect.
        nirimsg action set-workspace-name probe.winit.one >/dev/null
        ondesk() { XDG_RUNTIME_DIR=$mgr zde status 2>/dev/null | grep '^on desk' || true; }
        # What a switch moves: the focused workspace. Asked of niri rather than
        # of zded, because zded's idea of the desk you are on also changes when
        # the watcher notices a workspace being named - which happens moments
        # after this block names one, and is not the picker's doing.
        focusedws() {
          nirimsg --json workspaces 2>/dev/null | tr '{' '\n' |
            grep '"is_focused":[[:space:]]*true' | grep -o '"name":"[^"]*"' | head -1
        }
        # Which window has focus, as '"id":N'. Defined here rather than where nav
        # first needed it, because the window picker below asks the same question
        # and two spellings of it would eventually answer differently.
        focused_window() { nirimsg --json focused-window 2>/dev/null | grep -o '"id":[0-9]*' | head -1; }
        # How many windows are open. One object per line first: niri answers on a
        # single line, so counting matching lines without splitting says 1 for
        # any number of windows at all.
        count_windows() { nirimsg --json windows 2>/dev/null | tr '{' '\n' | grep -c '"id":' || true; }
        [ "$(pickerq state)" = "closed" ] || {
          echo "the picker is open before anything asked for it: $(pickerq state)"; exit 1
        }
        XDG_RUNTIME_DIR=$mgr zde desk switcher 2>&1 | tee /tmp/switcher.txt
        # Nothing printed, which now means more than it used to: the shell has to
        # acknowledge the event before the verb calls it shown, so an empty file
        # here is the whole round trip - key, daemon, event, surface, token back.
        # A shell that read the socket and drew nothing would print the list.
        [ ! -s /tmp/switcher.txt ] || {
          echo "a shell was listening and the switcher printed the list anyway:"
          cat /tmp/switcher.txt; exit 1
        }
        # Exactly what it should be showing: desks rather than windows, one of
        # them, and the one we are on. A match on "open" alone would pass a
        # picker that came up empty, or one that does not know where you are -
        # which is what Enter lands on - and now also one showing the wrong
        # rows, since both pickers are the same surface.
        picker_open() { [ "$(pickerq state)" = "open desks 1 probe" ]; }
        if ! waitfor 20 picker_open; then
          echo "the picker never opened with the right contents: $(pickerq state)"
          journalctl --user -u zde-bar.service --no-pager | tail -20; exit 1
        fi
        # On the screen, and named ours: the bar is on a layer too, so this looks
        # for the picker's own namespace rather than any surface at all.
        nirimsg --json layers 2>&1 | tee /tmp/layers-picker.txt
        grep -q '"namespace":"zde-picker"' /tmp/layers-picker.txt || {
          echo "the picker says it is open and niri has no such layer:"
          cat /tmp/layers-picker.txt; exit 1
        }
        # And it has the keyboard, which is the half that makes it usable and the
        # half nothing else here would notice: driving it through IPC works just
        # as well with the keyboard never taken. niri prints the interactivity in
        # the same reply, so this costs nothing.
        grep -q '"keyboard_interactivity":"Exclusive"' /tmp/layers-picker.txt || {
          echo "the picker is on screen without the keyboard, so no key would reach it:"
          cat /tmp/layers-picker.txt; exit 1
        }

        # Dismissing, before choosing: the way out that changes nothing. It is
        # the path Escape takes, and without this the whole hide-without-picking
        # branch is dead code as far as CI is concerned.
        before_dismiss=$(focusedws)
        [ "$(pickerq dismiss)" = "closed" ] || { echo "dismiss said $(pickerq dismiss)"; exit 1; }
        dismissed() { [ "$(pickerq state)" = "closed" ] && [ "$(nirimsg --json layers 2>/dev/null | grep -c zde-picker)" = "0" ]; }
        if ! waitfor 15 dismissed; then
          echo "the picker was dismissed and is still there: $(pickerq state)"
          nirimsg --json layers; exit 1
        fi
        # And nothing moved: dismissing is not choosing.
        [ "$(focusedws)" = "$before_dismiss" ] || {
          echo "dismissing the picker moved focus from $before_dismiss to $(focusedws)"; exit 1
        }

        # The leader: a letter that is not navigation runs an action and closes
        # the surface. Mod+Tab then l is what the keymap has been calling a
        # sequence that needs the input daemon, and it does not: the picker holds
        # the keyboard, so the second press is one this surface can read.
        #
        # Reopened first, because dismissing closed it.
        XDG_RUNTIME_DIR=$mgr zde desk switcher >/dev/null
        if ! waitfor 20 picker_open; then
          echo "the picker did not reopen for the leader check: $(pickerq state)"; exit 1
        fi
        rm -f /tmp/zde-locked
        [ "$(pickerq act lock)" = "acted" ] || {
          echo "the leader did not act: $(pickerq act lock)"; exit 1
        }
        # And the command ran: picker, shell, zde, the apps table, an exec. The
        # locker on this machine is a touch rather than swaylock, because a VM
        # with no input devices that locks itself cannot unlock itself - what is
        # asserted is the chain, and whether a real locker takes the screen is a
        # by-hand item (docs/verify.md).
        ran() { [ -e /tmp/zde-locked ]; }
        if ! waitfor 20 ran; then
          echo "the leader acted and nothing ran"; exit 1
        fi
        if ! waitfor 15 dismissed; then
          echo "the leader acted and the picker stayed on screen: $(pickerq state)"; exit 1
        fi

        # Then open it again for the half that does choose.
        XDG_RUNTIME_DIR=$mgr zde desk switcher >/dev/null
        if ! waitfor 20 picker_open; then
          echo "the picker did not reopen: $(pickerq state)"; exit 1
        fi

        # And choosing takes you there. Through the same IPC, because a machine
        # with no input devices cannot press Enter - what is being checked is the
        # wiring behind the key: the shell asks zded, zded switches the desk.
        [ "$(pickerq pick probe)" = "picked" ] || {
          echo "choosing probe from the picker: $(pickerq pick probe)"; exit 1
        }
        landed() { case "$(ondesk)" in *probe*) return 0 ;; *) return 1 ;; esac; }
        if ! waitfor 20 landed; then
          echo "the picker chose probe and zded is on '$(ondesk)'"; exit 1
        fi
        # And it closed itself on the way, surface and all.
        shut() { [ "$(pickerq state)" = "closed" ] && [ "$(nirimsg --json layers 2>/dev/null | grep -c zde-picker)" = "0" ]; }
        if ! waitfor 15 shut; then
          echo "the picker chose a desk and stayed on screen: $(pickerq state)"
          nirimsg --json layers; exit 1
        fi
        # Mod+w, which is the same surface with different rows: zded hands over
        # the open windows, the shell draws them, and choosing one goes to it.
        #
        # Nothing is open yet, and that is a refusal rather than an empty
        # surface - a picker with no rows is one you have to press Escape to get
        # out of, and it answers a question nobody asked.
        if XDG_RUNTIME_DIR=$mgr zde window jump-to 2>&1 | tee /tmp/jump-none.txt; then
          echo "jump-to opened a picker with nothing to show:"
          cat /tmp/jump-none.txt; exit 1
        fi
        grep -q 'nothing is open' /tmp/jump-none.txt || {
          echo "the refusal does not say why there is nothing to pick:"
          cat /tmp/jump-none.txt; exit 1
        }
        [ "$(pickerq state)" = "closed" ] || {
          echo "a picker came up with no windows in it: $(pickerq state)"; exit 1
        }

        # Two windows, because with one, a jump to the right window, a jump to
        # the wrong one and a jump that did nothing all leave the same window
        # focused. Redirected, like every other client this script starts.
        foot -e sleep 600 >/tmp/jump-foot1.log 2>&1 &
        foot -e sleep 600 >/tmp/jump-foot2.log 2>&1 &
        two_windows() { [ "$(count_windows)" -ge 2 ]; }
        if ! waitfor 60 two_windows; then
          echo "the two windows to jump between never appeared:"
          cat /tmp/jump-foot1.log /tmp/jump-foot2.log; nirimsg windows; exit 1
        fi

        # Which window has focus, and the other one, both read now rather than
        # once the picker is up. A layer surface holding the keyboard reads to
        # niri as nothing focused at all - the roadmap's own warning, and the
        # reason the picker takes focus only while visible - so asked with the
        # picker on screen this answers nothing and takes the script with it.
        onnow=$(focused_window) || true
        onnow=''${onnow##*:}
        [ -n "$onnow" ] || {
          echo "two windows are open and niri has none of them focused:"
          nirimsg windows; exit 1
        }
        target=$(nirimsg --json windows 2>/dev/null | tr '{' '\n' |
          grep -o '"id":[0-9]*' | cut -d: -f2 | grep -v "^$onnow$" | head -1) || true
        [ -n "$target" ] || {
          echo "no second window to choose: everything reports the focused id ($onnow)"
          nirimsg windows; exit 1
        }

        XDG_RUNTIME_DIR=$mgr zde window jump-to 2>&1 | tee /tmp/jump-shell.txt
        # Nothing printed: a shell drew it and said so, the same round trip the
        # desk switcher makes - key, daemon, event, surface, token back.
        [ ! -s /tmp/jump-shell.txt ] || {
          echo "a shell was listening and jump-to printed the list anyway:"
          cat /tmp/jump-shell.txt; exit 1
        }
        # Both windows, and the window picker rather than the desk one: the two
        # share a surface now, so a Mod+w that opened the desks would otherwise
        # look exactly like this from outside. The dash is "no row you are
        # already on", which a list of windows has none of.
        jumper_open() { [ "$(pickerq state)" = "open windows 2 -" ]; }
        if ! waitfor 20 jumper_open; then
          echo "the window picker never opened with the right contents: $(pickerq state)"
          journalctl --user -u zde-bar.service --no-pager | tail -20; exit 1
        fi
        nirimsg --json layers 2>&1 | tee /tmp/layers-jump.txt
        grep -q '"namespace":"zde-picker"' /tmp/layers-jump.txt || {
          echo "the window picker says it is open and niri has no such layer:"
          cat /tmp/layers-jump.txt; exit 1
        }

        # The window that is not the one already focused - the newest has it, and
        # this was read before the picker took the keyboard - so that what is
        # asserted afterwards is a jump and not a no-op.
        [ "$(pickerq pick "$target")" = "picked" ] || {
          echo "choosing window $target: $(pickerq pick "$target")"; exit 1
        }
        # By id, and asked of niri. "a window is focused" is true before this
        # runs, so it would pass with the jump landing anywhere at all.
        jumped() { [ "$(focused_window)" = '"id":'"$target" ]; }
        if ! waitfor 20 jumped; then
          echo "the picker chose window $target and niri has $(focused_window) focused"
          nirimsg windows; exit 1
        fi
        # And it closed itself on the way, surface and all.
        if ! waitfor 15 shut; then
          echo "the picker chose a window and stayed on screen: $(pickerq state)"
          nirimsg --json layers; exit 1
        fi

        # Both windows closed again. Everything below this line was written for
        # a strip with a known set of windows on it, and a foot left here is a
        # window on whichever workspace was focused when it started - which is
        # how this test failed once already.
        nirimsg action close-window >/dev/null
        one_window() { [ "$(count_windows)" -le 1 ]; }
        if ! waitfor 30 one_window; then
          echo "the first jump window would not close:"; nirimsg windows; exit 1
        fi
        nirimsg action close-window >/dev/null
        no_windows() { [ "$(count_windows)" -eq 0 ]; }
        if ! waitfor 30 no_windows; then
          echo "a jump window outlived its test, and later checks need the strip as it was:"
          nirimsg windows; exit 1
        fi

        # And the probe desk goes away again, so the rotation tests further down
        # see the two desks they were written for.
        nirimsg action focus-workspace probe.winit.one >/dev/null
        nirimsg action unset-workspace-name >/dev/null
        nirimsg workspaces 2>&1 | tee /tmp/ws-probe.txt
        if grep -q 'probe.winit.one' /tmp/ws-probe.txt; then
          echo "the probe desk outlived its test and will join the rotation:"
          cat /tmp/ws-probe.txt; exit 1
        fi

        # And that it comes back. zded is restarted by every home-manager switch
        # that changes it, and the bar's own unit is not restarted with it - so a
        # socket that does not redial leaves the bar blind for the rest of the
        # session. It did, until this test existed.
        sctl restart zded.service
        if ! waitfor 30 zded_up; then
          echo "zded did not come back:"; sctl status zded.service || true; exit 1
        fi
        recovered() { [ "$(barq count)" = "1 0" ]; }
        if ! waitfor 30 recovered; then
          echo "zded restarted and the bar never reconnected: '$(barq count)'"
          journalctl --user -u zde-bar.service --no-pager | tail -25; exit 1
        fi

        XDG_RUNTIME_DIR=$mgr zde queue 2>&1 | tee /tmp/bar-queue.txt
        grep -q 'something for the bar to count' /tmp/bar-queue.txt
        id=$(grep 'something for the bar' /tmp/bar-queue.txt | cut -f1)
        XDG_RUNTIME_DIR=$mgr zde queue done "$id" >/dev/null

        # PartOf, which is what keeps one session's daemon out of the next
        # one's way: the target going down takes zded with it, and zded on its
        # way out takes its socket. Ending the session is ending what held the
        # target up - graphical-session.target is StopWhenUnneeded, so letting
        # go of it is how a session ends rather than stopping it by name.
        sctl stop nixos-fake-graphical-session.target
        gone() { ! sctl is-active --quiet zded.service; }
        if ! waitfor 15 gone; then
          echo "the session ended and zded stayed:"
          sctl status zded.service || true; exit 1
        fi
        socket_gone() { [ ! -e "$mgr/zde/zded.sock" ]; }
        if ! waitfor 15 socket_gone; then
          echo "zded left $mgr/zde/zded.sock behind for the next session"; exit 1
        fi
        # The bar goes with it, and takes its surface off the screen. A bar left
        # behind by a session that ended is a strip drawn over the next one.
        bar_gone() { ! sctl is-active --quiet zde-bar.service; }
        if ! waitfor 15 bar_gone; then
          echo "the session ended and the bar stayed:"
          sctl status zde-bar.service || true; exit 1
        fi
        layers_gone() { [ "$(nirimsg --json layers 2>/dev/null)" = "[]" ]; }
        if ! waitfor 15 layers_gone; then
          echo "the bar stopped and its surface is still on the screen:"
          nirimsg --json layers; exit 1
        fi

        # A session bus, so zded can be the notification server on it. Started
        # before zded because zded takes the name at startup and carries on
        # without one - which is the right behaviour and also means a bus
        # started afterwards would leave notifications going nowhere.
        eval "$(dbus-launch --sh-syntax)"
        export DBUS_SESSION_BUS_ADDRESS

        zded -journal /tmp/live.jsonl -desks /tmp/desks >/tmp/zded-live.log 2>&1 &
        for i in $(seq 30); do
          [ -S "$XDG_RUNTIME_DIR/zde/zded.sock" ] && break
          sleep 1
        done
        if [ ! -S "$XDG_RUNTIME_DIR/zde/zded.sock" ]; then
          echo "zded never listened:"; cat /tmp/zded-live.log; exit 1
        fi

        # The daemon can see the compositor, which is the thing a fake cannot
        # prove.
        zde status 2>&1 | tee /tmp/status.txt
        grep -qx 'compositor connected' /tmp/status.txt

        # A desk that exists only as a manifest is created and entered.
        zde desk switch vshop 2>&1 | tee /tmp/switch.txt
        grep -qx 'vshop.winit.code' /tmp/switch.txt

        # Layer 2, end to end and through the CLI: the desk's app comes back
        # as the address zinc takes, on the workspace zde names, with the state
        # directory zinc itself answered for. The last column is the one worth
        # the VM - it is `zcr where`, run by zde, and the point of asking is
        # that the layout stays zinc's to change.
        XDG_STATE_HOME=/tmp/state zde desk apps vshop 2>&1 | tee /tmp/apps.txt
        awk '$1=="absent-app@vshop" && $2=="vshop.winit.code" { found=1 }
             END { exit !found }' /tmp/apps.txt || {
          echo "zde desk apps did not name the app the manifest declares:"
          cat /tmp/apps.txt; exit 1
        }
        # And it asked zinc rather than answering for it. This desk names an app
        # zinc does not have, so what comes back is zinc's refusal, passed
        # through - which is the same round trip that fills the state column for
        # an app that exists (asserted directly against zcr further up, where it
        # costs nothing).
        grep -q 'no app "absent-app" defined' /tmp/apps.txt || {
          echo "zde desk apps did not report what zcr said about the app:"
          cat /tmp/apps.txt; exit 1
        }
        # And the desk you are on, which is the form a keybind would use: the
        # switch above put us on vshop.
        XDG_STATE_HOME=/tmp/state zde desk apps 2>&1 | tee /tmp/apps-here.txt
        grep -q 'absent-app@vshop' /tmp/apps-here.txt || {
          echo "zde desk apps, on vshop, did not list vshop's apps:"
          cat /tmp/apps-here.txt; exit 1
        }
        # And entering the desk tried to start what it declares: the whole chain,
        # from a switch through the manifest to zcr with the address the
        # manifest spells. A desk that starts nothing writes nothing here.
        #
        # The app is one zinc does not have, deliberately. A defined app would
        # send podman looking for an image this VM has no network to fetch, on
        # every switch in this script, and the first thing that broke was an
        # assertion three screens away about a bar reconnecting. A refusal
        # arrives immediately and proves the same wiring.
        waitfor 20 grep -q 'starting absent-app@vshop' /tmp/zded-live.log || {
          echo "switching to vshop did not try to start the app it declares:"
          cat /tmp/zded-live.log; exit 1
        }

        # zcr is on the session's PATH, said by the daemon that has that PATH.
        # A machine where this is no is one whose desks declare apps it cannot
        # run, and every symptom of it looks like something else.
        grep -qx 'zinc       yes' /tmp/status.txt || {
          echo "zded cannot see zcr, so nothing sandboxed can start:"
          cat /tmp/status.txt; exit 1
        }

        # No shell on this zded, and status says so. Between this and the line
        # above, the fact is asserted in both directions - a status field that is
        # always "yes" answers nothing.
        zde status 2>&1 | tee /tmp/status-noshell.txt
        grep -qx 'shell      no' /tmp/status-noshell.txt || {
          echo "no shell is listening and status does not say so:"
          cat /tmp/status-noshell.txt; exit 1
        }
        grep -q '^notify' /tmp/status-noshell.txt || {
          echo "status does not say whether notifications are ours:"
          cat /tmp/status-noshell.txt; exit 1
        }

        # And doctor, which is all of that on one screen. This is as healthy as
        # this script ever gets - a daemon answering, a compositor it can see,
        # the notification name taken, a manifest that parses - so nothing may
        # fail and the exit status has to be zero. The units are inactive here
        # (this zded was started by hand) and no machine anywhere has zcr yet,
        # which is exactly why those are warnings: neither is a session
        # somebody cannot work in, and a command that exits non-zero on every
        # machine is one nobody reads the output of.
        if ! zde doctor >/tmp/doctor.txt 2>&1; then
          echo "doctor failed a check on a session that is working:"
          cat /tmp/doctor.txt; exit 1
        fi
        cat /tmp/doctor.txt
        for line in 'ok +zded' 'ok +compositor +connected' 'ok +notify' 'warn +shell' 'ok +locker'; do
          grep -qE "^$line" /tmp/doctor.txt || {
            echo "doctor has no '$line' line:"; cat /tmp/doctor.txt; exit 1
          }
        done
        # The locker is the line this command is most worth having: swaylock is
        # what layer 1 configures and /etc/pam.d/swaylock is what lets it
        # authenticate, so a machine where that file is not there locks and
        # never unlocks. The python half of this test asserts the file exists;
        # this asserts doctor is the thing that would notice if it stopped.
        grep -q '/etc/pam.d/swaylock' /tmp/doctor.txt || {
          echo "doctor does not say what the screen lock would authenticate against:"
          cat /tmp/doctor.txt; exit 1
        }

        # Mod+t, which is `zde app launch terminal`. The one thing a desktop has
        # to be able to do: until this existed, a session could be entered and
        # nothing could be started from it, and the live image carried a bind of
        # its own just so somebody could type.
        #
        # Asserted on a window appearing, not on the config file existing: the
        # name has to resolve, the binary has to be there, and exec has to
        # replace this process with it.
        before_terminal=$(nirimsg --json windows | grep -c '"id"' || true)
        # Redirected, like every other client this script starts. A background
        # process that inherits stdout holds the pipe open, and the runner waits
        # for EOF on it - so leaving this bare hung the whole test until the
        # global timeout, twenty five minutes later, with the terminal it
        # launched sitting there working perfectly.
        zde app launch terminal >/tmp/launch.log 2>&1 &
        more_windows() {
          [ "$(nirimsg --json windows | grep -c '"id"' || true)" -gt "$before_terminal" ]
        }
        if ! waitfor 60 more_windows; then
          echo "zde app launch terminal started nothing:"
          zde app list; nirimsg windows; exit 1
        fi

        # And close it again. Everything below this line was written for a strip
        # with a known set of windows on it - the move-window checks in
        # particular expect the workspace they land on to be empty - and a
        # terminal left running here is a window on whichever workspace was
        # focused when it started. It failed exactly that way once.
        nirimsg action close-window >/dev/null
        closed() {
          [ "$(nirimsg --json windows | grep -c '"id"' || true)" -le "$before_terminal" ]
        }
        if ! waitfor 30 closed; then
          echo "the launched terminal would not close, and later checks need the strip as it was:"
          nirimsg windows; exit 1
        fi

        # And the lock is one of the names, because Mod+Ctrl+semicolon resolves
        # through the same table and a machine that cannot lock its screen is
        # not one to take out of the house.
        zde app list 2>&1 | tee /tmp/applist.txt
        grep -q '^lock' /tmp/applist.txt || {
          echo "no lock configured, so Mod+Ctrl+semicolon does nothing:"
          cat /tmp/applist.txt; exit 1
        }

        # And a name nobody configured says what is configured, rather than
        # failing in a way that leaves somebody wondering whether they typed it
        # wrong or their machine has none.
        if zde app launch nosuchthing 2>&1 | tee /tmp/nosuch.txt; then
          echo "launched an app that is not configured"; exit 1
        fi
        grep -q 'terminal' /tmp/nosuch.txt || {
          echo "the refusal does not say what this machine can start:"; cat /tmp/nosuch.txt; exit 1
        }

        # The switcher with nothing listening, which is this zded: the session
        # target was stopped above, so there is no shell here. The key still has
        # to do something, so it prints the desks and marks the one you are on -
        # which is what it did before there was a picker to open.
        zde desk switcher 2>&1 | tee /tmp/switcher-noshell.txt
        grep -q 'vshop' /tmp/switcher-noshell.txt || {
          echo "no shell to show the picker and the switcher printed nothing:"
          cat /tmp/switcher-noshell.txt; exit 1
        }
        grep -q '(here)' /tmp/switcher-noshell.txt || {
          echo "the printed list does not say which desk you are on:"
          cat /tmp/switcher-noshell.txt; exit 1
        }

        # And it still works with a broken manifest sitting beside it, which is
        # the ordinary state of a directory somebody edits by hand. One bad file
        # used to fail the whole read, and the caller that mattered dropped the
        # error - so every desk on the machine quietly stopped being declared.
        # Left here for the rest of the run, deliberately: everything below this
        # line is therefore also a test that a broken manifest does not spread.
        printf 'name: haven\nmonitorz: nope\n' > /tmp/desks/broken.yaml
        zde desk switch vshop 2>&1 | tee /tmp/switch2.txt
        grep -qx 'vshop.winit.code' /tmp/switch2.txt
        # And somebody is told, by the one command that exists to say what is
        # wrong with a session.
        zde status 2>&1 | tee /tmp/status-bad.txt
        grep -q 'broken.yaml' /tmp/status-bad.txt || {
          echo "a manifest could not be read and zde status did not mention it:"
          cat /tmp/status-bad.txt; exit 1
        }
        # And doctor says so in both ways it has: the line that names the file,
        # and the exit status - which is the half that makes this worth piping
        # into a bug report. The same command exited zero a few lines up, so
        # between the two the status is asserted in both directions.
        if zde doctor >/tmp/doctor-bad.txt 2>&1; then
          echo "a manifest could not be read and doctor still passed:"
          cat /tmp/doctor-bad.txt; exit 1
        fi
        grep -qE '^fail +manifests +.*broken.yaml' /tmp/doctor-bad.txt || {
          echo "doctor failed and does not name the manifest it failed over:"
          cat /tmp/doctor-bad.txt; exit 1
        }

        # niri's own client agrees that both declared workspaces are there.
        nirimsg workspaces 2>&1 | tee /tmp/ws.txt
        grep -q 'vshop.winit.code' /tmp/ws.txt
        grep -q 'vshop.winit.notes' /tmp/ws.txt

        # The journal recorded where the desk was left.
        grep -q '"slot":"code"' /tmp/live.jsonl

        # Reconcile is quiet on a world that is already true, and niri's own
        # trailing empty workspace is left alone rather than adopted.
        zde desk reconcile 2>&1 | tee /tmp/rec.txt
        grep -qx 'nothing to reconcile' /tmp/rec.txt

        # Adoption, against a real compositor and a real window. Everything up
        # to here could be done with no windows at all; this is the path that
        # names a workspace after what is in it.
        #
        # foot is a Wayland client that starts without a GPU, which not many
        # do. It has to talk to niri rather than to the cage hosting it, which
        # is what WAYLAND_DISPLAY above is for - exported where the bar needed
        # it first, and read out of niri's socket name either way.
        # Past the end of the strip, onto the empty workspace niri keeps there.
        # That is where a new workspace comes from in real use, and it is
        # unnamed until something claims it.
        # niri clamps at the last workspace instead of wrapping, so any count
        # that reaches the end lands there. Two is one per declared workspace,
        # which is what keeps this honest if [code, notes] ever grows: fewer
        # downs than workspaces and foot opens on a named one instead.
        nirimsg action focus-workspace-down >/dev/null
        nirimsg action focus-workspace-down >/dev/null
        foot -e sleep 600 >/tmp/foot.log 2>&1 &
        # Asked for into a file and grepped there rather than piped: under
        # pipefail a producer that exits non-zero after grep already matched -
        # SIGPIPE, or the timeout firing late - would read as no match.
        foot_window() { nirimsg windows >/tmp/win.txt 2>&1 && grep -qi foot /tmp/win.txt; }
        if ! waitfor 60 foot_window; then
          echo "no window ever appeared:"; cat /tmp/foot.log /tmp/win.txt; exit 1
        fi

        # Deliberately no reconcile here. What keeps names true while a session
        # runs is the watcher, and asserting the command instead would pass even
        # with the watcher broken - so wait for the name to appear on its own.
        # zded's own log is the one worth printing when it does not: a watcher
        # that never saw an event and one that errored every reconcile leave
        # exactly the same workspace list behind.
        adopted() { nirimsg workspaces >/tmp/ws2.txt 2>&1 && grep -q 'vshop.winit.foot' /tmp/ws2.txt; }
        if ! waitfor 30 adopted; then
          echo "the watcher never named the workspace after what is in it:"
          cat /tmp/ws2.txt /tmp/zded-live.log; exit 1
        fi

        # Only then the manual path, which must find nothing left: adoption
        # converges on a real compositor, not only in a unit test.
        zde desk reconcile 2>&1 | tee /tmp/rec3.txt
        grep -qx 'nothing to reconcile' /tmp/rec3.txt

        # And the desk can be written back out as a manifest.
        if zde desk snapshot haven 2>&1 | tee /tmp/snapfail.txt; then
          echo "snapshotted a desk that does not exist"; exit 1
        fi
        grep -q 'no workspaces' /tmp/snapfail.txt
        rm -f /tmp/desks/vshop.yaml
        zde desk snapshot 2>&1 | tee /tmp/snap.txt
        grep -q 'vshop.yaml' /tmp/snap.txt
        # The adopted workspace is the one worth checking: it is what a
        # snapshot newly captures that a hand-written manifest would not have
        # had.
        grep -q 'foot' /tmp/desks/vshop.yaml

        # Rotation, which needs a second desk to be about anything. Declared
        # here rather than at the top so that everything above it is about one
        # desk, the way it was written.
        printf 'name: haven
    monitors:
      winit: { workspaces: [db] }
    '       > /tmp/desks/haven.yaml
        zde desk switch haven 2>&1 | tee /tmp/haven.txt
        grep -qx 'haven.winit.db' /tmp/haven.txt

        # Alphabetical, and a loop: from haven forward is vshop, and from
        # vshop forward again is haven rather than the end of a list.
        zde desk next 2>&1 | tee /tmp/next.txt
        grep -q 'vshop.winit' /tmp/next.txt
        zde desk next 2>&1 | tee /tmp/next2.txt
        grep -qx 'haven.winit.db' /tmp/next2.txt
        # And prev is next backwards, wrapping the other way.
        zde desk prev 2>&1 | tee /tmp/prev.txt
        grep -q 'vshop.winit' /tmp/prev.txt

        # nav, the axis the model reads as one: the window below first, the
        # desk after it.
        #
        # On the workspace that has a window in it, deliberately. A desk switch
        # comes back to the workspace you left, which here is an empty one, and
        # an empty workspace rotates whatever nav does with a stack - so this
        # would pass just as well with the window half of nav missing.
        nirimsg action focus-workspace vshop.winit.foot >/dev/null

        # One window is the end of its own stack, so the desk turns. This is
        # also where the assumption under nav gets tested: niri's
        # FocusWindowDown does nothing at the end of a column rather than
        # falling through to the next workspace. If it ever fell through, focus
        # would have changed, nav would report no desk change, and the grep
        # below would fail here rather than on somebody's machine.
        zde nav down 2>&1 | tee /tmp/nav-down.txt
        grep -qx 'haven.winit.db' /tmp/nav-down.txt

        # And with two windows stacked in one column the same key stays put,
        # moving focus inside the workspace. niri opens the second in its own
        # column, so it has to be consumed into the first.
        nirimsg action focus-workspace vshop.winit.foot >/dev/null
        first=$(focused_window)
        foot -e sleep 600 >/tmp/foot2.log 2>&1 &
        second_window() { [ -n "$(focused_window)" ] && [ "$(focused_window)" != "$first" ]; }
        if ! waitfor 60 second_window; then
          echo "the second window never took focus:"; cat /tmp/foot2.log; exit 1
        fi
        nirimsg action consume-or-expel-window-left >/dev/null
        # From the top of the stack, so down has somewhere to go regardless of
        # which end consuming left focus on.
        nirimsg action focus-window-top >/dev/null
        top=$(focused_window)
        zde nav down 2>&1 | tee /tmp/nav-stack.txt
        [ ! -s /tmp/nav-stack.txt ] # nothing about the desk changed
        [ -n "$top" ] && [ "$(focused_window)" != "$top" ] || {
          echo "nav down stayed on window $top instead of the one below it"
          exit 1
        }

        # Shift on the same axis: the window comes along, and so do you.
        #
        # The window half is niri's own answer - the focused window id after
        # the move is read from the compositor, and a carry that moved nothing
        # would leave haven's empty workspace focused and no window at all. The
        # desk half is zded's answer rather than niri's, which is the weaker of
        # the two; and with one screen and two desks this cannot see a carry
        # that went the wrong way or one that let focus follow it. Those are
        # pinned in the unit tests, where three desks and a focus-following
        # compositor are cheap.
        carried=$(focused_window)
        zde desk move-window next 2>&1 | tee /tmp/move.txt
        grep -qx 'haven.winit.db' /tmp/move.txt
        [ "$(focused_window)" = "$carried" ] || {
          echo "window $carried did not come along: focus is on $(focused_window)"
          nirimsg windows; exit 1
        }

        # The regulars: a band reachable from anywhere by its own key, and
        # never scrolled into by anything else. Made last, so everything above
        # it ran on a machine that had none.
        #
        # Made with the verb, which is the whole point of the verb: a manifest
        # cannot declare this band and adoption names into the desk you are on,
        # so until there was one, the only way to have regulars was to name a
        # workspace through niri by hand - which this test used to do, and which
        # is not something to ask of anybody.
        #
        # So: a workspace worth keeping, made the way a person makes one. Past
        # the end of the strip, open something, let adoption name it. vshop
        # already has a foot workspace and its snapshot declares it, so this one
        # is adopted as foot-2 - undeclared, which is what makes it movable.
        #
        # The desk is said out loud rather than inherited from the block above.
        # Adoption names into the desk you are on, and the tests before this one
        # left haven active, so a workspace opened here without switching first
        # is adopted into haven - which is what happened when this was written,
        # and cost a CI round trip to a name that never appeared.
        zde desk switch vshop >/dev/null
        for i in $(seq 10); do nirimsg action focus-workspace-down >/dev/null; done
        foot -e sleep 600 >/tmp/foot3.log 2>&1 &
        promoted() { nirimsg workspaces >/tmp/ws3.txt 2>&1 && grep -q 'vshop.winit.foot-2' /tmp/ws3.txt; }
        if ! waitfor 60 promoted; then
          echo "the workspace to promote was never adopted:"
          cat /tmp/foot3.log /tmp/ws3.txt /tmp/zded-live.log; exit 1
        fi

        # First the refusal, on a workspace the manifest declares: it would be
        # recreated by the next switch, so moving it would look undone by
        # something invisible. foot is in vshop's snapshot from earlier.
        nirimsg action focus-workspace vshop.winit.foot >/dev/null
        if zde desk move-workspace-to regulars 2>&1 | tee /tmp/mws-no.txt; then
          echo "moved a workspace vshop declares, which comes straight back"; exit 1
        fi
        grep -q 'vshop' /tmp/mws-no.txt
        grep -q 'foot' /tmp/mws-no.txt

        # Then the move that works, and the band exists because of it.
        nirimsg action focus-workspace vshop.winit.foot-2 >/dev/null
        zde desk move-workspace-to regulars 2>&1 | tee /tmp/mws.txt
        grep -qx 'regulars.winit.foot-2' /tmp/mws.txt
        nirimsg workspaces 2>&1 | tee /tmp/ws4.txt
        grep -q 'regulars.winit.foot-2' /tmp/ws4.txt
        # A rename and not a move: the window that was in it is still in it.
        [ -n "$(focused_window)" ] || {
          echo "the workspace changed bands and lost what was in it:"; nirimsg windows; exit 1
        }

        # Making the band left focus sitting in it - the workspace under you
        # changed hands, you did not go anywhere - so the desk to come back to
        # is chosen here rather than inherited: what is being checked is
        # reaching the regulars from somewhere and landing back on that
        # somewhere, which needs the somewhere to be known.
        zde desk switch haven >/dev/null
        zde desk regulars 2>&1 | tee /tmp/reg.txt
        grep -qx 'regulars.winit.foot-2' /tmp/reg.txt

        # Reaching for them costs nothing, because coming back is one key.
        zde desk last 2>&1 | tee /tmp/reg-back.txt
        grep -q 'haven.winit' /tmp/reg-back.txt

        # And the rotation steps over them, in both directions and in both
        # senses. From the band, which is in no rotation, forward is the first
        # desk and back is the last; were the regulars in it they would be an
        # index in the middle, and both of these would answer the other desk.
        zde desk regulars >/dev/null
        zde desk next 2>&1 | tee /tmp/reg-next.txt
        grep -q 'haven.winit' /tmp/reg-next.txt
        zde desk regulars >/dev/null
        zde desk prev 2>&1 | tee /tmp/reg-prev.txt
        grep -q 'vshop.winit' /tmp/reg-prev.txt

        # The step that would actually land on them: alphabetically the
        # regulars sit between haven and vshop, so going back from vshop is
        # where a rotation that stopped excluding them would arrive.
        zde desk prev 2>&1 | tee /tmp/reg-prev2.txt
        grep -q 'haven.winit' /tmp/reg-prev2.txt

        # Into the regulars and back out, which is what a desk named rather
        # than counted to is for. Out was already possible - from inside the
        # band the rotation falls back to an end of it - but only to whichever
        # desk that fallback picked. Getting a window in was impossible.
        zde desk switch vshop >/dev/null
        nirimsg action focus-workspace vshop.winit.foot >/dev/null
        moved=$(focused_window)
        [ -n "$moved" ] || {
          echo "expected a window on vshop.winit.foot to carry:"; nirimsg windows; exit 1
        }
        # Where the window ended up, counted on the band's own workspace rather
        # than read off what has focus. This used to check that focus followed
        # the carried window, which held only because the band's workspace was
        # empty: the band is made by promoting a workspace now, so it has work
        # in it, and landing on the workspace lands next to what was already
        # there. Which of them niri focuses is niri's business; whether the
        # window arrived is ours.
        # Which workspace the carried window is on, asked of niri about that
        # window by id. Not counted, and not read off what has focus: counting
        # let the wrong window pass this test once, because the trip back
        # carried whatever had focus rather than the window that went in.
        wsid() {
          nirimsg --json workspaces 2>/dev/null | tr '{' '\n' | grep "\"name\":\"$1\"" |
            grep -o '"id":[0-9]*' | head -1 | cut -d: -f2
        }
        winws() {
          nirimsg --json windows 2>/dev/null | tr '{' '\n' | grep "\"id\":$1," |
            grep -o '"workspace_id":[0-9]*' | head -1 | cut -d: -f2
        }
        # focused_window answers '"id":2'; the number is what niri wants back.
        movedid=''${moved##*:}
        band=$(wsid regulars.winit.foot-2)
        [ -n "$band" ] || { echo "cannot find the band's workspace id"; nirimsg --json workspaces; exit 1; }

        zde desk move-window-to regulars 2>&1 | tee /tmp/mwt-in.txt
        grep -qx 'regulars.winit.foot-2' /tmp/mwt-in.txt
        [ "$(winws "$movedid")" = "$band" ] || {
          echo "window $movedid is on workspace $(winws "$movedid"), not the band's $band"
          nirimsg windows; exit 1
        }

        # Focus it before sending it back, because the verb carries whatever is
        # focused - and the band already had a window, so landing there did not
        # leave focus on the one that arrived. That ambiguity is a question for
        # the by-hand list; here it just has to be pinned down.
        nirimsg action focus-window --id "$movedid" >/dev/null
        # Whole line, not a substring: tee hides the exit status, so this grep
        # is all that stands between a refusal and a green run - and a refusal
        # that names the desk it could not reach contains "vshop.winit" too.
        # vshop is entered on code, which is where the journal left it: the
        # raw focus-workspace above went behind zded's back and did not move
        # what it remembers.
        zde desk move-window-to vshop 2>&1 | tee /tmp/mwt-out.txt
        grep -qx 'vshop.winit.code' /tmp/mwt-out.txt
        # Out again, and it is the same window that left: it is on the
        # workspace vshop was entered on, and it is what you are looking at -
        # that workspace is empty, so here the carried window is the only thing
        # focus can be.
        [ "$(winws "$movedid")" = "$(wsid vshop.winit.code)" ] || {
          echo "window $movedid did not come back out: it is on workspace $(winws "$movedid")"
          nirimsg windows; exit 1
        }
        [ "$(focused_window)" = "$moved" ] || {
          echo "window $moved came out of the band and focus is on $(focused_window)"; exit 1
        }

        # Band-clamped scrolling against a real strip. zde names the workspace
        # it wants rather than asking niri to step, so escaping the desk is
        # structurally impossible here; what this can catch is the band being
        # the wrong set - too wide, in the wrong order, or on the wrong screen
        # - which is what the walk and the focus check below are for.
        focused_workspace() {
          nirimsg --json workspaces 2>/dev/null | tr '{' '\n' \
            | grep '"is_focused":[[:space:]]*true' \
            | grep -o '"name":[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/'
        }
        zde desk switch vshop >/dev/null
        : > /tmp/ws-walk.txt
        for i in $(seq 8); do zde workspace next >>/tmp/ws-walk.txt 2>&1; done
        [ -s /tmp/ws-walk.txt ] || { echo "the scroll never moved at all"; exit 1; }
        if grep -qv '^vshop\.winit\.' /tmp/ws-walk.txt; then
          echo "a scroll left the desk:"; cat /tmp/ws-walk.txt; exit 1
        fi
        # And the end of the band is silent rather than wrapping round.
        zde workspace next 2>&1 | tee /tmp/ws-end.txt
        [ ! -s /tmp/ws-end.txt ] || { echo "the band did not end"; exit 1; }

        # niri's own answer, not zde's: every check above this line believes
        # what the daemon said about where it went.
        landed=$(tail -1 /tmp/ws-walk.txt)
        here=$(focused_workspace)
        [ -n "$here" ] || { echo "could not read the focused workspace from niri"; exit 1; }
        [ "$here" = "$landed" ] || {
          echo "zde said it focused $landed, niri has $here focused"; exit 1
        }

        # The queue, which is the half of 0.1 that is not desks. What makes it
        # a queue rather than a list is that each item knows the desk it was
        # taken on, so jumping has somewhere to go: added from vshop, jumped to
        # from haven, and the jump has to cross back.
        zde desk switch vshop >/dev/null
        zde queue add reply to ilya about the invoice 2>&1 | tee /tmp/q-add.txt
        grep -q 'reply to ilya about the invoice' /tmp/q-add.txt
        id=$(cut -f1 /tmp/q-add.txt)
        [ -n "$id" ] || { echo "no id came back"; cat /tmp/q-add.txt; exit 1; }

        zde queue 2>&1 | tee /tmp/q-list.txt
        grep -q "^$id	.	vshop	-	reply to ilya" /tmp/q-list.txt

        # A second one, newer and on another desk. With one item a queue that
        # went to the newest and one that went to the oldest are the same
        # queue, and so are ids counted from the journal and from the length of
        # the list - so the check that follows would prove neither.
        zde desk switch haven >/dev/null
        zde queue add look at the build log 2>&1 | tee /tmp/q-add2.txt
        id2=$(cut -f1 /tmp/q-add2.txt)
        [ "$id2" != "$id" ] || { echo "both reminders got id $id"; exit 1; }
        zde queue > /tmp/q-list2.txt 2>&1
        grep -q "^$id	.	vshop	-	" /tmp/q-list2.txt
        grep -q "^$id2	.	haven	-	look at the build log" /tmp/q-list2.txt
        # Oldest first, which is the order the jump below follows.
        [ "$(head -1 /tmp/q-list2.txt | cut -f1)" = "$id" ] || {
          echo "the list is not oldest first:"; cat /tmp/q-list2.txt; exit 1
        }

        # Standing on haven, where the newer one waits: the jump has to cross
        # back to vshop, because that is where the older one is.
        zde desk queue-jump 2>&1 | tee /tmp/q-jump.txt
        grep -q 'vshop.winit' /tmp/q-jump.txt
        # Jumping is not finishing: it is still there afterwards.
        zde queue 2>&1 | tee /tmp/q-still.txt
        grep -q "^$id	" /tmp/q-still.txt

        # A notification from something that has never heard of zde. This is
        # the whole point of zded being the notification server rather than
        # talking to one: notify-send speaks the freedesktop spec at whatever
        # holds the bus name, and what it says lands on the desk it arrived on
        # instead of on a popup nobody was looking at.
        zde desk switch vshop >/dev/null
        notify-send --urgency=critical "the build failed" "on the third try"
        arrived() { zde queue >/tmp/q-notify.txt 2>&1 && grep -q 'the build failed' /tmp/q-notify.txt; }
        if ! waitfor 15 arrived; then
          echo "the notification never reached the queue:"
          cat /tmp/q-notify.txt /tmp/zded-live.log; exit 1
        fi
        # Urgent, on the desk it arrived on, and attributed to what claimed to
        # send it - the claim being all anybody has until zinc gives each app
        # its own bus socket.
        grep -q '	!	vshop	notify-send	the build failed' /tmp/q-notify.txt || {
          echo "the notification arrived wrong:"; cat /tmp/q-notify.txt; exit 1
        }
        nid=$(grep 'the build failed' /tmp/q-notify.txt | cut -f1)
        zde queue done "$nid"

        zde queue done "$id"
        zde queue done "$id2"
        zde queue 2>&1 | tee /tmp/q-empty.txt
        [ ! -s /tmp/q-empty.txt ] || { echo "the queue did not empty:"; cat /tmp/q-empty.txt; exit 1; }

        # Mod+w with nothing listening, which is this zded: the session target
        # was stopped long ago, so there is no shell here. The key still has to
        # do something, so it prints what it would have shown - one window per
        # line, id first, tab separated, so a session whose shell has died can
        # still be steered and so what is open can be grepped at all.
        zde window jump-to 2>&1 | tee /tmp/jump-list.txt
        # One row, matched on its shape: the id, then the zde name of the
        # workspace it is on, then the app. Taken out of the list rather than
        # off the top of it, so this says nothing about the order and everything
        # about the columns.
        row=$(grep -m1 '^[0-9][0-9]*	[a-z][a-z0-9-]*\.winit\.[a-z0-9-]*	foot' /tmp/jump-list.txt) || true
        [ -n "$row" ] || {
          echo "the printed list is not id, then the zde workspace name, then the app:"
          cat /tmp/jump-list.txt; nirimsg windows; exit 1
        }
        jid=$(printf '%s\n' "$row" | cut -f1)
        jws=$(printf '%s\n' "$row" | cut -f2)
        jdesk=''${jws%%.*}
        # Standing on a desk that is not the window's, whichever row this is, so
        # that the jump below crosses a desk rather than moving within one.
        if [ "$jdesk" = "vshop" ]; then
          zde desk switch haven >/dev/null
        else
          zde desk switch vshop >/dev/null
        fi

        zde window jump-to "$jid" 2>&1 | tee /tmp/jump.txt
        grep -qx "$jws" /tmp/jump.txt || {
          echo "jumping to window $jid says it landed somewhere other than $jws:"
          cat /tmp/jump.txt; exit 1
        }
        # niri's answer, not zded's, and by id: "a window is focused" is true
        # whatever the jump did, and counting windows cannot fail at all.
        [ "$(focused_window)" = '"id":'"$jid" ] || {
          echo "jumped to window $jid and niri has $(focused_window) focused"
          nirimsg windows; exit 1
        }
        # And the desk came up with it. A jump that only focused the window
        # would leave the session standing on the desk it started from, which is
        # a desk on one screen and another desk on the rest.
        zde status 2>&1 | tee /tmp/jump-status.txt
        grep -q "^on desk    $jdesk" /tmp/jump-status.txt || {
          echo "jumped to a window on $jdesk and zded is somewhere else:"
          cat /tmp/jump-status.txt; exit 1
        }

        echo "live compositor check passed"
  '';
in
pkgs.testers.runNixOSTest {
  name = "zde-smoke";

  # Shorter than the workflow's job timeout, so a hang is reported by the test
  # driver with the VM's console in the log rather than killed by GitHub with
  # nothing to read.
  globalTimeout = 1500;

  nodes.machine = {
    imports = [
      zdeModule
      homeManagerModule
      ../zde-user.nix
    ];

    # zde.laptop stays off here on purpose: it would hand the VM's network to
    # NetworkManager and make anything network-shaped in this script flaky.
    # nix/test-host.nix evaluates that branch instead.
    zde.enable = true;

    # Only so that shell_interact and a manual login work when debugging this
    # test; most assertions below run as root.
    users.users.zde.password = "zde";

    # Somebody else on the machine, so that "the zde socket is private" can be
    # asserted by a user who is actually subject to it - root is not.
    users.users.intruder.isNormalUser = true;

    home-manager.users.zde = {
      # Layer 2's tools, the way a real machine gets them: zinc's own
      # home-manager module, which is the shape it says zde should install it
      # in.
      imports = [ zincModule ];
      programs.zinc.enable = true;
      # zlg is opt-in in zinc's module, on the reasoning that a desktop shipping
      # its own launcher does not want a second one. zde ships none: its
      # generated keymap binds Mod+g to `zlg` already (common/keymap), so
      # leaving it out is not declining a second launcher, it is a bound key
      # that spawns nothing.
      programs.zinc.tools = [
        "zc"
        "zcr"
        "zlt"
        "zlg"
      ];

      # The locker, pointed at something that locks nothing. The leader check
      # below runs it for real - that is the point, it proves the whole chain
      # from a keypress to an exec - and a VM with no input devices that locked
      # itself could never unlock itself, taking every assertion after it down.
      #
      # It writes a file rather than exiting 0, so the test can see that it ran
      # rather than only that nothing complained.
      zde = {
        apps.lock = [ "${fakeLocker}/bin/swaylock" ];

        # A second keyboard layout, which is what Mod+space switches between.
        # With one layout that key does nothing, which is what every zde
        # session has done so far - and a machine you cannot write to somebody
        # in is not one anybody travels with. Set here so the generated
        # local.kdl is what niri validates below, rather than a shape nothing
        # has ever parsed.
        niri.xkb = {
          layout = "us,ru";
          options = "grp:caps_toggle";
        };

        # A host's own binds, through the seam that exists for them: local.kdl,
        # included after the generated ones. The live image (nix/live.nix) puts
        # a terminal on a key this way, because nothing in the keymap can open
        # one yet - so niri accepting a second binds block is load-bearing
        # rather than incidental, and the niri validate below is what keeps it
        # that way.
        niri.extraConfig = ''
          binds {
              Mod+Return { spawn "foot"; }
          }
        '';
      };
    };

    # cage hosts the nested niri; mesa's software rasteriser is what both of
    # them render with.
    environment.systemPackages = [
      pkgs.cage
      pkgs.foot # a Wayland client that runs without a GPU, for adoption
      pkgs.dbus # dbus-launch, for a session bus to be the notification server on
      pkgs.libnotify # notify-send: an app that has never heard of zde
    ];

    virtualisation = {
      memorySize = 2048;
      cores = 2;
    };
  };

  testScript =
    { nodes, ... }:
    ''
      machine.wait_for_unit("multi-user.target")

      # Layer 0: the greeter is up and everything a session is launched from is
      # installed.
      machine.wait_for_unit("greetd.service")
      machine.wait_until_succeeds("pgrep -f tuigreet")
      machine.succeed("test -x /run/current-system/sw/bin/niri-session")
      machine.succeed("test -f /run/current-system/sw/share/xdg-desktop-portal/niri-portals.conf")

      # Layer 2's tools arrived, and the contract zde leans on holds against the
      # real binary. `zcr where` is what zde asks rather than joining that path
      # itself: the layout is zinc's, and two copies of it would drift the first
      # time either side moved.
      machine.succeed("su -l zde -c 'command -v zcr'")
      machine.succeed("su -l zde -c 'command -v zc'")
      # And zlg, which is not a nicety: Mod+g in the generated keymap spawns it
      # by name (common/keymap/keymap.yaml, launcher.open). Without it that key
      # is bound to a binary the machine does not have, which looks from the
      # keyboard exactly like a key that does nothing.
      machine.succeed("su -l zde -c 'command -v zlg'")
      # zinc 0.9 made `zcr where` load the config, so it answers only for apps
      # that exist. zc init is zinc's own seeding, which beats this test writing
      # an app file and owning zinc's schema to keep it parsing.
      machine.succeed("su -l zde -c 'zc init'")
      where = machine.succeed(
          "su -l zde -c 'XDG_STATE_HOME=/tmp/st zcr where example-instanced@work'"
      )
      assert "state: /tmp/st/zinc/example-instanced/work" in where, where
      # Both halves of an address are a name, which is the rule a desk manifest
      # feeds: zde refuses a path in either field, and so does zinc.
      machine.fail("su -l zde -c 'zcr where \"example-instanced@../../etc\"'")
      machine.fail("su -l zde -c 'zcr where \"../../../etc\"'")

      # The screen lock can authenticate. This is the trap worth a test rather
      # than the locking itself: a locker with no PAM service takes the screen
      # and then refuses every password, which is not a locked screen but a lost
      # machine. niri's own module provides it, and this is what notices if that
      # ever stops being true.
      #
      # Locking for real is a by-hand item (docs/verify.md): this VM has no
      # input devices, so anything that locked it could not unlock it.
      machine.succeed("test -e /etc/pam.d/swaylock")

      # The backlight rules reached udev, which is the difference between the
      # brightness keys working and failing for everyone who is not root. They
      # arrive only through services.udev.packages: installing brightnessctl
      # the package, which layer 1 does, puts its rules somewhere nothing
      # reads. Asserted on the file udev actually loads.
      machine.succeed("test -e /etc/udev/rules.d/90-brightnessctl.rules")
      machine.succeed("grep -q 'chgrp video' /etc/udev/rules.d/90-brightnessctl.rules")

      # niri's user units reached systemd. This is what stands between a login
      # and a greeter that silently takes it straight back: niri-session starts
      # niri.service, and a build that installs those units where NixOS does not
      # glob for them (share/ rather than lib/) produces exactly that, with
      # nothing on screen and nothing in a log. It held for a whole PR here.
      machine.succeed("test -f /etc/systemd/user/niri.service")

      # The session entry, asserted on the package because the system profile
      # does not link share/wayland-sessions.
      machine.succeed(
          "test -f ${nodes.machine.programs.niri.package}/share/wayland-sessions/niri.desktop"
      )

      # Layer 1: home-manager put the generated config and the cheatsheet in
      # place, and the config is the very build the flake exposes - the claim
      # that layer 1 and the flake produce identical output (docs/delivery.md).
      # Its activation is a oneshot that does not linger, so this waits on what
      # it produced rather than on the unit ever being active.
      machine.wait_until_succeeds("test -L /home/zde/.config/niri/config.kdl")
      # LoadState first: systemctl reports Result=success for a unit that does
      # not exist, so asking about Result alone would pass a typo.
      machine.succeed(
          "systemctl show -p LoadState --value home-manager-zde.service | grep -qx loaded"
      )
      machine.succeed(
          "systemctl show -p Result --value home-manager-zde.service | grep -qx success"
      )
      machine.succeed("test -s /home/zde/.config/zde/keymap-cheatsheet.md")
      machine.succeed(
          'test "$(readlink -f /home/zde/.config/niri/config.kdl)"'
          ' = "${zdeConfig}/niri-config.kdl"'
      )
      machine.succeed(
          'test "$(readlink -f /home/zde/.config/niri/binds.kdl)"'
          ' = "${zdeConfig}/binds.kdl"'
      )

      # dynamic.kdl is the one file zded writes at runtime, so it has to exist
      # before niri reads the config (a missing include is fatal) and it has to
      # be a real writable file, not a symlink into the read-only store.
      machine.succeed("test -f /home/zde/.config/niri/dynamic.kdl")
      machine.succeed("test ! -L /home/zde/.config/niri/dynamic.kdl")
      machine.succeed("su -l zde -c 'echo \"// zded was here\" >> ~/.config/niri/dynamic.kdl'")

      # The whole tree is one config this niri accepts: the nodes parse, the
      # includes resolve, and every action name the keymap emitted is real.
      # Run as the user, since the includes resolve relative to the file.
      machine.succeed("su -l zde -c 'niri validate -c ~/.config/niri/config.kdl'")

      # The layout reached the config niri reads. Two of them, because one is
      # what a session has without this and Mod+space needs somewhere to switch
      # to.
      machine.succeed("grep -q 'layout \"us,ru\"' /home/zde/.config/niri/local.kdl")
      machine.succeed("grep -q 'grp:caps_toggle' /home/zde/.config/niri/local.kdl")

      # A broken include has to fail loudly rather than boot into a default
      # config: this is the seam zded writes through every day.
      machine.succeed("su -l zde -c 'mv ~/.config/niri/dynamic.kdl ~/dyn.bak'")
      machine.fail("su -l zde -c 'niri validate -c ~/.config/niri/config.kdl'")
      machine.succeed("su -l zde -c 'mv ~/dyn.bak ~/.config/niri/dynamic.kdl'")
      machine.succeed("su -l zde -c 'niri validate -c ~/.config/niri/config.kdl'")

      # Every binary the binds spawn has to be on the user's PATH, or the key
      # does nothing and says nothing. zlg is zinc's launcher and is the last
      # exception left; that list shrinks, it must not grow.
      spawned = machine.succeed(
          "grep -ho 'spawn \"[^\"]*\"' /home/zde/.config/niri/*.kdl"
          " | cut -d'\"' -f2 | sort -u"
      ).split()
      # The binds moved to binds.kdl once the config was split, and a grep of
      # the wrong file would make everything below pass by finding nothing.
      assert len(spawned) >= 3, f"expected the binds to spawn several commands, found {spawned}"
      missing = [
          c for c in spawned
          if c not in ("zlg",)
          and machine.execute(f"su -l zde -c 'command -v {c}'")[0] != 0
      ]
      assert missing == [], f"binds spawn commands that are not installed: {missing}"

      # And one level down: a bind that spawns `zde app launch help` resolves to
      # zde, which the check above is satisfied by - what the name resolves to
      # lives in apps.json, and a program missing there is the same silent key
      # with one more step in front of it.
      import json
      apps = json.loads(machine.succeed("cat /home/zde/.config/zde/apps.json"))
      assert "help" in apps, f"no help app, so Mod+slash opens nothing: {apps}"
      for name, argv in apps.items():
          machine.succeed(f"su -l zde -c 'test -x {argv[0]}'")

      # The keymap as a key shows it. `zde help` printed usage to a stderr no
      # keypress has, which on a desktop where most keys are silent made the one
      # key that says which keys work another silent one.
      keys = machine.succeed("su -l zde -c 'zde keys'")
      assert "Mod+Tab" in keys, keys
      # Plain text, not the markdown cheatsheet: this one goes through a pager.
      assert "|" not in keys and "`" not in keys, keys

      # Screenshots are niri's own actions rather than a `zde capture` nobody
      # has written. Asserted in the generated binds because the check above
      # cannot see it: those keys spawned `zde`, which is installed, and did
      # nothing at all when pressed.
      binds = machine.succeed("cat /home/zde/.config/niri/binds.kdl")
      for shot in ("{ screenshot; }", "{ screenshot-screen; }", "{ screenshot-window; }"):
          assert shot in binds, f"no {shot} bind: {binds}"

      # The unit that starts the daemon with the session. Every check in this
      # file runs zded by hand, which is the one thing a person never does:
      # on a login it is niri that brings up graphical-session.target, and
      # this is what has to be hanging off it. Without the symlink the session
      # comes up with every desk key silent and nothing to say why.
      machine.succeed(
          "test -L /home/zde/.config/systemd/user/graphical-session.target.wants/zded.service"
      )

      # That is the precise-cause check, and on its own it is a weak one: a
      # unit can be wanted by the target and still be wrong in every way that
      # matters. Starting it the way a login does needs a compositor to import
      # an environment from, so it happens in the live check below, where there
      # is one. What this does is give the user manager the linger that check
      # needs - su gives no session (the podman check at the end of this file
      # leans on the same fact from the other side).
      uid = machine.succeed("id -u zde").strip()
      machine.succeed("loginctl enable-linger zde")
      machine.wait_until_succeeds(f"systemctl is-active user@{uid}.service")

      # zded comes up and answers its socket. There is no compositor in a
      # build sandbox, which is the point: the daemon everyone asks why the
      # session is broken has to run, and say so, when niri is not there.
      machine.succeed("su -l zde -c 'mkdir -p /tmp/rt'")
      machine.succeed(
          "su -l zde -c 'XDG_RUNTIME_DIR=/tmp/rt nohup zded >/tmp/zded.log 2>&1 &'"
      )
      machine.wait_until_succeeds("test -S /tmp/rt/zde/zded.sock")
      status = machine.succeed("su -l zde -c 'XDG_RUNTIME_DIR=/tmp/rt zde status'")
      assert "zded" in status, status
      # It reports the missing compositor rather than claiming a working one.
      assert "NIRI_SOCKET" in status, status

      # The socket is the zde boundary (docs/vision.md, principle 7). Asserted
      # as another user, because root is not subject to the mode bits and
      # would pass this test no matter what zded did.
      machine.fail("su -l intruder -c 'test -r /tmp/rt/zde/zded.sock'")
      machine.fail("su -l intruder -c 'ls /tmp/rt/zde'")

      # A real compositor: zded and zde against niri itself, not a fake.
      machine.succeed("su -l zde -c ${liveCheck}", timeout=600)

      # Rootless podman, what zcr will run apps with. su gives no logind
      # session, so this exercises podman's cgroupfs fallback rather than the
      # systemd path a real login would take.
      machine.succeed("su -l zde -c 'podman info'")
    '';
}
