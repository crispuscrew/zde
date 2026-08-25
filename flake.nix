{
  description = "zde - Zinc Desktop Environment, niri variant";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    home-manager = {
      url = "github:nix-community/home-manager/release-26.05";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # Layer 2 (docs/delivery.md): the sandbox every app is meant to run in.
    # Pinned to a tag rather than a branch, which is what zde asks of anybody
    # pinning zde - an update should be a decision, and a tag is a thing to
    # decide about.
    #
    # Following our nixpkgs so a machine has one, not two. zinc's own lock
    # pins a different one, and two nixpkgs in a closure is a second copy of
    # everything for no benefit here.
    zinc = {
      url = "github:crispuscrew/zinc/v0.10.1";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # niri comes from nixpkgs stable, which carries the release zde wants
    # (26.04) and builds it against the same mesa the system runs - the skew
    # upstream names as the usual cause of a black screen from a TTY. The lock
    # is the pin: niri moves when nixpkgs does, by hand (docs/update.md). It
    # goes back to being its own input the day zde needs a niri that stable
    # does not carry.
    #
    # Quickshell was expected to need exactly that when the shell landed, and
    # it did not: stable carries 0.3.0, so the bar builds from the same
    # nixpkgs as everything else and there is still nothing here but these
    # two.
  };

  outputs =
    {
      self,
      nixpkgs,
      home-manager,
      zinc,
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
      zdeShell = pkgs: pkgs.callPackage ./nix/shell.nix { };
      zdeReplay = pkgs: pkgs.callPackage ./nix/replay.nix { };

      # The live image (nix/live.nix): both layers, the same modules a real
      # machine imports. Built once per system and shared by the two outputs
      # that want it, because evaluating a NixOS host with an ISO on top is not
      # cheap enough to do twice for the same answer.
      live = nixpkgs.lib.genAttrs systems (
        system:
        nixpkgs.lib.nixosSystem {
          inherit system;
          # Layer 2's tools reach the image the way they reach a real machine:
          # zinc's own home-manager module, imported by nix/live.nix. Passed in
          # rather than fetched there, because that file is a path module and a
          # path module cannot see this flake's inputs.
          specialArgs = {
            zincModule = zinc.homeModules.zinc;
          };
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
        zde-shell = zdeShell pkgs;
        default = zdeTools pkgs;

        # The QEMU smoke test (nix build .#zde-smoke). A package and not a
        # check, so that booting a VM stays off pull requests that cannot
        # affect one; it runs in its own workflow, which needs KVM.
        zde-smoke = import ./nix/tests/smoke.nix {
          inherit pkgs;
          zdeModule = ./nix/system.nix;
          homeManagerModule = home-manager.nixosModules.home-manager;
          zdeConfig = zdeConfig pkgs;
          # Layer 2's tools, so the test machine has the sandbox a zde machine
          # is supposed to run its apps in - and so the contract zde leans on
          # (`zcr where`, the state layout) is checked against the real binary
          # rather than against a paragraph.
          zincModule = zinc.homeModules.zinc;
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
          # The bar's QML, checked by qmllint with the imports resolved. It is
          # a check and not only a package because it is cheap and because
          # quickshell has none of its own (nix/shell.nix).
          zde-shell = zdeShell pkgs;
          # The pinned recorder backport, built rather than only evaluated: its
          # IPC binary is what turns a replay signal into a confirmed file.
          zde-replay = zdeReplay pkgs;
          # Build the configured override so its patches and upstream checks run.
          zde-bluez = testHost.config.hardware.bluetooth.package;
          # Compile the exact patched derivation configured by the home module.
          zde-hypridle = pkgs.callPackage ./nix/hypridle.nix { };
          # Evaluation only: discarding the string context keeps the system
          # closure out of the build, so this costs an eval and nothing else.
          # It is what catches a bad option or a typo in nix/system.nix and
          # nix/home.nix, neither of which any other check touches. What it
          # cannot catch is a machine that fails to come up - that is
          # packages.zde-smoke.
          zde-nixos-eval = pkgs.runCommand "zde-nixos-eval" { } ''
            echo ${builtins.unsafeDiscardStringContext testHost.config.system.build.toplevel.drvPath} > "$out"
          '';
          # The host's own local.kdl, through niri's parser
          # (nix/niri-local.nix). Named here because zde-nixos-eval above
          # discards its string context on purpose and so builds nothing: this
          # is one derivation, and it is the one that would let a machine boot
          # into a compositor running its own defaults. What it parses is
          # whatever nix/test-host.nix wrote, which is the block the template
          # ships and the xkb pair the runbook hands out.
          zde-niri-local = testHost.config.home-manager.users.zde.xdg.configFile."niri/local.kdl".source;
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
            # dbus-daemon, which internal/link's tests start one of: the
            # NetworkManager conversation is faked on a private bus rather than
            # at the Go boundary, because the defects it was written for live in
            # the order of the calls and in what the other end says between them
            # (internal/link/fakebus_test.go). Without this on PATH those tests
            # skip, and the skip says so.
            pkgs.dbus
            # niri, for the same reason: internal/zded hands the window rules it
            # writes to `niri validate`, because the shape of a KDL string is
            # not a thing to assert from memory - the escaping bug this was
            # written for looked correct to everyone who read it and was refused
            # by the parser. Without this the test skips and CI proves nothing.
            # It costs the download and no build: nix/zde-config.nix already
            # runs niri's parser over the generated binds, so this closure is
            # one `nix flake check` builds anyway.
            pkgs.niri
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
