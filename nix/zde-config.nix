# Build the zde runtime config from the keymap source of truth: the niri
# config.kdl (base + generated binds) and the keymap cheatsheet. Shared by the
# flake package and the home module, so both produce byte-identical output.
{
  runCommand,
  callPackage,
}:
let
  zde-keymap = callPackage ./zde-keymap.nix { };
in
runCommand "zde-config" { } ''
  mkdir -p "$out"
  ${zde-keymap}/bin/zde-keymap \
    -in ${../common/keymap/keymap.yaml} \
    -kdl binds.kdl \
    -cheatsheet "$out/keymap-cheatsheet.md"
  cat ${../niri/config.base.kdl} binds.kdl > "$out/niri-config.kdl"
''
