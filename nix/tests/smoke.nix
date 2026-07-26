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
    # test; every assertion below runs as root.
    users.users.zde.password = "zde";

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

      # The generated config is one this niri accepts: the base nodes parse and
      # every action name the keymap emitted resolves.
      machine.succeed("niri validate -c /home/zde/.config/niri/config.kdl")

      # Every binary the binds spawn has to be on the user's PATH, or the key
      # does nothing and says nothing. The zde tools are the known exception
      # until roadmap 0.1 builds them; that list shrinks, it must not grow.
      missing = machine.succeed(
          "su -l zde -c '"
          "for c in $(grep -o \"spawn \\\"[^\\\"]*\\\"\" ~/.config/niri/config.kdl"
          " | cut -d\\\" -f2 | sort -u); do"
          "  case $c in zde|zlg) continue;; esac;"
          "  command -v $c >/dev/null || echo $c;"
          "done'"
      ).strip()
      assert missing == "", f"binds spawn commands that are not installed: {missing}"

      # Rootless podman, what zcr will run apps with. su gives no logind
      # session, so this exercises podman's cgroupfs fallback rather than the
      # systemd path a real login would take.
      machine.succeed("su -l zde -c 'podman info'")
    '';
}
