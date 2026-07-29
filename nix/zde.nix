# The zde binaries: zded (the daemon), zde (the command line into it) and
# zde-keymap (the generator the config build runs). One derivation, so the
# flake output and the home module cannot drift onto different builds.
# Deps are vendored (vendorHash null), zinc-style: the build fetches nothing.
{ lib, buildGoModule }:
let
  # One place. It used to be here and in cmd/zded/main.go, kept in step by
  # hand and by nothing else, so a running daemon could report a version its
  # own store path disagreed with.
  version = "0.1.0";
in
buildGoModule {
  pname = "zde";
  inherit version;
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
  ];
  # Only the Go tree. With the whole repo as src, editing a doc rebuilt the
  # binary, which rebuilt zde-config, which rewrote the user's config.kdl and
  # the system closure behind it.
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../vendor
      ../cmd
      ../internal
    ];
  };
  vendorHash = null;
  subPackages = [
    "cmd/zde"
    "cmd/zded"
    "cmd/zde-keymap"
  ];
  meta = {
    description = "The zde daemon, its command line, and the keymap generator";
    mainProgram = "zde";
  };
}
