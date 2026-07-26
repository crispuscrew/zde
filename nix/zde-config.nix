# Build the zde runtime config from the keymap source of truth: the niri
# config.kdl, the generated binds beside it, and the keymap cheatsheet. Shared
# by the flake package and the home module, so both produce byte-identical
# output.
{
  runCommand,
  callPackage,
  niri,
}:
let
  zde = callPackage ./zde.nix { };
in
runCommand "zde-config" { nativeBuildInputs = [ niri ]; } ''
  mkdir -p "$out"
  cp ${../niri/config.kdl} "$out/niri-config.kdl"
  ${zde}/bin/zde-keymap \
    -in ${../common/keymap/keymap.yaml} \
    -kdl "$out/binds.kdl" \
    -cheatsheet "$out/keymap-cheatsheet.md"

  # niri's own parser, on the tree as a machine will actually see it: the
  # config symlinked into place with its includes beside it, local and dynamic
  # empty the way they are on a first boot. The generator can only enforce the
  # shape of a key name; this is what knows that "Mod+banana" is not one, that
  # every emitted action resolves, and that the includes resolve at all.
  # Without it the first thing to find out is the compositor, at a login.
  mkdir -p check
  ln -s "$out/niri-config.kdl" check/config.kdl
  ln -s "$out/binds.kdl" check/binds.kdl
  : > check/local.kdl
  : > check/dynamic.kdl
  niri validate -c check/config.kdl
''
