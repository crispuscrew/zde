# A machine running zde. Start here:
#
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
#   nix flake update zde
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
  };

  outputs =
    {
      self,
      nixpkgs,
      home-manager,
      zde,
      ...
    }:
    let
      # Yours. The name has to match the user in configuration.nix.
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
            home-manager.users.${user} = {
              imports = [ zde.homeModules.zde ];
              zde.enable = true;

              # Host-specific niri: outputs, scale, an xkb layout. This is the
              # seam for everything zde does not fix.
              # zde.niri.extraConfig = ''
              #   output "eDP-1" { scale 2 }
              # '';

              # Set once, at creation, and then never touched (docs/update.md).
              home.stateVersion = "26.05";
            };
          }
        ];
      };
    };
}
