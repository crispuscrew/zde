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
      # zde is the binaries (zded, zde, zde-keymap); zde-config is the niri
      # config and cheatsheet built from the keymap. Both are shared with the
      # home module (nix/*.nix) so the flake and layer 1 build the same thing.
      zdeTools = pkgs: pkgs.callPackage ./nix/zde.nix { };
      zdeConfig = pkgs: pkgs.callPackage ./nix/zde-config.nix { };

      # The live image (nix/live.nix), as one host the flake then offers three
      # ways: a nixosConfiguration to build or inspect, the ISO as a package,
      # and an eval-only check. Both layers, the same modules a real machine
      # imports.
      liveFor =
        system:
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            ./nix/system.nix
            home-manager.nixosModules.home-manager
            ./nix/live.nix
          ];
        };
    in
    {
      # Layer 0 (docs/delivery.md). A plain path module: it needs nothing from
      # this flake, which is what keeps it importable twice without conflict
      # and usable from a config that never heard of zde's flake.
      nixosModules.zde = ./nix/system.nix;

      # Layer 1: the user environment. One module, shared by the NixOS
      # reference and the portable path (docs/delivery.md).
      homeModules.zde = ./nix/home.nix;

      # The live image as a host, so it can be inspected, overridden, or
      # rebuilt from by a machine that wants zde without an install script yet
      # (docs/delivery.md, sequencing).
      nixosConfigurations.zde-live = liveFor "x86_64-linux";

      packages = forAll (pkgs: {
        zde = zdeTools pkgs;
        zde-config = zdeConfig pkgs;
        default = zdeTools pkgs;

        # The QEMU smoke test (nix build .#zde-smoke). A package and not a
        # check, so that booting a VM stays off pull requests that cannot
        # affect one; it runs in its own workflow, which needs KVM.
        zde-smoke = import ./nix/tests/smoke.nix {
          inherit pkgs;
          zdeModule = ./nix/system.nix;
          homeManagerModule = home-manager.nixosModules.home-manager;
          zdeConfig = zdeConfig pkgs;
        };

        # The USB stick (nix build .#zde-iso). A package for the same reason
        # the smoke test is one - it is gigabytes and an hour, and no pull
        # request should wait for it. What CI does check is that it still
        # evaluates (checks.zde-iso-eval).
        zde-iso = (liveFor pkgs.stdenv.hostPlatform.system).config.system.build.isoImage;
      });

      # Built by nix flake check in CI: compiles the tools, assembles the niri
      # config (catches KDL assembly errors), and evaluates both layers on a
      # throwaway host. The go tests are their own CI step: this build compiles
      # the binaries but only runs the tests that sit beside them.
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
          zde = zdeTools pkgs;
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

          # The same trick for the live image, and pointed at the ISO rather
          # than at the system: nix flake check evaluates every
          # nixosConfiguration's toplevel on its own, which is the half that
          # would still pass with isoImage.* broken. This is the half that
          # would send someone to burn a stick and find out.
          zde-iso-eval = pkgs.runCommand "zde-iso-eval" { } ''
            echo ${builtins.unsafeDiscardStringContext (liveFor pkgs.stdenv.hostPlatform.system).config.system.build.isoImage.drvPath} > "$out"
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
    };
}
