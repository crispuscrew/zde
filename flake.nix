{
  description = "zde - Zinc Desktop Environment, niri variant";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    home-manager = {
      url = "github:nix-community/home-manager/release-25.05";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # Fast-moving pieces (niri, Quickshell) get pinned as their own inputs
    # during the 0.1 prototype - zinc-style pinning, exact releases, our hand
    # on the bump. Until verified they are not declared here.
  };

  outputs =
    {
      self,
      nixpkgs,
      home-manager,
    }:
    let
      systems = [ "x86_64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      # zde-keymap generates niri binds + cheatsheet; zde-config assembles the
      # full niri config.kdl (base + binds). Both are shared with the home
      # module (nix/*.nix) so the flake and layer 1 build identical output.
      keymap = pkgs: pkgs.callPackage ./nix/zde-keymap.nix { };
      zdeConfig = pkgs: pkgs.callPackage ./nix/zde-config.nix { };
    in
    {
      # Layer 0: the system module (NixOS reference platform).
      nixosModules.zde = ./nix/system.nix;

      # Layer 1: the user environment. One module, shared by the NixOS
      # reference and the portable path (docs/delivery.md).
      homeModules.zde = ./nix/home.nix;

      packages = forAll (pkgs: {
        zde-keymap = keymap pkgs;
        zde-config = zdeConfig pkgs;
        default = keymap pkgs;
      });

      # Built by nix flake check in CI: compiles the tools, runs their go
      # tests, and assembles the niri config (catches KDL assembly errors).
      checks = forAll (pkgs: {
        zde-keymap = keymap pkgs;
        zde-config = zdeConfig pkgs;
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.nixfmt-rfc-style
            pkgs.statix
          ];
        };
      });

      formatter = forAll (pkgs: pkgs.nixfmt-rfc-style);

      # Added once 0.1 is usable (docs/delivery.md, sequencing):
      # nixosConfigurations.zde, the ISO output.
    };
}
