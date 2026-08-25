{
  bluez,
  fetchpatch2,
}:
let
  avrcpBackports = [
    {
      commit = "bd8989620ed6e80755f06cfdb18f5b4a3913493c";
      hash = "sha256-Wt5q2/l5d0dBqswCwCqoZsPwEWd/6xibmmGmi9Gs7zQ=";
    }
    {
      commit = "58088149872d014684a582fdb7ad01a5180c9bc5";
      hash = "sha256-658sZG1NDiNw77ThY6Y5okgedkClvxWvy/K9VKR1nlw=";
    }
  ];
in
bluez.overrideAttrs (old: {
  patches = (old.patches or [ ]) ++ map (backport: fetchpatch2 {
    name = "bluez-avrcp-${backport.commit}.patch";
    url = "https://github.com/bluez/bluez/commit/${backport.commit}.patch";
    inherit (backport) hash;
  }) avrcpBackports;

  passthru = (old.passthru or { }) // {
    zdeAvrcpPatchCommits = map (backport: backport.commit) avrcpBackports;
  };
})
