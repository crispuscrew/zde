{
  lib,
  stdenv,
  fetchgit,
  libjpeg_turbo,
  gpu-screen-recorder,
}:
# 6.0.1 adds instance-scoped IPC: save-replay answers only after the file is
# closed and names it. The pinned nixpkgs has signal-only 5.13.8, so keep the
# update on that package's dependency set instead of adding a second nixpkgs.
gpu-screen-recorder.overrideAttrs (old: {
  version = "6.0.1";
  src = fetchgit {
    url = "https://repo.dec05eba.com/gpu-screen-recorder";
    tag = "6.0.1";
    hash = "sha256-mq+I90JaVsYZgPFLHRO/Qebv5p3XQZ6VaNbvHJBfXbQ=";
  };
  postPatch = (old.postPatch or "") + ''
    substituteInPlace src/capture/v4l2.c src/image_writer.c \
      --replace-fail "libturbojpeg.so.0" "${lib.getLib libjpeg_turbo}/lib/libturbojpeg${stdenv.hostPlatform.extensions.sharedLibrary}"
  '';
  mesonFlags = (old.mesonFlags or [ ]) ++ [
    (lib.mesonBool "ffmpeg_static" false)
  ];
  postInstall = (old.postInstall or "") + ''
    substituteInPlace $out/lib/systemd/user/gpu-screen-recorder.service \
      --replace-fail "ExecStart=gpu-screen-recorder" "ExecStart=$out/bin/gpu-screen-recorder"
  '';
})
