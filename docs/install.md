# zde - Installing it on a machine

For putting zde on a laptop and using it. If you only want to look at it, the
live image needs no install and [`verify.md`](verify.md) is the shorter path.

## Read this first

zde is early, and this is what "early" means in practice:

- **Part of the cheatsheet is silent.** The desks, the queue, notifications and
  the centre that reads them, the bar, the picker, the palette, the wifi list,
  screenshots, the clipboard history on `Mod+v` and a terminal work. pass,
  media, the modes beyond Normal, panic and zen, the calendar, the wallpapers
  and the power menu are keys that do nothing. Three more do nothing until you
  say what they are:
  ask has no tier until a machine names one, `Mod+e` has no editor until
  `zde.apps.editor` names one (section 4), and bluetooth is a command rather
  than a key. `Mod+semicolon` says which is which on the machine in front of
  you; [`verify.md`](verify.md) has the same split written down.
- **Apps are not sandboxed unless you sandbox them.** The premise of zde is that
  every app runs under zinc. The tools are installed now (`zcr`, `zc`) and the
  runtime under them is on, so a sandboxed app is a thing you can define and put
  on a key - but nothing zde ships does it for you, and what `Mod+t` starts is
  an ordinary host program. Most of the security model in
  [`vision.md`](vision.md) is not true of this install until you define apps
  that way.
- **It has never run for a week.** Or a day. Bugs found in ordinary use are the
  point of installing it, and the ones that matter will be found by you.

So: **do not install this over the system you depend on.** A spare disk, a spare
partition, or an external SSD you can unplug. NixOS makes the machine easy to
roll back and does nothing about a disk you have already reformatted.

## What you need

A machine that boots UEFI (nearly everything since about 2012), the live image,
and about twenty minutes. Build the image and write it to a stick as in
[`verify.md`](verify.md), boot it, and log in at the greeter as **zde / zde**.

Everything below is typed in that session. Open a terminal with **Mod+Return**
(the live image binds it; a real install uses `Mod+t`).

## 1. The disk

Look before you cut. `lsblk` names the disks; the one you want is almost never
the one the live image booted from.

```sh
lsblk -o NAME,SIZE,MODEL,MOUNTPOINTS
```

Then, for a disk you are giving entirely to zde - **this erases it**:

```sh
sudo -i
DISK=/dev/nvme0n1          # yours, from lsblk. Check it twice.

parted "$DISK" -- mklabel gpt
parted "$DISK" -- mkpart ESP fat32 1MiB 1GiB
parted "$DISK" -- set 1 esp on
parted "$DISK" -- mkpart root ext4 1GiB 100%

mkfs.fat -F32 -n BOOT "${DISK}p1"     # p1/p2 on nvme, 1/2 on sata
mkfs.ext4 -L nixos "${DISK}p2"

mount /dev/disk/by-label/nixos /mnt
mkdir -p /mnt/boot
mount /dev/disk/by-label/BOOT /mnt/boot
```

Encryption, swap and a separate home are all reasonable and all out of scope
here: the NixOS manual covers them, and nothing about zde changes them.

## 2. The configuration

The template is the flake a zde machine is built from. It goes into an **empty**
directory, because `nix flake init` refuses to overwrite and would otherwise
leave you with half of it:

```sh
mkdir -p /mnt/etc/nixos && cd /mnt/etc/nixos
nix flake init -t github:crispuscrew/zde#host
```

That writes `flake.nix`, `configuration.nix`, and a `hardware-configuration.nix`
that refuses to evaluate until you replace it - which is the next line, and the
one part nobody can write for you:

```sh
nixos-generate-config --root /mnt --show-hardware-config > hardware-configuration.nix
```

Now edit two files. In `flake.nix`, `user` and `host`; in `configuration.nix`,
the matching `users.users.<name>`, the hostname, and the timezone. Nothing
checks that the two agree, and a mismatch fails at evaluation with a missing
attribute rather than anything friendlier.

While you are in those two files:

- **The password.** The template ships `initialPassword = "zde"` so that the
  first boot lets you in. It applies once, at account creation, and it sits in
  the world-readable store until you change it. Change it with `passwd` on the
  first login, or put a hash in `initialHashedPassword` now
  (`mkpasswd -m yescrypt`) and never type the plain one at all.
- **`video` is already in the groups**, which is what makes the brightness keys
  work.
- **A laptop** wants `zde.laptop.enable = true` in the flake's module block:
  battery, the network radio, bluetooth, and the lid switch. A desktop that
  wants bluetooth and none of the rest says `zde.bluetooth.enable = true`
  instead; both are off by default, because a radio nobody asked for is a
  listening radio nobody asked for.
- **A second keyboard layout**, if you use one. `Mod+space` switches between
  them and has nothing to switch to until you say so. The niri config is layer
  1, so these two go in the flake's `home-manager.users.<name>` block, beside
  `zde.enable` and the commented-out `zde.niri.extraConfig`. Not in
  `configuration.nix`: there they are not options at all, and the build says so
  in the least helpful way it has.

  ```nix
  zde.niri.xkb.layout = "us,ru";
  zde.niri.xkb.options = "grp:caps_toggle";
  ```

## 3. Install

```sh
nixos-install --flake /mnt/etc/nixos#zdebox     # or your host name
reboot
```

The first build downloads a compositor, a Qt runtime and the tools, so it is not
quick on a hotel connection. It is also the last time it will be that slow.

## 4. The first session

Log in at the greeter. Then, in order:

```sh
zde status              # zded up, compositor connected, zinc yes
zde doctor              # every check on one screen, if any of that looks wrong
zde app list            # what the launch keys can start on this machine
zde keys                # every bind, one per line (Mod+slash shows this too)
```

That third line will print `help`, `lock` and `terminal`, and no editor. Those
three have defaults because layer 1 installs the programs behind them; the
editor zde ships is a zinc container (`common/apps/nvim`), which is layer 2's
to build and pin, so `Mod+e` starts nothing until this machine says what an
editor is. It says so in one line, in the flake's `home-manager.users.<name>`
block beside `zde.enable`:

```nix
zde.apps.editor = [ "foot" "-e" "hx" ];             # a host program
zde.apps.editor = [ "zcr" "run" "nvim" "--exec" ];  # or a sandboxed one
```

Two desks, which is the smallest number that makes the model do anything:

```sh
mkdir -p ~/.config/zde/desks
cat > ~/.config/zde/desks/work.yaml <<'EOF'
name: work
monitors:
  eDP-1: { workspaces: [code, notes, web] }
EOF
cat > ~/.config/zde/desks/home.yaml <<'EOF'
name: home
monitors:
  eDP-1: { workspaces: [media, read] }
EOF
zde desk list
```

Use your own output name from `niri msg outputs`. Then `Mod+Tab` picks a desk,
`Mod+j`/`Mod+k` walk them, and `Mod+t` opens a terminal.

The regulars - the band reachable from every desk - do not exist until you make
one: stand on a workspace worth keeping, press `Mod+Ctrl+Shift+Tab` and pick
`regulars`, which is offered before there is a band. `zde desk
move-workspace-to regulars` is the same thing typed.

## 5. Apps, and the sandbox

A desk manifest declares what the desk is for:

```yaml
apps:
  - { app: browser, instance: work, monitor: eDP-1, workspace: web }
```

`zde desk apps` prints what a desk declares - the address zinc takes for each
app, the workspace it is pinned to, and where zinc says that instance keeps its
state. The last one is asked rather than assumed, which is why it is worth
printing: two desks can declare the same app, and what makes them two browsers
instead of one is that directory.

Defining the app itself is zinc's, and `zc` is what does it. Once an app is
defined, putting it on a key is one line in your flake's module block:

```nix
zde.apps.browser = [ "zcr" "run" "browser" "--exec" ];
```

Then `zde app launch browser` runs it, and any key can. Without `--exec` zcr
prints the launch plan and exits, which from a keybind looks exactly like
nothing happening. An instance goes in the same argv - `zcr run browser@work
--exec` - so two desks can each have their own browser.

A desk manifest starts its own apps: entering the desk runs each one it
declares, and zinc refuses a second launch of something already up, so
switching back and forth does not pile up browsers - and does not complain
about it either, because an app that is already running is the state the switch
was asking for. The launches happen behind the switch, so the desk is in front
of you before they arrive, and a failure never takes the switch down with it:
it arrives as a notification instead, one for the whole switch, saying which
apps did not start and what the runner said about them. `journalctl --user -u
zded` has every one of them whole, and `zde doctor` says which of your desks
name an app this machine cannot start before you go there at all - asking zcr,
since a manifest's `app:` is a zinc app name and not one of the logical names
`zde.apps` holds. Each of those lines ends with which of the two answered,
because on a machine with no zcr the only map left is the wrong one and a
warning off it can be about nothing at all.
Where a window lands is the pin, if the manifest also says what the window
calls itself:

```yaml
apps:
  - { app: browser, instance: work, app_id: org.mozilla.firefox,
      monitor: eDP-1, workspace: web }
```

`app_id` is the Wayland application id, which is not the zinc app name - `niri
msg windows` prints the ones you have open. zded turns the pin into a niri
window rule, so the window opens on that workspace rather than in front of you.
Leave it out and the app still starts; adoption places it. The rules are written
when zded starts and on `zde desk reconcile`, and not on every switch, so a
manifest edited while the session runs wants a reconcile before the pin bites.

## 6. When it breaks

It will. In descending order of how much it hurts:

- **A key did nothing.** On a machine you have just installed, the likeliest one
  is `Mod+e`: nothing is configured to be an editor until you say so (section
  4). `zde app list` settles it in one line, since it prints what this machine
  can start and an editor is not on it until you put one there. The key itself
  does say so, but it says it to the journal a keybind's output goes to rather
  than to the screen, which is why the list is the faster question.
  Otherwise it is one of the silent ones.
  `Mod+semicolon` is the answer from the build in front of you: every action by
  name, with the ones nobody has written marked as such and the key each would
  have been. `Mod+slash` opens the keymap in a pager and `zde keys` prints the
  same list from a terminal, which is what every key is bound to, one per line.
  The table in [`verify.md`](verify.md) says which of them are wired to
  something yet.
- **The session is wrong but the machine is fine.** `zde doctor` first. One line
  per check, on one screen, because this is the moment when there is no second
  machine to look anything up on:

  ```
  ok    zded          0.1.0, socket at /run/user/1000/zde/zded.sock
  ok    compositor    connected
  warn  shell         not listening: Mod+Tab prints a list instead of a picker
  fail  notify        org.freedesktop.Notifications is owned by dunst, pid 812
  ```

  It covers the daemon and the socket it is on, whether it can see the
  compositor, whether a shell is listening (no shell is the whole diagnosis for
  a session where `Mod+Tab` prints a list instead of drawing a picker), who has
  the notification name when it is not zded, whether zinc is on this machine at
  all, whether the units a login starts are up, which manifests it could not
  read, which of your desks name an app nothing here can start (and which
  resolver said so - zcr where there is one, `zde.apps` otherwise), whether the
  screen lock could accept a password, and whether logind would let this session
  log out, suspend, reboot or power off. The last two are the ones worth running
  before you need them: a locker with no PAM service takes the screen and then
  refuses every password, and a power key that polkit refuses is one that does
  nothing at all. `warn` is a session you can work in and `fail` is not, so the
  exit status counts only the failures. Paste the whole thing into a bug report.

  `zde status` is the same daemon answering a narrower question: what it is
  doing now - the desk you are on, how many things are queued, and whether the
  shell, the notification name and zinc are there.

  Then `Ctrl+Alt+F2` is a text console you can log into with the same password:
  `journalctl --user -u zded`, `systemctl --user restart zded`, and
  `systemctl --user restart zde-bar`.
- **A rebuild broke the session.** `sudo nixos-rebuild switch --rollback`, or
  pick the previous generation in the boot menu. This is why NixOS is the
  reference platform, and it is worth doing once on purpose before you need it.
- **The machine will not boot at all.** The boot menu holds every generation you
  have built. The one above the newest is the one that worked.

Keep the live stick in your bag. It is a working system, it needs no disk, and
it can mount and repair the installed one.

## 7. Updating

zde is an input to your flake, so an update is two steps and neither is
automatic ([`update.md`](update.md) has the detail, including what a switch
restarts underneath you):

```sh
cd /etc/nixos
sudo nix flake update zde
sudo nixos-rebuild switch --flake .#zdebox
```

zinc - the sandbox - is a second input in that same flake, pinned to a tag, so
updating the desktop does not update it and the other way round. Moving it is
editing the tag in `flake.nix` and then:

```sh
sudo nix flake update zinc
sudo nixos-rebuild switch --flake .#zdebox
```

Nothing already running is restarted by that: the tools on PATH become the new
ones, and an app that is up stays as it was started until you stop it.

Use `boot` instead of `switch` for anything touching the kernel or mesa, and log
out and back in after a niri bump: a running compositor is not replaced by a
rebuild, and its config is.
