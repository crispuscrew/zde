# zde - Installing it on a machine

For putting zde on a laptop and using it. If you only want to look at it, the
live image needs no install and [`verify.md`](verify.md) is the shorter path.

## Read this first

zde is early, and this is what "early" means in practice:

- **Most of the cheatsheet is silent.** The desks, the queue, notifications, the
  bar, the picker, a terminal and an editor work. The palette, the launcher,
  ask, clip, pass, capture, media and most of the system group are keys that do
  nothing. [`verify.md`](verify.md) has the split key by key.
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

While you are in `configuration.nix`:

- **The password.** The template ships `initialPassword = "zde"` so that the
  first boot lets you in. It applies once, at account creation, and it sits in
  the world-readable store until you change it. Change it with `passwd` on the
  first login, or put a hash in `initialHashedPassword` now
  (`mkpasswd -m yescrypt`) and never type the plain one at all.
- **`video` is already in the groups**, which is what makes the brightness keys
  work.
- **A laptop** wants `zde.laptop.enable = true` in the flake's module block:
  battery, radios, and the lid switch.
- **A second keyboard layout**, if you use one. `Mod+space` switches between
  them and has nothing to switch to until you say so:

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
zde app list            # what Mod+t and Mod+e will run
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
one: stand on a workspace worth keeping and run
`zde desk move-workspace-to regulars`.

## 5. Apps, and the sandbox

A desk manifest can declare what the desk is for. Nothing launches it yet, so
this is a record rather than a machine that starts things:

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
nothing happening. An instance cannot be threaded through a launch yet - zinc
addresses one as `browser@work` and `zcr run` does not take one until 0.8.2 -
so a manifest's instance is declared and not yet started.

## 6. When it breaks

It will. In descending order of how much it hurts:

- **A key did nothing.** Most likely it is one of the silent ones; check the
  table in [`verify.md`](verify.md) before assuming a fault.
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
  read, and whether the screen lock could accept a password - which is the one
  worth running before you need it, since a locker with no PAM service takes the
  screen and then refuses every password. `warn` is a session you can work in
  and `fail` is not, so the exit status counts only the failures. Paste the
  whole thing into a bug report.

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
