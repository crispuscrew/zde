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
  zincModule,
  ...
}:
{
  imports = [
    "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix"
    ./zde-user.nix
  ];

  # One block rather than a zde.* line per concern, which is what statix asks
  # for once there are three of them.
  zde = {
    enable = true;

    # The state snapshot, on, because this image is the by-hand list and the
    # list asks for it (docs/verify.md, section 1). It is also the honest place
    # to find out whether the mechanism works before anybody relies on it for a
    # machine that will not boot.
    #
    # What it cannot do here is outlive the boot: this image's root is a tmpfs,
    # so /var/log/zde goes with the power. That is the difference between a
    # stick and an install and it is the whole reason the file exists on the
    # second one - so read it on this image, and turn it on in your own flake
    # before you install.
    debug.enable = true;

    # Exercise every hardware-facing branch on the by-hand image. Networking
    # and brightness are useful on desktops too and default on; bluetooth stays
    # an explicit opt-in because an unrequested radio is needless exposure.
    networking.enable = true;
    brightness.enable = true;
    laptop.enable = true;
    bluetooth.enable = true;
  };

  # A password, and the greeter left in the way on purpose. Autologin would be
  # friendlier and would skip the one part of the boot path nothing has ever
  # run: greetd reaching niri-session on hardware (docs/roadmap.md). Plaintext
  # and public because this image installs nothing and keeps nothing - do not
  # copy this line into a machine that does.
  users.users.zde.password = "zde";

  # And with that password, no ssh. The installer profile turns sshd on and
  # opens port 22, reasoning in its own comment that the accounts it makes have
  # empty passwords, which sshd refuses. This image breaks that assumption: zde
  # has a real password, written down in a public document. Left alone, joining
  # a cafe network to do the wifi items would hand the session user to everyone
  # else on it.
  #
  # That is the remote half and it is the only half. The local one is left
  # exactly as the installer CD has it, which is worth saying out loud because a
  # line turning sshd off reads like a hardened image and this is not one:
  # greetd holds VT1, and on VT2 upward the CD's own `nixos` account is
  # autologged in by getty, with an empty password, in `wheel`, and
  # `security.sudo.wheelNeedsPassword` is false. Anyone at this keyboard is one
  # Ctrl+Alt+F2 from root and is asked for nothing on the way.
  #
  # Left that way because it is what the by-hand list needs: docs/verify.md
  # sends you to a text console to read journals and restart units while the
  # session is the thing under test, and a stick that touches no disk, keeps
  # nothing and is meant to be pulled out has nothing on it worth that lock. The
  # network is different because it is the one way in that does not need the
  # keyboard. A machine you install gets the other answer, in templates/host -
  # do not carry any of this into one.
  services.openssh.enable = lib.mkForce false;

  # nmtui works off networkmanager; `video` is what brightnessctl's udev rules
  # hand the backlight to (nix/system.nix), and the brightness keys are on the
  # by-hand list. It was dropped from here once as buying nothing, which was
  # true for exactly as long as those rules were not installed.
  #
  # No `wheel`, deliberately, and this is the line somebody will want to change:
  # the install runbook is all root work, and from this session `sudo -i`
  # answers "zde is not in the sudoers file". Adding it would take the password
  # three lines above - plaintext, public, and typed by anyone who picks up the
  # stick - and make it root. The reasoning under openssh.enable is the same
  # reasoning: this image hands a real password to a user on purpose, so what
  # that user can reach has to stay worth handing over. Today the answer to a
  # cafe network finding it is a session; with wheel it would be the machine.
  #
  # The root shell the runbook needs already exists and costs nothing: the
  # installer profile autologins `nixos` on every console greetd is not holding,
  # in wheel with passwordless sudo, and greetd holds only the first. So the fix
  # for "sudo does not work here" is Ctrl+Alt+F2, and docs/install.md opens by
  # saying so.
  users.users.zde.extraGroups = [
    "networkmanager"
    "video"
  ];

  # The instruments the list asks for. wev is how "is this key arriving as F13
  # or as XF86Tools" gets answered; notify-send is the well-behaved client to
  # compare a real one against. dbus-send comes with the bus itself.
  environment.systemPackages = [
    pkgs.kitty
    pkgs.wev
    pkgs.libnotify
  ];

  # Flakes, because this image is what somebody installs zde from and zde is
  # delivered as one. Without them the first command of the install runbook -
  # nix flake init -t github:crispuscrew/zde#host - fails on an experimental
  # feature, which is a poor way to meet a new system.
  nix.settings.experimental-features = [
    "nix-command"
    "flakes"
  ];

  # The minimal CD turns fontconfig off, which leaves the fonts it installs
  # unreachable: with no /etc/fonts every client falls back to the one face
  # compiled into fontconfig itself, and that face is proportional. The first
  # thing anyone does on this image is open a terminal, so a monospace font
  # that resolves is not a nicety here.
  fonts.fontconfig.enable = lib.mkForce true;

  # Mod+t opens the generated terminal default. The live image keeps the
  # familiar Mod+Return as a second route and as a check of the host-config seam.
  #
  # This is the image's own bind and not zde's. It goes through the seam a
  # host has for exactly this: local.kdl, included after the generated binds.
  # A later binds block does not replace the earlier ones - niri keeps them and
  # swaps only the keys the new block names (niri-config, the "binds" arm),
  # which is the same behaviour zded's dynamic.kdl will lean on.
  # Both layers, which is what this image is for. zcr and zc come from zinc's
  # own flake (docs/delivery.md, layer 2): without them the stick boots a
  # desktop whose whole premise is sandboxed apps and has nothing to run one
  # with, and the by-hand list (docs/verify.md) cannot ask about layer 2 at all.
  # The runtime they need is layer 0's - rootless podman is on already.
  home-manager.users.zde = {
    imports = [ zincModule ];
    # The other half of zde.debug above: layer 0 makes the directory and this
    # is what writes into it.
    zde.debug.enable = true;
    programs.zinc.enable = true;
    # zlg is opt-in in zinc's module, on the reasoning that a desktop shipping
    # its own launcher does not want a second one. zde ships none: its
    # generated keymap binds Mod+g to `zlg` already (common/keymap), so
    # leaving it out is not declining a second launcher, it is a bound key
    # that spawns nothing.
    programs.zinc.tools = [
      "zc"
      "zcr"
      "zlt"
      "zlg"
    ];

    zde.niri.extraConfig = ''
      binds {
          Mod+Return { spawn "kitty"; }
      }
    '';
  };

  # zde-live.iso rather than nixos-minimal-<version>-x86_64-linux.iso, so a
  # stick says what it is. mkForce: the CD names it after the distro.
  image.baseName = lib.mkForce "zde-live";

  # The image mounts no pool and installs nothing, so the forced import this
  # would do is only a risk. It is also the default from 26.11, and saying so
  # here keeps a warning about data loss out of every build of a test image.
  boot.zfs.forceImportRoot = false;
}
