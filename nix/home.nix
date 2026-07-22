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
in
{
  options.zde.enable = lib.mkEnableOption "the zde user environment";

  config = lib.mkIf cfg.enable {
    # Grows with roadmap 0.1: the generated niri config (keymap scheme),
    # the zded service, the shell, and zcr/zcc/zlt on PATH.
    home.packages = [ ];
  };
}
