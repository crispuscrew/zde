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

    # cage hosts the nested niri; mesa's software rasteriser is what both of
    # them render with.
    environment.systemPackages = [
      pkgs.cage
      pkgs.foot # a Wayland client that runs without a GPU, for adoption
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
