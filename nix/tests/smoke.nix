# Smoke test: boot zde in a QEMU VM and check that both layers actually arrive
# on a machine. This is where the host eval in `nix flake check` stops being
# enough - that one proves the modules evaluate, not that a machine comes up
# with a greeter and a config niri accepts.
#
# The compositor itself never starts here: a nix build sandbox has no GPU and
# niri has no software renderer, so a running session stays a real-hardware
# item (docs/roadmap.md, verify list). What runs instead is `niri validate`,
# the same parser niri uses on its own config at startup.
{
  pkgs,
  zdeModule,
  homeManagerModule,
  zdeConfig,
}:
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

      # Rootless podman, what zcr will run apps with. su gives no logind
      # session, so this exercises podman's cgroupfs fallback rather than the
      # systemd path a real login would take.
      machine.succeed("su -l zde -c 'podman info'")
    '';
}
