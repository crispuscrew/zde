# The keymap generator as its own package, so the flake output and the home
# module (which generates the niri config) build the exact same binary.
# Deps are vendored (vendorHash null), zinc-style: the build fetches nothing.
{ buildGoModule }:
buildGoModule {
  pname = "zde-keymap";
  version = "0.1.0";
  src = ../.;
  vendorHash = null;
  subPackages = [ "cmd/zde-keymap" ];
  meta = {
    description = "Generate niri binds and a cheatsheet from the zde keymap";
    mainProgram = "zde-keymap";
  };
}
