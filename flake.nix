{
  description = "zde - Zinc Desktop Environment, niri variant";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    home-manager = {
      url = "github:nix-community/home-manager/release-26.05";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # niri comes from nixpkgs stable, which carries the release zde wants
    # (26.04) and builds it against the same mesa the system runs - the skew
    # upstream names as the usual cause of a black screen from a TTY. The lock
    # is the pin: niri moves when nixpkgs does, by hand (docs/update.md). It
    # goes back to being its own input the day zde needs a niri that stable
    # does not carry. Quickshell will need exactly that when the shell lands.
  };

  outputs =
    {
      nixpkgs,
      home-manager,
      ...
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
      # Layer 0 (docs/delivery.md). A plain path module: it needs nothing from
      # this flake, which is what keeps it importable twice without conflict
      # and usable from a config that never heard of zde's flake.
      nixosModules.zde = ./nix/system.nix;

      # Layer 1: the user environment. One module, shared by the NixOS
      # reference and the portable path (docs/delivery.md).
      homeModules.zde = ./nix/home.nix;

      packages = forAll (pkgs: {
        zde-keymap = keymap pkgs;
        zde-config = zdeConfig pkgs;
        default = keymap pkgs;

        # The QEMU smoke test (nix build .#zde-smoke). A package and not a
        # check, so that booting a VM stays off pull requests that cannot
        # affect one; it runs in its own workflow, which needs KVM.
        zde-smoke = import ./nix/tests/smoke.nix {
          inherit pkgs;
          zdeModule = ./nix/system.nix;
          homeManagerModule = home-manager.nixosModules.home-manager;
          zdeConfig = zdeConfig pkgs;
        };
      });

      # Built by nix flake check in CI: compiles the tools, assembles the niri
      # config (catches KDL assembly errors), and evaluates both layers on a
      # throwaway host. The go tests are their own CI step - this build only
      # covers cmd/zde-keymap, which has none.
      checks = forAll (
        pkgs:
        let
          testHost = nixpkgs.lib.nixosSystem {
            inherit (pkgs.stdenv.hostPlatform) system;
            modules = [
              ./nix/system.nix
              home-manager.nixosModules.home-manager
              ./nix/test-host.nix
            ];
          };
        in
        {
          zde-keymap = keymap pkgs;
          zde-config = zdeConfig pkgs;
          # Evaluation only: discarding the string context keeps the system
          # closure out of the build, so this costs an eval and nothing else.
          # It is what catches a bad option or a typo in nix/system.nix and
          # nix/home.nix, neither of which any other check touches. What it
          # cannot catch is a machine that fails to come up - that is
          # packages.zde-smoke.
          zde-nixos-eval = pkgs.runCommand "zde-nixos-eval" { } ''
            echo ${builtins.unsafeDiscardStringContext testHost.config.system.build.toplevel.drvPath} > "$out"
          '';
        }
      );

      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.nixfmt
            pkgs.statix
          ];
        };
      });

      formatter = forAll (pkgs: pkgs.nixfmt);

      # Added once 0.1 is usable (docs/delivery.md, sequencing):
      # nixosConfigurations.zde, the ISO output.
    };
}
