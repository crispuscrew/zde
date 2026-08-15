# The zde user wherever one is stood up: layer 1 on, for one normal user.
# Shared by nix/test-host.nix (what nix flake check evaluates), the smoke test
# node (what QEMU boots) and nix/live.nix (what a stick boots), so the three
# cannot drift apart. What differs between them - placeholder disks, a VM size,
# a password anyone can read - stays there.
{
  users.users.zde.isNormalUser = true;

  home-manager = {
    # The same two lines the template sets, and here for the same reason the
    # rest of this file exists: a check is worth something only if it evaluates
    # what a machine evaluates. Without them home-manager instantiates its own
    # nixpkgs, so these three hosts would be the only zde systems where layer
    # 1's packages come out of a package set nothing on the machine can
    # influence - and the regression that put the template in that state would
    # pass every check. templates/host/flake.nix carries the argument for both.
    useGlobalPkgs = true;
    useUserPackages = true;

    users.zde = {
      imports = [ ./home.nix ];
      zde.enable = true;
      # The release a machine installed from the template is created at
      # (templates/host/flake.nix), and therefore the one every check should be
      # evaluating. It was 25.05 while the template said 26.05, which made these
      # three hosts the only zde installations in existence with that number:
      # the live image booted a system.stateVersion of 26.05 with a
      # home.stateVersion of 25.05 in it, and nothing anybody would ever run was
      # being checked.
      #
      # It is not a machine's creation stamp here, for the same reason it is not
      # one in the template (docs/update.md, Bumping): nothing persistent is
      # ever created from this file, so it moves with the release rather than
      # being frozen at one. home-manager keys option defaults off it - in this
      # release xdg.userDirs.extraConfig changes shape at exactly the 26.05
      # boundary - so a stale number here is a different module system from the
      # one a machine evaluates, quietly.
      home.stateVersion = "26.05";
    };
  };
}
