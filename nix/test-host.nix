# A throwaway host that turns zde on, both layers, so `nix flake check`
# evaluates them (checks.zde-nixos-eval). Nothing here describes a real
# machine: the disk and the bootloader are placeholders that satisfy the NixOS
# module system, and nothing is ever built from this file.
{
  config,
  lib,
  options,
  pkgs,
  ...
}:
let
  sleepActions = [
    "org.freedesktop.login1.suspend"
    "org.freedesktop.login1.suspend-multiple-sessions"
    "org.freedesktop.login1.suspend-ignore-inhibit"
    "org.freedesktop.login1.hibernate"
    "org.freedesktop.login1.hibernate-multiple-sessions"
    "org.freedesktop.login1.hibernate-ignore-inhibit"
  ];
  sleepPolicy = config.environment.etc."polkit-1/rules.d/00-zde-no-sleep.rules".text;
  ignoredLogindActions = [
    "HandlePowerKey"
    "HandlePowerKeyLongPress"
    "HandleSuspendKey"
    "HandleSuspendKeyLongPress"
    "HandleHibernateKey"
    "HandleHibernateKeyLongPress"
    "HandleLidSwitch"
    "HandleLidSwitchExternalPower"
    "HandleLidSwitchDocked"
    "IdleAction"
  ];
in
{
  imports = [ ./zde-user.nix ];

  # Keep the independent hardware switches off under laptop mode. This host is
  # evaluation-only; the smoke test boots the inverse, brightness without
  # laptop mode.
  zde = {
    enable = true;
    laptop.enable = true;
    networking.enable = false;
    brightness.enable = false;
  };

  # A host cannot accidentally reopen a desktop sleep path.
  services.logind.settings.Login.HandleLidSwitch = "suspend";

  # This host is evaluated by the nixpkgs zde itself pins, so it is the one
  # place that can check the release warning against reality. Without it, a
  # release bump that edits flake.nix and forgets nix/system.nix ships a zde
  # that warns on every correctly pinned machine - telling the user their
  # system is wrong when it is zde that is wrong - and CI stays green, because
  # a warning changes nothing it looks at.
  assertions = [
    {
      assertion = config.warnings == [ ];
      message = "zde warns about its own pinned nixpkgs: ${toString config.warnings}";
    }
    {
      assertion = options.zde.networking.enable.default;
      message = "zde.networking.enable no longer defaults on for desktops";
    }
    {
      assertion = options.zde.brightness.enable.default;
      message = "zde.brightness.enable no longer defaults on for desktops";
    }
    {
      assertion = config.services.upower.enable && config.services.power-profiles-daemon.enable;
      message = "zde.laptop.enable does not enable its battery and power services";
    }
    {
      assertion = lib.all (name: config.services.logind.settings.Login.${name} == "ignore") ignoredLogindActions;
      message = "a logind key, lid, long-press, or idle action can still sleep the machine";
    }
    {
      assertion = lib.all (action: lib.hasInfix action sleepPolicy) sleepActions;
      message = "the root-owned polkit rule does not deny every systemd 260.2 sleep action";
    }
    {
      assertion = lib.hasInfix ''subject.user !== "root"'' sleepPolicy;
      message = "the sleep policy does not preserve root's administrative boundary";
    }
    {
      assertion = builtins.hasAttr "zde/laptop" config.environment.etc;
      message = "zde.laptop.enable does not declare the laptop to the user layer";
    }
    {
      assertion = lib.versionAtLeast pkgs.podman.version "5.8.6";
      message = "the pinned Podman is older than 5.8.6";
    }
    {
      assertion = lib.versionAtLeast pkgs.kitty.version "0.48.2";
      message = "the pinned Kitty is older than 0.48.2";
    }
    {
      assertion = lib.versionAtLeast pkgs.systemd.version "260.2";
      message = "the pinned systemd is older than the sleep policy's 260.2 action map";
    }
    {
      assertion = !config.networking.networkmanager.enable;
      message = "zde.laptop.enable overrides zde.networking.enable = false";
    }
    {
      assertion = !(lib.any (package: lib.getName package == "brightnessctl") config.services.udev.packages);
      message = "zde.laptop.enable overrides zde.brightness.enable = false";
    }
    {
      assertion = !config.hardware.bluetooth.enable;
      message = "zde.laptop.enable unexpectedly enables bluetooth";
    }
    {
      assertion =
        let
          bluezPackage = config.hardware.bluetooth.package;
          declaredCommits = bluezPackage.passthru.zdeAvrcpPatchCommits or [ ];
          patchMatches = map (
            patch: builtins.match ".*-bluez-avrcp-([0-9a-f]{40})[.]patch" (toString patch)
          ) (lib.takeEnd 2 bluezPackage.patches);
        in
        declaredCommits == [
          "bd8989620ed6e80755f06cfdb18f5b4a3913493c"
          "58088149872d014684a582fdb7ad01a5180c9bc5"
        ]
        && lib.all (match: match != null) patchMatches
        && map builtins.head patchMatches == declaredCommits;
      message = "the configured BlueZ package is missing the ordered AVRCP parser backports";
    }
  ];

  # A host that writes its own niri, because that is the half of local.kdl
  # nothing used to parse (nix/niri-local.nix). Both halves are what the docs
  # actually hand out: the output block is the one templates/host/flake.nix
  # ships commented out, and the xkb pair is what docs/install.md section 2
  # gives a laptop on its first evening. So what CI runs niri's parser over is
  # what a person is being told to write, rather than a sample nobody copies.
  home-manager.users.zde.zde.niri = {
    xkb = {
      layout = "us,ru";
      options = "grp:caps_toggle";
    };
    extraConfig = ''
      output "eDP-1" {
          scale 2
      }
    '';
  };
  # Evaluate the opt-in replay package backport and unit without starting a
  # portal or requiring a GPU in the VM smoke test.
  home-manager.users.zde.zde.capture.replay.enable = true;

  boot.loader.grub.devices = [ "/dev/sda" ];
  fileSystems."/" = {
    device = "/dev/sda1";
    fsType = "ext4";
  };
  # The release the template creates a machine at, so this throwaway host and a
  # real one evaluate the same NixOS. The live image already gets 26.05 from the
  # installer CD, so this was the last host disagreeing with the other two.
  system.stateVersion = "26.05";
}
