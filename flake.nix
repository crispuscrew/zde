{
  description = "zde - Zinc Desktop Environment, niri variant";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    home-manager = {
      url = "github:nix-community/home-manager/release-25.05";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # Fast-moving pieces get pinned as their own inputs - zinc-style pinning,
    # exact releases, our hand on the bump (delivery.md, channel strategy).
    # Quickshell joins when the shell lands.
    niri = {
      url = "github:niri-wm/niri/v26.04";
      # niri keeps its own nixpkgs on purpose: upstream's flake targets
      # nixpkgs-unstable, and against our 25.05 the install fails - its
      # postInstall asks installShellFiles for --nushell completions, which
      # 25.05's hook does not know, and the build dies with an empty log.
      # Bringing its own dependencies is what pinning it separately is for.
      # It ships no flake.lock, so the nixpkgs it builds against is resolved
      # here and pinned in ours; bumping niri moves both.
      # rust-overlay is upstream's dev-shell toolchain; aiming it at this flake
      # keeps it unfetched, and nothing we evaluate reads it.
      inputs.rust-overlay.follows = "";
    };
  };

  outputs =
    {
      nixpkgs,
      home-manager,
      niri,
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

      # Layer 0 (docs/delivery.md). The pinned niri is handed over here rather
      # than inside the module, so the module stays a plain NixOS module - a
      # consumer who prefers the nixpkgs build only sets zde.niri.package. The
      # key is what keeps this deduplicated when a config imports it twice, say
      # from a shared profile and a host; a bare path module gets that for
      # free, an inline one does not.
      zdeSystem = {
        key = "zde:nixosModules.zde";
        _file = ./flake.nix;
        imports = [
          ./nix/system.nix
          (
            { lib, pkgs, ... }:
            {
              zde.niri.package = lib.mkDefault niri.packages.${pkgs.stdenv.hostPlatform.system}.niri;
            }
          )
        ];
      };
    in
    {
      nixosModules.zde = zdeSystem;

      # Layer 1: the user environment. One module, shared by the NixOS
      # reference and the portable path (docs/delivery.md).
      homeModules.zde = ./nix/home.nix;

      packages = forAll (pkgs: {
        zde-keymap = keymap pkgs;
        zde-config = zdeConfig pkgs;
        default = keymap pkgs;
        # The pinned compositor, re-exported so that bumping the input is one
        # command to verify: nix build .#niri. CI never builds it.
        inherit (niri.packages.${pkgs.stdenv.hostPlatform.system}) niri;
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
              zdeSystem
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
          # nix/home.nix, neither of which any other check touches. A build
          # failure it cannot catch - that is what nix build .#niri is for.
          # Evaluating niri does reach the network for its git dependencies,
          # so this check is not offline-capable.
          zde-nixos-eval = pkgs.runCommand "zde-nixos-eval" { } ''
            echo ${builtins.unsafeDiscardStringContext testHost.config.system.build.toplevel.drvPath} > "$out"
          '';
        }
      );

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
