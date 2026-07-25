# A throwaway host that turns zde on, both layers, so `nix flake check`
# evaluates them (checks.zde-nixos-eval). Nothing here describes a real
# machine: the disk and the bootloader are placeholders that satisfy the NixOS
# module system, and nothing is ever built from this file.
{
  # Layer 0, laptop bits included so that branch gets evaluated too.
  zde.enable = true;
  zde.laptop.enable = true;

  # Layer 1, the same home module the portable path uses.
  home-manager.users.zde = {
    imports = [ ./home.nix ];
    zde.enable = true;
    home.stateVersion = "25.05";
  };

  users.users.zde.isNormalUser = true;

  boot.loader.grub.devices = [ "/dev/sda" ];
  fileSystems."/" = {
    device = "/dev/sda1";
    fsType = "ext4";
  };
  system.stateVersion = "25.05";
}
