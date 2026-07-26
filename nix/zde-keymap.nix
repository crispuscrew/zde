# The keymap generator as its own package, so the flake output and the home
# module (which generates the niri config) build the exact same binary.
# Deps are vendored (vendorHash null), zinc-style: the build fetches nothing.
{ lib, buildGoModule }:
buildGoModule {
  pname = "zde-keymap";
  version = "0.1.0";
  # Only the Go tree. With the whole repo as src, editing a doc rebuilt the
  # binary, which rebuilt zde-config, which rewrote the user's config.kdl and
  # the system closure behind it.
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../vendor
      ../cmd/zde-keymap
      ../internal
    ];
  };
  vendorHash = null;
  subPackages = [ "cmd/zde-keymap" ];
  meta = {
    description = "Generate niri binds and a cheatsheet from the zde keymap";
    mainProgram = "zde-keymap";
  };
}
