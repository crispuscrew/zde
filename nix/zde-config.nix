# Build the zde runtime config from the keymap source of truth: the niri
# config.kdl (base + generated binds) and the keymap cheatsheet. Shared by the
# flake package and the home module, so both produce byte-identical output.
{
  runCommand,
  callPackage,
  niri,
}:
let
  zde-keymap = callPackage ./zde-keymap.nix { };
in
runCommand "zde-config" { nativeBuildInputs = [ niri ]; } ''
  mkdir -p "$out"
  ${zde-keymap}/bin/zde-keymap \
    -in ${../common/keymap/keymap.yaml} \
    -kdl binds.kdl \
    -cheatsheet "$out/keymap-cheatsheet.md"
  cat ${../niri/config.base.kdl} binds.kdl > "$out/niri-config.kdl"

  # niri's own parser, on the assembled file. The generator can only enforce
  # the shape of a key name; this is what knows that "Mod+banana" is not one,
  # that every emitted action resolves, and that concatenating the base with
  # the binds produced one valid document. Without it the first thing to find
  # out is the compositor, at a login, with nothing on screen.
  niri validate -c "$out/niri-config.kdl"
''
