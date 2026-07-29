# The zde user wherever one is stood up: layer 1 on, for one normal user.
# Shared by nix/test-host.nix (what nix flake check evaluates), the smoke test
# node (what QEMU boots) and nix/live.nix (what a stick boots), so the three
# cannot drift apart. What differs between them - placeholder disks, a VM size,
# a password anyone can read - stays there.
{
  users.users.zde.isNormalUser = true;

  home-manager.users.zde = {
    imports = [ ./home.nix ];
    zde.enable = true;
    home.stateVersion = "25.05";
  };
}
