# The zde user as both test hosts stand it up: layer 1 on, for one normal user.
# Shared by nix/test-host.nix (what nix flake check evaluates) and the smoke
# test node (what QEMU boots), so the two cannot drift apart. What differs
# between them - placeholder disks, or a password and a VM size - stays there.
{
  users.users.zde.isNormalUser = true;

  home-manager.users.zde = {
    imports = [ ./home.nix ];
    zde.enable = true;
    home.stateVersion = "25.05";
  };
}
