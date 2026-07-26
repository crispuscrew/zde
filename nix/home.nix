# zde user layer (docs/delivery.md, layer 1). The single home-manager module
# shared by the NixOS reference and the portable path - the "which distro"
# question never reaches this file.
{
  lib,
  config,
  pkgs,
  ...
}:
let
  cfg = config.zde;
  # The niri config, the generated binds and the cheatsheet, built from the
  # keymap source of truth. Same derivation the flake exposes.
  zdeConfig = pkgs.callPackage ./zde-config.nix { };

  # What dynamic.kdl says before zded has written anything into it. niri treats
  # a missing include as a fatal error, so this file has to exist from the
  # first boot, and it cannot be a store symlink because zded writes it.
  dynamicSeed = pkgs.writeText "zde-niri-dynamic.kdl" ''
    // Written by zded while the session runs: capture-block window rules, the
    // binds a desk releases. Empty until then.
    //
    // home-manager creates this file once and never touches it again - it is
    // the one part of the niri config that is not generated, because it has to
    // change without a rebuild. Anything you want to keep belongs in
    // zde.niri.extraConfig, which lands in local.kdl beside this.
  '';
in
{
  options.zde = {
    enable = lib.mkEnableOption "the zde user environment";

    niri.extraConfig = lib.mkOption {
      type = lib.types.lines;
      default = "";
      example = ''
        output "eDP-1" {
            scale 2
        }
      '';
      description = ''
        Host-specific niri config, written to ~/.config/niri/local.kdl and
        included after the generated binds. This is where outputs, an xkb
        layout, or a window rule go: everything zde does not fix, without
        forking the config it does.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # The niri config, and the binds it includes. Regenerated on every switch,
    # so keymap.yaml is the only place binds are edited (the file itself says
    # "Do not edit"). Forced, because niri writes a default config.kdl itself
    # the first time it starts without one, and an unmanaged file in that spot
    # stops every later home-manager generation.
    xdg.configFile = {
      "niri/config.kdl" = {
        source = "${zdeConfig}/niri-config.kdl";
        force = true;
      };
      "niri/binds.kdl" = {
        source = "${zdeConfig}/binds.kdl";
        force = true;
      };
      # The host's own half, still declarative.
      "niri/local.kdl" = {
        text = ''
          // Generated from zde.niri.extraConfig. Edit that, not this.
          ${cfg.niri.extraConfig}
        '';
        force = true;
      };
      # The cheatsheet the help widget (system.help) shows.
      "zde/keymap-cheatsheet.md".source = "${zdeConfig}/keymap-cheatsheet.md";
    };

    # dynamic.kdl is the seam zded writes through, so home-manager seeds it and
    # then keeps its hands off. Created rather than linked: a store symlink is
    # read-only, and a missing include is fatal to the whole config.
    home.activation.zdeNiriDynamic = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      dynamic="${config.xdg.configHome}/niri/dynamic.kdl"
      if [ ! -e "$dynamic" ]; then
        run mkdir -p "$(dirname "$dynamic")"
        run install -m 0644 ${dynamicSeed} "$dynamic"
      fi
    '';

    # What the generated binds actually shell out to. The keymap is the source
    # of truth for the chords, which makes it a source of truth for the
    # binaries too: a bind whose command is not installed is a key that does
    # nothing, silently. The smoke test checks the two against each other.
    #
    # Grows with roadmap 0.1: the zded service, the shell, and zcr/zcc/zlt.
    home.packages = [
      pkgs.brightnessctl # system.brightness-up/dn
      pkgs.wireplumber # wpctl, for audio.*
    ];
  };
}
