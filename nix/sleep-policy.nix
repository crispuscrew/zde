{ config, lib, ... }:
let
  cfg = config.zde;
  # systemd 260.2 maps hybrid-sleep and suspend-then-hibernate to the
  # hibernate authorizations; these are all sleep IDs its policy defines.
  sleepActions = [
    "org.freedesktop.login1.suspend"
    "org.freedesktop.login1.suspend-multiple-sessions"
    "org.freedesktop.login1.suspend-ignore-inhibit"
    "org.freedesktop.login1.hibernate"
    "org.freedesktop.login1.hibernate-multiple-sessions"
    "org.freedesktop.login1.hibernate-ignore-inhibit"
  ];
in
{
  config = lib.mkIf cfg.enable {
    services.logind.settings.Login = lib.genAttrs [
      "HandlePowerKey"
      "HandlePowerKeyLongPress"
      "HandleSuspendKey"
      "HandleSuspendKeyLongPress"
      "HandleHibernateKey"
      "HandleHibernateKeyLongPress"
      "HandleLidSwitch"
      "HandleLidSwitchExternalPower"
      "HandleLidSwitchDocked"
      "IdleAction"
    ] (_: lib.mkForce "ignore");

    # This runs before NixOS's general polkit rules. Non-root desktop callers
    # get a hard denial; root retains the administrative system boundary.
    environment.etc."polkit-1/rules.d/00-zde-no-sleep.rules".text = ''
      var zdeSleepActions = ${builtins.toJSON sleepActions};
      polkit.addRule(function(action, subject) {
        if (subject.user !== "root" && zdeSleepActions.indexOf(action.id) !== -1) {
          return polkit.Result.NO;
        }
      });
    '';
  };
}
