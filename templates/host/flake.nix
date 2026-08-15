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

    # follows on both, so this machine has exactly one nixpkgs and one
    # home-manager. Without it you get zde's copies as well: a second closure
    # to download, and two versions of home-manager deciding what your home
    # looks like.
    zde = {
      url = "github:crispuscrew/zde";
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

            # Layer 1: the niri config, the generated binds, zded and its unit.
            # Layer 2 arrives here too: zinc's tools on PATH, which is what a
            # `zde.apps` entry naming `zcr` needs to exist.
            home-manager.users.${user} = {
              imports = [
                zde.homeModules.zde
                zinc.homeModules.zinc
              ];
              zde.enable = true;
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

              # Host-specific niri: outputs, scale, a window rule. This is the
              # seam for everything zde does not fix. An xkb layout has its own
              # option (zde.niri.xkb, docs/install.md section 2) - this is the
              # rest.
              #
              # What goes in the string is KDL, which is not Nix: a node's
              # children go on their own lines. `output "eDP-1" { scale 2 }`
              # reads like it should work and is not KDL - the parser wants a
              # newline or a `;` before the closing brace - and it is what this
              # template used to ship commented out. A rebuild now refuses it
              # (nix/niri-local.nix runs niri's own parser over this block), so
              # the cost of getting it wrong is a build error and not a session.
              # zde.niri.extraConfig = ''
              #   output "eDP-1" {
              #       scale 2
              #   }
              # '';

              # Set once, at creation, and then never touched (docs/update.md).
              home.stateVersion = "26.05";
            };
          }
        ];
      };
    };
}
