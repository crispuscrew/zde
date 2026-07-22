{
  description = "zde - Zinc Desktop Environment, niri variant";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    home-manager = {
      url = "github:nix-community/home-manager/release-25.05";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # Fast-moving pieces (niri, quickshell) get pinned as their own inputs
    # during the 0.1 prototype - zinc-style pinning, exact releases, our hand
    # on the bump. Until verified they are not declared here.
  };

  outputs =
    { self, nixpkgs, home-manager }:
    let
      systems = [ "x86_64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      # Layer 0: the system module (NixOS reference platform).
      nixosModules.zde = ./nix/system.nix;

      # Layer 1: the user environment. One module, shared by the NixOS
      # reference and the portable path (docs/delivery.md).
      homeModules.zde = ./nix/home.nix;

      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.nixfmt-rfc-style
            pkgs.statix
          ];
        };
      });

      formatter = forAll (pkgs: pkgs.nixfmt-rfc-style);

      # Added once 0.1 is usable (docs/delivery.md, sequencing):
      # nixosConfigurations.zde, the ISO output, checks.
    };
}
