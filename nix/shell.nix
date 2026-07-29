# The shell: the bar, as QML for Quickshell (docs/roadmap.md, 0.1).
#
# A derivation and not a file the home module copies, because this is where the
# QML gets checked. quickshell has no --check of its own, and QML is read when
# it is displayed: a typo in a bar is a bar that never appears, at a login,
# with a message in a log nobody is looking at. qmllint resolves the imports
# properly, so it catches an unknown property and not only a missing brace.
{
  lib,
  runCommand,
  quickshell,
  qt6,
}:
runCommand "zde-shell"
  {
    meta = {
      description = "The zde shell: the bar, in QML for Quickshell";
    };
  }
  ''
    # --max-warnings 0, because qmllint exits 0 on a warning by default: a
    # nonexistent property is a warning, and without this the check would pass
    # on exactly the mistakes it exists to catch. Verified by making both
    # kinds and watching them fail.
    #
    # Every file, not shell.qml by name. The day this grows a second one, a
    # list that names one would keep passing and the derivation would ship a
    # shell that cannot load.
    #
    # PanelWindow's uncreatable-type warning is suppressed where it happens,
    # with a comment, rather than by turning the category off here: as a
    # category it would also hide a real attempt to instantiate Quickshell
    # itself or a DataStream, and those do fail at load.
    ${qt6.qtdeclarative}/bin/qmllint \
      -I ${quickshell}/lib/qt-6/qml \
      -I ${qt6.qtdeclarative}/lib/qt-6/qml \
      --max-warnings 0 \
      ${../shell}/*.qml

    for f in ${../shell}/*.qml; do
      install -Dm444 "$f" "$out/share/zde/shell/$(basename "$f")"
    done
  ''
