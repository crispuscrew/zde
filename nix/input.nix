# The input layer's config, in one place.
#
# The NixOS module installs it and `nix flake check` checks it, and a check
# that wrapped it in a different defcfg would be checking a config nobody runs.
# So the parts around the file live here, next to the file, rather than in
# whichever of the two happened to need them first.
{
  # Without this, keys the layout does not mention bypass it and are emitted at
  # once, while a key still deciding tap or hold is held back - so Tab then a
  # letter, typed quickly, arrives the wrong way round. kanata warns about
  # leaving it unset.
  extraDefCfg = "process-unmapped-keys yes";
  config = builtins.readFile ../input/kanata.kbd;
}
