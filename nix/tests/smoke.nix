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
    ];

    zde.enable = true;

    users.users.zde = {
      isNormalUser = true;
      password = "zde";
    };

    home-manager.users.zde = {
      imports = [ ../home.nix ];
      zde.enable = true;
      home.stateVersion = "25.05";
    };

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

      # niri's user units reached systemd. Upstream installs them where NixOS
      # does not look, so this is what stands between a login and a greeter
      # that silently takes it back (see niriFor in flake.nix).
      machine.succeed("test -f /etc/systemd/user/niri.service")

      # The session entry a greeter or display manager registers. Asserted on
      # the package because the system profile does not link
      # share/wayland-sessions, and because the postInstall that puts it there
      # is ours to maintain.
      machine.succeed(
          "test -f ${nodes.machine.zde.niri.package}/share/wayland-sessions/niri.desktop"
      )

      # Layer 1: home-manager put the generated config and the cheatsheet in
      # place, and the config is the very build the flake exposes - the claim
      # that layer 1 and the flake produce identical output (docs/delivery.md).
      # Its activation is a oneshot that does not linger, so this waits on what
      # it produced rather than on the unit ever being active.
      machine.wait_until_succeeds("test -L /home/zde/.config/niri/config.kdl")
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

      # Rootless podman, what zcr will run apps with.
      machine.succeed("su -l zde -c 'podman info'")
    '';
}
