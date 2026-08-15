# A throwaway host that turns zde on, both layers, so `nix flake check`
# evaluates them (checks.zde-nixos-eval). Nothing here describes a real
# machine: the disk and the bootloader are placeholders that satisfy the NixOS
# module system, and nothing is ever built from this file.
{ config, ... }:
{
  imports = [ ./zde-user.nix ];

  # Layer 0, laptop bits included so that branch gets evaluated too. The smoke
  # test leaves them off, so they are evaluated here and booted nowhere.
  zde.enable = true;
  zde.laptop.enable = true;

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
  ];

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
