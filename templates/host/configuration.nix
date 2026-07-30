# Your machine, not zde's. Everything zde needs is in the module the flake
# imports; what is left here is what only you can know.
_: {
  # The user zde runs as. The name has to match `user` in flake.nix.
  users.users.you = {
    isNormalUser = true;
    description = "you";
    extraGroups = [
      "wheel" # sudo
      "video" # the brightness keys: brightnessctl's udev rules hand the
      # backlight to this group, and layer 0 installs them
      "networkmanager" # only does anything with zde.laptop.enable
    ];

    # Something to type at the greeter on the first boot, and change straight
    # after with `passwd`. Without it the account is created locked: the
    # greeter takes the name, refuses every password, and there is no session
    # to fix it from.
    #
    # It is initial in the literal sense - it applies when the account is
    # created and never again, so `passwd` sticks. Until you run it, this
    # password sits in the world-readable nix store. Skip both by putting a
    # hash here instead: `mkpasswd -m yescrypt` into initialHashedPassword.
    initialPassword = "zde";
  };

  # Has to match `host` in flake.nix. They are two files because one is yours
  # and one is the flake's, and nothing checks that they agree: if they
  # diverge, `nixos-rebuild switch --flake .` with no attribute stops finding
  # this machine, because it looks itself up by hostname.
  networking.hostName = "zdebox";
  time.timeZone = "Europe/Belgrade";

  # The bootloader, and one of the two guesses in this file. systemd-boot is
  # right for UEFI, which is nearly every machine made since about 2012, and
  # wrong for anything that boots the old way - `nixos-generate-config
  # --show-hardware-config` prints the hardware and nothing else, so it will
  # not correct this for you. If /sys/firmware/efi does not exist, this
  # machine is not UEFI: delete these two lines and use GRUB instead.
  #
  #   boot.loader.grub.enable = true;
  #   boot.loader.grub.device = "/dev/sda";   # the disk, not a partition
  #
  # Getting it wrong builds and activates fine and then fails inside the
  # bootloader install, at the end of a switch, which is a long way from here.
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
  # leave it at whatever it was on the day you installed, for ever. (In the
  # template it is the exception that proves it: here it is what a new
  # install starts at, so it moves with each release - docs/update.md.)
  system.stateVersion = "26.05";
}
