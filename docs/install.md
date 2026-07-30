# zde - Installing it on a machine

For putting zde on a laptop and using it. If you only want to look at it, the
live image needs no install and [`verify.md`](verify.md) is the shorter path.

## Read this first

zde is early, and this is what "early" means in practice:

- **Most of the cheatsheet is silent.** The desks, the queue, notifications, the
  bar, the picker, a terminal and an editor work. The palette, the launcher,
  ask, clip, pass, capture, media and most of the system group are keys that do
  nothing. [`verify.md`](verify.md) has the split key by key.
- **Apps are not sandboxed yet.** The premise of zde is that every app runs
  under zinc; layer 2 is not here, so what `Mod+t` starts is an ordinary host
  program. Nothing about the security model in [`vision.md`](vision.md) is true
  of this install yet.
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
zde status              # zded up, compositor connected
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

## 5. When it breaks

It will. In descending order of how much it hurts:

- **A key did nothing.** Most likely it is one of the silent ones; check the
  table in [`verify.md`](verify.md) before assuming a fault.
- **The session is wrong but the machine is fine.** `Ctrl+Alt+F2` is a text
  console you can log into with the same password. From there:
  `journalctl --user -u zded`, `systemctl --user restart zded`, and
  `systemctl --user restart zde-bar`.
- **A rebuild broke the session.** `sudo nixos-rebuild switch --rollback`, or
  pick the previous generation in the boot menu. This is why NixOS is the
  reference platform, and it is worth doing once on purpose before you need it.
- **The machine will not boot at all.** The boot menu holds every generation you
  have built. The one above the newest is the one that worked.

Keep the live stick in your bag. It is a working system, it needs no disk, and
it can mount and repair the installed one.

## 6. Updating

zde is an input to your flake, so an update is two steps and neither is
automatic ([`update.md`](update.md) has the detail, including what a switch
restarts underneath you):

```sh
cd /etc/nixos
sudo nix flake update zde
sudo nixos-rebuild switch --flake .#zdebox
```

Use `boot` instead of `switch` for anything touching the kernel or mesa, and log
out and back in after a niri bump: a running compositor is not replaced by a
rebuild, and its config is.
