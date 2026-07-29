# The live image: zde on a USB stick, which is the only way anyone has to put
# it in front of a real keyboard (docs/verify.md). Built on the minimal
# installer CD, so it boots on most machines, carries firmware for their
# hardware, and touches no disk. Everything under the session is the layer 0
# and layer 1 a real install gets - the image adds a way in and nothing else.
#
# The QEMU smoke test and this image cover different halves. That one boots a
# host with no GPU, no keyboard and one virtual screen, and asserts. This one
# has all three and asserts nothing: what it is for is the list of questions
# only hands can answer.
{
  lib,
  pkgs,
  modulesPath,
  ...
}:
{
  imports = [
    "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix"
    ./zde-user.nix
  ];

  zde.enable = true;

  # The laptop branch, which the smoke test deliberately leaves off (it would
  # hand the VM's network to NetworkManager and make the test flaky). Real
  # hardware is where it belongs anyway: battery, radios and the lid switch
  # are exactly what a VM cannot answer for.
  zde.laptop.enable = true;

  # A password, and the greeter left in the way on purpose. Autologin would be
  # friendlier and would skip the one part of the boot path nothing has ever
  # run: greetd reaching niri-session on hardware (docs/roadmap.md). Plaintext
  # and public because this image installs nothing and keeps nothing - do not
  # copy this line into a machine that does.
  users.users.zde.password = "zde";

  # nmtui and bluetuith work off these; the wifi item on the list needs them
  # before anything else on it can be done from a cafe.
  users.users.zde.extraGroups = [
    "networkmanager"
    "video"
  ];

  # The instruments the list asks for. wev is how "is this key arriving as F13
  # or as XF86Tools" gets answered; notify-send is the well-behaved client to
  # compare a real one against. dbus-send comes with the bus itself.
  environment.systemPackages = [
    pkgs.foot
    pkgs.wev
    pkgs.libnotify
  ];

  # A terminal, and a key that opens it. Nothing in the keymap does yet: every
  # launch bind spawns a zde subcommand that lands with the shell (roadmap
  # 0.1), so a session on this image would come up with no way to type into
  # it. Mod+Return is unbound in the keymap and is what every other compositor
  # uses, so it clashes with nothing and needs no explaining.
  #
  # This is the image's own bind and not zde's. It goes through the seam a
  # host has for exactly this - local.kdl, included after the generated binds
  # - and niri accepts the second binds block (checked by hand against
  # niri validate, which is the static half of the include-precedence question
  # in docs/roadmap.md).
  home-manager.users.zde.zde.niri.extraConfig = ''
    binds {
        Mod+Return { spawn "foot"; }
    }
  '';

  # The installer profile autologins `nixos` on a getty, and greetd takes tty1
  # away from getty rather than from all of them. So Ctrl+Alt+F2 is a shell on
  # a machine whose session did not come up, which is where the log for why
  # lives: journalctl -b -u greetd, and --user -M zde@ for zded.
  #
  # Named here because a live image that cannot be debugged from the console
  # is a reboot per question.

  # zde-live.iso rather than nixos-minimal-<version>-x86_64-linux.iso, so a
  # stick says what it is. mkForce: the CD names it after the distro.
  image.baseName = lib.mkForce "zde-live";

  # The image mounts no pool and installs nothing, so the forced import this
  # would do is only a risk. It is also the default from 26.11, and saying so
  # here keeps a warning about data loss out of every build of a test image.
  boot.zfs.forceImportRoot = false;
}
