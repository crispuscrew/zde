# A throwaway host that turns zde on, both layers, so `nix flake check`
# evaluates them (checks.zde-nixos-eval). Nothing here describes a real
# machine: the disk and the bootloader are placeholders that satisfy the NixOS
# module system, and nothing is ever built from this file.
{
  imports = [ ./zde-user.nix ];

  # Layer 0, laptop bits included so that branch gets evaluated too. The smoke
  # test leaves them off, so they are evaluated here and booted nowhere.
  zde.enable = true;
  zde.laptop.enable = true;

  boot.loader.grub.devices = [ "/dev/sda" ];
  fileSystems."/" = {
    device = "/dev/sda1";
    fsType = "ext4";
  };
  system.stateVersion = "25.05";
}
