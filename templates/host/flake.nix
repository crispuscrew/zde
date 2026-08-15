# A machine running zde. Start here, in a new empty directory - `nix flake
# init` refuses to overwrite, so run in /etc/nixos it half-applies and leaves
# a flake pointing at somebody else's configuration.nix:
#
#   mkdir ~/zdebox && cd ~/zdebox
#   nix flake init -t github:crispuscrew/zde#host
#   sudo nixos-generate-config --show-hardware-config > hardware-configuration.nix
#   $EDITOR flake.nix configuration.nix      # the user, the hostname
#   sudo nixos-rebuild switch --flake .#zdebox
#
# zde is a module, not a system: this flake is yours, and it is where your
# disks, your hostname and your user live. What zde pins is what zde is tested
# against - the nixpkgs below - and the whole point of this template is that
# the pin travels with you. A module is evaluated by whatever nixpkgs imports
# it, so zde's own lock has no say in what runs here.
#
# Updating is two steps, and the order matters (docs/update.md in the zde
# repo): move the pin, then rebuild.
#
#   cd /etc/nixos
#   sudo nix flake update zde
#   sudo nixos-rebuild switch --flake .#zdebox
#
# While zde is pinned to a branch - it has no tags yet, see the zde input
# below - that first command takes whatever is on dev at that moment. It is
# still a decision you make and a generation you can roll back, but it is not
# a release.
{
  description = "A machine running zde";

  inputs = {
    # The release zde is tested against. Moving this on your own is allowed
    # and unsupported: zde follows nixpkgs stable, and the two are meant to
    # move together.
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

    home-manager = {
      url = "github:nix-community/home-manager/release-26.05";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # follows on both, so this machine resolves one nixpkgs flake and one
    # home-manager. Without it you get zde's copies as well: a second closure
    # to download, and two versions of home-manager deciding what your home
    # looks like. (One *flake*. The package set layer 1 sees is a separate
    # question, and it is the useGlobalPkgs line further down that answers it.)
    #
    # `/dev` is a branch, and it is named here rather than left implicit
    # because a bare `github:crispuscrew/zde` means "the default branch,
    # whatever that is today" - which is a decision zde would be making for
    # your machine without telling you.
    #
    # What actually pins this machine is your own flake.lock, written the first
    # time you build and not touched again until you say so: zde cannot move
    # under you. But `nix flake update zde` on a branch is not "take the next
    # release", it is "take everything that landed on dev since", which is not
    # a thing anybody decided. zde has no tags at all yet, so a branch is the
    # only honest thing this line can say today.
    #
    # The day zde cuts its first tag, this becomes the same shape as zinc
    # below:
    #
    #   url = "github:crispuscrew/zde/v0.1.0";
    #
    # and `nix flake update zde` re-resolves a tag to the same commit, so
    # moving becomes editing this line - which is what zde already asks of
    # anybody pinning zinc, and what docs/update.md says zde asks of anybody
    # pinning zde. Until then, read the commits between your lock and dev
    # before you update.
    zde = {
      url = "github:crispuscrew/zde/dev";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.home-manager.follows = "home-manager";
    };

    # Layer 2: the sandbox apps run in. Its own flake, pinned to its own tag,
    # because it releases on its own schedule and an update to one should not
    # be an update to the other.
    zinc = {
      url = "github:crispuscrew/zinc/v0.9.1";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      home-manager,
      zde,
      zinc,
      ...
    }:
    let
      # Yours. Both have a twin in configuration.nix that nothing checks:
      # `user` must match users.users.<name> there, and `host` must match
      # networking.hostName.
      user = "you";
      host = "zdebox";
    in
    {
      nixosConfigurations.${host} = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        modules = [
          ./hardware-configuration.nix
          ./configuration.nix

          zde.nixosModules.zde # layer 0: greetd, niri, podman, PipeWire
          home-manager.nixosModules.home-manager

          {
            # What generation is this, in terms of the flake that made it.
            # `nixos-version --configuration-revision` reads it back, which is
            # the only honest answer to "which zde am I running".
            system.configurationRevision = self.rev or self.dirtyRev or "dirty";

            zde.enable = true;
            # zde.laptop.enable = true;   # battery, radios, the lid switch

            # The state snapshot, for the failure with no terminal in it: the
            # session does not come up, the screen is black, and the only way
            # left to ask this machine anything is to take the disk out. With
            # both halves on, every session start writes what the machine
            # looked like into /var/log/zde/${user} - the graphics device and
            # whether niri reached a renderer on it, the versions, the
            # hardware, and the whole of `zde doctor`. Eight files, 0600, and
            # nothing private in them (docs/verify.md, when the screen is
            # black). Off by default because it is a file on your disk.
            # zde.debug.enable = true;                      # layer 0: the directory

            home-manager = {
              # Layer 1 gets this machine's package set, not a second one built
              # from the same flake input. Without this, home-manager
              # instantiates nixpkgs again from scratch, and an overlay, a patch
              # or `nixpkgs.config.allowUnfree` set in configuration.nix reaches
              # the system and silently stops at layer 1's front door: foot,
              # swaylock, quickshell, brightnessctl, less, wl-clipboard and
              # wireplumber all come out of the second evaluation, which never
              # saw any of it. Same flake input, two package sets, and only one
              # of them takes your instructions. It is a quiet failure - the
              # build succeeds and you get a foot nobody patched.
              #
              # What it costs: the `nixpkgs.*` options *inside* a home-manager
              # config stop existing, so a per-user overlay is no longer a thing
              # this machine can have. On a single-user desktop that is the
              # right way round - an overlay belongs to the machine - and
              # neither zde's home module nor zinc's has ever set one. zinc is
              # unaffected either way: `programs.zinc.packages` defaults to
              # zinc's own flake outputs, so its tools are built by zinc's
              # nixpkgs and your overlays do not reach them under any setting.
              useGlobalPkgs = true;

              # And layer 1's programs are installed as part of this generation,
              # in /etc/profiles/per-user, rather than into the mutable
              # ~/.nix-profile. `nixos-rebuild switch --rollback` then takes them
              # back with everything else, which is the rollback story zde sells
              # NixOS on - and it leaves ~/.nix-profile free for whatever you
              # install by hand, instead of home-manager and `nix profile`
              # fighting over the same symlink. Both directories are on the login
              # PATH (`environment.profiles`), so nothing moves out of reach.
              useUserPackages = true;

              # Layer 1: the niri config, the generated binds, zded and its unit.
              # Layer 2 arrives here too: zinc's tools on PATH, which is what a
              # `zde.apps` entry naming `zcr` needs to exist.
              users.${user} = {
                imports = [
                  zde.homeModules.zde
                  zinc.homeModules.zinc
                ];
                zde.enable = true;
                # zde.debug.enable = true;                  # layer 1: what writes it
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

                # Host-specific niri: outputs, scale, an xkb layout. This is the
                # seam for everything zde does not fix.
                # zde.niri.extraConfig = ''
                #   output "eDP-1" { scale 2 }
                # '';

                # Set once, at creation, and then never touched (docs/update.md).
                home.stateVersion = "26.05";
              };
            };
          }
        ];
      };
    };
}
