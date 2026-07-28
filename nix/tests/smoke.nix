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
}:
let
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

        # A desk that does not exist yet, declared.
        printf 'name: vshop
    monitors:
      winit: { workspaces: [code, notes] }
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
        sctl import-environment NIRI_SOCKET
        # Pulled in rather than started: graphical-session.target refuses a
        # manual start, by design - it is meant to arrive as somebody's
        # dependency, which on a login is niri.service. NixOS ships this stand-in
        # for sessions that do not speak systemd, and it binds to the target the
        # same way, so what starts here is the real one.
        sctl start nixos-fake-graphical-session.target
        zded_unit() { sctl is-active --quiet zded.service; }
        if ! waitfor 30 zded_unit; then
          echo "the target came up and zded did not follow it:"
          sctl status zded.service || true
          journalctl --user -u zded.service --no-pager | tail -20; exit 1
        fi
        # And it can see the compositor, which is the whole reason the unit
        # waits for the target rather than racing niri: NIRI_SOCKET is in the
        # environment only after niri has put it there.
        XDG_RUNTIME_DIR=$mgr zde status 2>&1 | tee /tmp/unit-status.txt
        grep -qx 'compositor connected' /tmp/unit-status.txt

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
        # do. It has to talk to niri rather than to the cage hosting it, and
        # niri names its IPC socket after the Wayland display it opened.
        export WAYLAND_DISPLAY=$(basename "$NIRI_SOCKET" | cut -d. -f2)
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
        focused_window() { nirimsg --json focused-window 2>/dev/null | grep -o '"id":[0-9]*' | head -1; }
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
        # Not declared, because they cannot be: a manifest named regulars is
        # refused where manifests are read - the regulars are the band that is
        # not a desk. Naming a workspace into the band is the only way they
        # come into being, so that is what this does, and what the error on a
        # machine without them tells you to do.
        for i in $(seq 10); do nirimsg action focus-workspace-down >/dev/null; done
        nirimsg action set-workspace-name regulars.winit.comms >/dev/null

        # Naming the band left focus sitting on it, so the desk to come back to
        # is chosen here rather than inherited: what is being checked is
        # reaching the regulars from somewhere and landing back on that
        # somewhere, which needs the somewhere to be known.
        zde desk switch haven >/dev/null
        zde desk regulars 2>&1 | tee /tmp/reg.txt
        grep -qx 'regulars.winit.comms' /tmp/reg.txt

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
        zde desk move-window-to regulars 2>&1 | tee /tmp/mwt-in.txt
        grep -qx 'regulars.winit.comms' /tmp/mwt-in.txt
        [ "$(focused_window)" = "$moved" ] || {
          echo "window $moved did not arrive in the band: focus is on $(focused_window)"; exit 1
        }
        # Whole line, not a substring: tee hides the exit status, so this grep
        # is all that stands between a refusal and a green run - and a refusal
        # that names the desk it could not reach contains "vshop.winit" too.
        # vshop is entered on code, which is where the journal left it: the
        # raw focus-workspace above went behind zded's back and did not move
        # what it remembers.
        zde desk move-window-to vshop 2>&1 | tee /tmp/mwt-out.txt
        grep -qx 'vshop.winit.code' /tmp/mwt-out.txt
        [ "$(focused_window)" = "$moved" ] || {
          echo "window $moved did not come back out: focus is on $(focused_window)"; exit 1
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

    # A host's own binds, through the seam that exists for them: local.kdl,
    # included after the generated ones. The live image (nix/live.nix) puts a
    # terminal on a key this way, because nothing in the keymap can open one
    # yet - so niri accepting a second binds block is load-bearing rather than
    # incidental, and the niri validate below is what keeps it that way.
    home-manager.users.zde.zde.niri.extraConfig = ''
      binds {
          Mod+Return { spawn "foot"; }
      }
    '';

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

      # The input layer is up. There is no keyboard in here to intercept, and
      # that is the point of the check: kanata is told to carry on when it
      # finds no devices, so a running unit means the binary started and took
      # our config. Whether held Tab actually reaches niri as F13 wants a
      # keyboard, and stays on the roadmap's verify list.
      machine.wait_for_unit("kanata-zde.service")

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
