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
      self,
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

      # The live image (nix/live.nix): both layers, the same modules a real
      # machine imports. Built once per system and shared by the two outputs
      # that want it, because evaluating a NixOS host with an ISO on top is not
      # cheap enough to do twice for the same answer.
      live = nixpkgs.lib.genAttrs systems (
        system:
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            ./nix/system.nix
            home-manager.nixosModules.home-manager
            ./nix/live.nix
            # Which commit is on the stick, readable from the booted image
            # with `nixos-version --configuration-revision`. That answer is
            # worth its cost, and the cost is real: the revision is part of
            # the system, so every commit - a doc, a comment - invalidates the
            # ISO and it builds again from scratch. Nothing in CI builds it,
            # so this is paid by whoever is making a stick, and only when they
            # have committed since the last one.
            { system.configurationRevision = self.rev or self.dirtyRev or "dirty"; }
          ];
        }
      );
    in
    {
      # Layer 0 (docs/delivery.md). A plain path module: it needs nothing from
      # this flake, which is what makes it importable from a config that never
      # heard of zde's flake, and what makes importing it twice from the same
      # path harmless. (Twice from two different store paths is not: the
      # module system refuses a second declaration of zde.enable, whatever
      # this file does.)
      nixosModules.zde = ./nix/system.nix;

      # Layer 1: the user environment. One module, shared by the NixOS
      # reference and the portable path (docs/delivery.md).
      homeModules.zde = ./nix/home.nix;

      # The live image as a host, so it can be inspected and overridden. Not a
      # machine to rebuild from, whatever its being a nixosConfiguration
      # suggests: it is the installer CD with a tmpfs root, no disk layout, and
      # a password printed in a public document. What a real machine starts
      # from is templates.host below.
      nixosConfigurations.zde-live = live.x86_64-linux;

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
        # request should wait for it. Being one is also what keeps it checked:
        # nix flake check instantiates every package, so a broken isoImage.*
        # fails CI here without anything being built.
        zde-iso = live.${pkgs.stdenv.hostPlatform.system}.config.system.build.isoImage;
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
          # There is deliberately no check for the live image beside this one.
          # It would be dead weight: nix flake check instantiates
          # packages.zde-iso, which is the whole ISO derivation, so a broken
          # isoImage.* already fails there. Checked by breaking it.
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

      # nix flake init -t github:crispuscrew/zde#host
      #
      # The one output that fixes the pin. zde's modules are evaluated by
      # whichever nixpkgs imports them, so this flake's lock says nothing
      # about what a machine runs - the lock in the user's own flake does, and
      # this is where that lock comes from already pointing at the release zde
      # is tested against (docs/update.md).
      templates.host = {
        path = ./templates/host;
        description = "A machine running zde: both layers, pinned to the tested nixpkgs";
      };
      templates.default = self.templates.host;
    };
}
