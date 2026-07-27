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
# What it still is not is a session on real hardware: no GPU, no seat, no
# input, no monitors coming and going (docs/roadmap.md, verify list).
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
        set -eu
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
        for i in $(seq 60); do
          niri msg version >/dev/null 2>&1 && break
          sleep 1
        done
        niri msg version >/dev/null
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
        niri msg workspaces 2>&1 | tee /tmp/ws.txt
        grep -q 'vshop.winit.code' /tmp/ws.txt
        grep -q 'vshop.winit.notes' /tmp/ws.txt

        # The journal recorded where the desk was left.
        grep -q '"slot":"code"' /tmp/live.jsonl

        # Reconcile is quiet on a world that is already true, and niri's own
        # trailing empty workspace is left alone rather than adopted.
        zde desk reconcile 2>&1 | tee /tmp/rec.txt
        grep -qx 'nothing to reconcile' /tmp/rec.txt

        # And the desk can be written back out as a manifest.
        zde desk snapshot haven 2>/dev/null && { echo "snapshotted a desk that does not exist"; exit 1; }
        rm -f /tmp/desks/vshop.yaml
        zde desk snapshot 2>&1 | tee /tmp/snap.txt
        grep -q 'vshop.yaml' /tmp/snap.txt
        grep -q 'code' /tmp/desks/vshop.yaml
        echo "live compositor check passed"
  '';
in
pkgs.testers.runNixOSTest {
  name = "zde-smoke";

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
    environment.systemPackages = [ pkgs.cage ];

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
      machine.succeed("su -l zde -c ${liveCheck}")

      # Rootless podman, what zcr will run apps with. su gives no logind
      # session, so this exercises podman's cgroupfs fallback rather than the
      # systemd path a real login would take.
      machine.succeed("su -l zde -c 'podman info'")
    '';
}
