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
  # The niri config.kdl (base + generated binds) and the cheatsheet, built from
  # the keymap source of truth. Same derivation the flake exposes.
  zdeConfig = pkgs.callPackage ./zde-config.nix { };
in
{
  options.zde.enable = lib.mkEnableOption "the zde user environment";

  config = lib.mkIf cfg.enable {
    # The generated niri config. Regenerated on every switch, so keymap.yaml is
    # the only place binds are edited (the file itself says "Do not edit").
    xdg.configFile."niri/config.kdl".source = "${zdeConfig}/niri-config.kdl";
    # The cheatsheet the help widget (system.help) shows.
    xdg.configFile."zde/keymap-cheatsheet.md".source = "${zdeConfig}/keymap-cheatsheet.md";

    # Grows with roadmap 0.1: the zded service, the shell, and zcr/zcc/zlt on
    # PATH. niri itself arrives with its pinned flake input (delivery.md).
    home.packages = [ ];
  };
}
