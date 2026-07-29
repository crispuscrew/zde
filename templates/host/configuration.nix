# Your machine, not zde's. Everything zde needs is in the module the flake
# imports; what is left here is what only you can know.
_: {
  # The user zde runs as. The name has to match `user` in flake.nix.
  users.users.you = {
    isNormalUser = true;
    description = "you";
    extraGroups = [
      "wheel" # sudo
      "networkmanager" # only with zde.laptop.enable
    ];
    # Set a password before the first boot, or the greeter has nothing to
    # accept: sudo passwd you. (Or set hashedPassword here and never type it.)
  };

  networking.hostName = "zdebox";
  time.timeZone = "Europe/Belgrade";

  # The bootloader. systemd-boot for UEFI, which is nearly everything now;
  # nixos-generate-config will have written the GRUB lines instead if this
  # machine boots the old way.
  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = true;

  # Flakes, because everything above is one. nixos-rebuild passes them itself,
  # but `nix flake update` from your own shell needs them enabled.
  nix.settings.experimental-features = [
    "nix-command"
    "flakes"
  ];

  # Rollback is the reason NixOS is the reference platform (docs/delivery.md),
  # and a generation you have collected is a generation you cannot roll back
  # to. Keep a month.
  nix.gc = {
    automatic = true;
    dates = "weekly";
    options = "--delete-older-than 30d";
  };

  # The version this machine was created at. It is not a version to update:
  # leave it at whatever it was on the day you installed, for ever.
  system.stateVersion = "26.05";
}
