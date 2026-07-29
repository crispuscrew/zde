# zde - Verify by hand

The list of things a machine has to answer, because nothing else can.

CI settles a lot: `nix flake check` evaluates both layers, the go tests pin
the model, and the smoke test ([`nix/tests/smoke.nix`](../nix/tests/smoke.nix))
boots a NixOS host in QEMU and drives a real niri through the desks, the queue
and a notification. What it boots has no GPU, no keyboard, one virtual screen
and nobody looking at it. Everything below is what that leaves over.

Work down it in order. Each item says what to do and what should happen; where
something is known to be missing, it says so, so the finding is not rediscovered
three times.

## Getting there

There is no installer yet ([`delivery.md`](delivery.md), sequencing), so the
way onto hardware is a live image: it boots, it touches no disk, and it carries
the same layer 0 and layer 1 an install would.

```sh
nix build .#zde-iso          # gigabytes, and the better part of an hour
sudo dd if=result/iso/zde-live.iso of=/dev/sdX bs=4M status=progress oflag=sync
```

On a machine with no nix, build it in a container and take the file out - the
image is self-contained, so only the build needs nix:

```sh
podman run -d --name zde-nix -v "$PWD":/w docker.io/nixos/nix sleep infinity
podman exec zde-nix sh -lc 'cd /w && nix build .#zde-iso --no-link --print-out-paths'
podman cp zde-nix:<that path>/iso/zde-live.iso ./zde-live.iso
```

Build it from the branch you mean to test. `dev` has the desks, the queue and
notifications; **the input layer is on `feat/input-layer` and is most of section
2**, so if that branch is still open, build from it and do the whole list in one
boot.

### In a VM first

Most of this list does not need the metal, and a VM turns a boot into a minute
instead of a stick. [`dev/vm.sh`](../dev/vm.sh) runs one. It needs no nix, only
qemu and KVM, so it runs on whatever machine you are sitting at:

```sh
dev/vm.sh                       # a window, one screen
dev/vm.sh --screens 2           # two outputs, for the band-per-screen items
dev/vm.sh --uefi                # boot the way your own hardware does
dev/vm.sh --disk zde.qcow2      # a disk to try installing onto
dev/vm.sh --boot-shot boot.png  # one screenshot: did it boot at all
```

What the script really encodes is the **virtual GPU**, because getting that
wrong produces the most misleading failure in the whole exercise. With a
display-only card - `-vga std`, or virtio without `gl=on` - niri comes up,
opens its Wayland and IPC sockets, and never draws, leaving the console text on
screen. That looks exactly like the black screen this project is most afraid
of. Its log is the tell: `failed to initialize renderer, falling back to
primary gpu: software EGL renderers are skipped`, then `no allocator available
for device`. niri skips software EGL on purpose. `virtio-vga-gl` plus `gl=on`
gives virgl, and niri initialises against it the way it would against a real
card.

That is also why `--boot-shot` stops at the greeter and says so: a screenshot
off the qemu monitor needs a surface in main memory, and there is none behind
virgl - not headless, not through VNC. So the screenshot mode trades the
session for a picture, which is the right trade for "does this image boot" and
no use for anything after it.

### Your host is already using Super

It will be, and then no `Mod` chord reaches the guest, which is most of the
keymap. Two ways out.

**Ctrl+Alt+G** in the qemu window toggles an input grab, and while it holds,
Super goes to the guest. Whether it works is the host's decision, not qemu's:
it needs the host compositor to honour keyboard-shortcuts-inhibit (KDE does,
GNOME does not).

When the host will not let go, move `Mod` in the guest instead. `dynamic.kdl` is
the one file in the generated config that is writable, niri reloads it the
moment it is written, and `input` is a section niri *merges* rather than
replaces - so this works on a running session, from the guest's own terminal:

```sh
cat >> ~/.config/niri/dynamic.kdl <<'EOF'
input {
    keyboard {
        xkb {
            options "altwin:swap_lalt_lwin,fkeys:basic_13-24"
        }
    }
}
EOF
```

Left Alt is `Mod` from then on, and it costs nothing: no zde chord uses Alt. If
your keyboard has a Menu key, `altwin:menu_win` is gentler - it makes Menu act
as Super and leaves Alt where it was.

Keep every option you need in that one string. xkb options are a single field,
and a later `input` block replaces it rather than appending, so writing only the
swap on an image built from `feat/input-layer` would silently drop
`fkeys:basic_13-24` and take the band keys with it.

What a VM can answer: the session coming up, the input layer (kanata grabs the
guest's keyboard, and whether a keysym arrives as F13 or as `XF86Tools` is
decided entirely inside the guest), notifications, the queue, adoption, the
desk invariants, and two *virtual* screens with `--screens 2`.

What it cannot: latency, which is meaningless under virtio and a software
renderer; a real GPU, which is the thing that decides between a session and a
black screen; docking and undocking; the lid, the battery, and any keyboard
you would actually plug in.

### Then

Boot the stick, log in at the greeter as **zde / zde**, and open a terminal
with **Mod+Return** (Mod is the Super/Windows key). That bind belongs to the
image, not to zde: every launch key in the keymap spawns a `zde` subcommand
that lands with the shell, so without it the session has no way to type into
it.

If the session does not come up at all, Ctrl+Alt+F2 is a shell - the installer
profile autologins `nixos` there with passwordless sudo, and greetd only takes
tty1. The two logs worth reading:

```sh
sudo journalctl -b -u greetd
sudo journalctl -b _SYSTEMD_USER_UNIT=zded.service
```

Then declare two desks, which is the same by-hand path a new user has:

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

Use your own output names (`niri msg outputs`). Two desks, not one: with a
single desk, next and prev are the same key and prove nothing.

## 1. The session comes up

Nothing has ever run this. The smoke test asserts greetd is installed and that
its units are where systemd looks; it never logs anybody in.

- The greeter appears on the console, and the password works.
- niri starts on the real GPU rather than dropping back to a black screen.
  This is the one to report with the machine's graphics chip attached.
- `zde status` says `compositor connected`. If it says anything about
  `NIRI_SOCKET`, zded started outside the session's environment, and that is
  the interesting half of the bug.
- `systemctl --user status zded` is running, and was started by
  `graphical-session.target` rather than by you.
- Log out and back in: the second session gets a zded of its own, not a stale
  socket from the first.

## 2. The input layer

Only on `feat/input-layer`. It is the reason that branch is unmerged: the
config is checked at build time by `kanata --check`, and what no check can
answer is whether the keys feel right under fingers.

- **Tab is still Tab.** In a terminal: completion. In a browser: field to
  field. In an editor: indent. This is the risk the whole layer carries - a
  tap-hold on Tab that guesses wrong makes the most-pressed key on the board
  unreliable.
- **Held Tab plus j / k walks this desk's band**, and stops at its ends rather
  than wrapping into someone else's workspaces. The arrows do the same thing
  for a hand off home row.
- **200ms is the number to argue with.** Type fast and see whether a Tab ever
  starts the layer by accident; hold deliberately and see whether Tab feels
  sticky. It is one number in `input/kanata.kbd` (on that branch, not on
  `dev`), and this is the only way to pick it.
- **Tab autorepeat is gone** - tap-hold eats it. Find out over an hour whether
  anything you do needs a held Tab that repeats.
- **F13 and F14 arrive as themselves.** The default xkb map turns FK13 into
  `XF86Tools`, which is why the config sets `fkeys:basic_13-24`. If the band
  keys do nothing, check this first: run `wev` (on the image for this), hold
  Tab and press j, and read the keysym it reports.
- **The keyboard you actually use.** kanata grabs a device: an external
  keyboard plugged in after login, or a laptop's own versus a dock's, is where
  this breaks.

## 3. Latency

Deferred here on purpose rather than optimised on a guess. One `Mod+j` spawns a
`zde`, which asks zded, which asks niri for the focused window, moves it, and
asks again; a rotation re-reads the map and the focused name several times more.
Every one is a socket round trip, so the process spawn should dominate.

- Hold `Mod+j` and let it repeat. Does the strip keep up, or does it lag behind
  the key?
- The same across a desk rotation, which is the heaviest path.
- If it drags: the reads fold into one pass. That is a real change with a real
  cost, and it is not worth making until a finger says so.

## 4. Two screens

The VM has one output, so everything about the second is unverified, and the
desk model is defined in terms of monitors (docs/model.md, invariant 1).

- **A bar on each screen**, and the right width on each. The panels are built
  from niri's output list, so this is also the unplug test: pull a screen and
  its bar should go with it, leaving the other one alone. The VM has done that
  with two virtual outputs (`dev/vm.sh --screens 2`); what it has not done is a
  real monitor with a different resolution and scale.
- **What the bar reserves.** It asks for 26 pixels at the top of every output
  and the smoke test only checks that it asked. Whether niri actually kept
  windows out of that strip is a thing to look at: if a window's title bar is
  under the bar, the exclusive zone is not being honoured.
- A desk whose manifest names two monitors comes up on both.
- `Mod+bracketleft` / `bracketright` move between them; `Mod+Shift+` the same
  carries the window.
- `zde workspace next` stays inside the band **on the screen you are on**. The
  band is filtered by the output the workspace is actually on, not the one its
  manifest named, which matters for the next item.
- **Undock.** A desk that named a monitor which is no longer there: does the
  session survive, does `zde workspace next` still walk something sensible, and
  does `zde desk switch` say something useful rather than nothing?
- **Redock**, and plug the screen into a different port. niri's output names
  are stable per connector, so a manifest naming `DP-1` is looking at a cable.

## 5. Notifications from real apps

The smoke test drives `notify-send`, which is one well-behaved client. Real
ones are the point of zded holding the bus name.

- Something you actually use - a browser, a mail client - and its notification
  lands in `zde queue` with its text and the desk you were on.
- **A volume OSD or anything that reuses one id.** It must replace, not pile
  up. This is what per-sender id mapping is for and it has never met a real
  client.
- `notify-send --wait "test"` in one terminal, `zde queue done <id>` in
  another: the first command returns.
- An app that expects a popup and gets a queue entry: does it misbehave, or
  quietly carry on? There is no notification centre yet, so a critical
  notification is silent until you look.
- Two terminals, two `notify-send`s: neither can close or replace the other's.
  Closing from the wrong one should leave the item where it is:

  ```sh
  dbus-send --session --dest=org.freedesktop.Notifications \
    /org/freedesktop/Notifications \
    org.freedesktop.Notifications.CloseNotification uint32:<id>
  ```

## 6. A day of work

The invariants are about a week, not a minute. These are the ones that only
show up in use.

- **Adoption**: open something on a fresh workspace and it takes the name of
  what is in it, on the desk you were standing on.
- **Nothing moves under you** (invariant 6): focus changes never rearrange the
  strip.
- **Coming back**: `zde desk last` after a detour lands where you left, on the
  workspace you left, not on the desk's first one.
- **The regulars fill up.** Work goes into the band and there is no way to get
  it back out except `zde desk move-window-to <desk>`, one window at a time.
  Watch how fast that becomes annoying - that is the argument for whatever
  replaces it.
- **The queue**: leave things in it for a day. `zde desk queue-jump` should
  take you where the oldest one was written, and the desk it names should still
  make sense hours later.

## Expected to be missing

Not bugs, do not report them:

- **Most of the shell**: there is a bar now, and no picker, no notification
  centre, no desk switcher. Pressed as a key, `Mod+Tab` does nothing visible -
  it lists the desks on a stdout nobody is reading, which is the honest thing it
  can do without a surface (`zde desk switcher` in a terminal shows the same
  list).
- **Most of the cheatsheet.** A bind whose command is not written yet prints
  usage to a stderr nobody reads, so the key is silent and so is the machine.
  What is live today, and nothing else:

  | Works | Silent |
  |---|---|
  | `Mod+j`/`k` and the arrows (nav) | `Mod+Shift+Escape` (panic), `Mod+Shift+z` (zen) |
  | `Mod+Shift+j`/`k` (move window) | `Mod+a`, `Mod+Shift+a` (ask) |
  | `Mod+r` (regulars), `Mod+u` (queue jump) | `Mod+v`, `Mod+Shift+v` (clip, pass) |
  | `Mod+Shift+Tab` (last desk) | `Mod+g`, `Mod+semicolon` (launcher, palette) |
  | `zde workspace next\|prev`, `zde desk *`, `zde queue *`, `zde status` | `Mod+t`, `Mod+e` (launch), `Mod+w` (jump to window) |
  | the niri natives: columns, monitors, fullscreen, float, close, overview, consume/expel | `Mod+m` (modes), `Mod+n`, `Mod+slash`, `Mod+c`, `Mod+p`, `Mod+q`, and the rest of the system group |

  `Mod+Shift+Escape` is worth singling out: panic is the key you reach for
  first when something goes wrong, and it is one of the silent ones.
- **Modes** (`Mod+m`) and the leader sequences for panic and block. They wait
  on the input layer landing.
- **Brightness** (`Mod+b`, `Mod+Shift+b`): layer 0 installs no udev rules for
  `brightnessctl`, so it probably cannot write the backlight as your user. If
  it works anyway, say so - that is one line either way.
- **The keyboard layout** is whatever niri defaults to. `zde.niri.extraConfig`
  is where a host's own layout goes, and whether it should be taken from the
  system's console keymap instead is still open.
- **Persistence**: it is a live image. The journal, the queue and anything you
  configure are gone on reboot.

## What to do with what you find

One finding, one line, with the machine it happened on. Anything in section 2
decides whether `feat/input-layer` merges as it stands; anything in section 3
decides whether the IPC reads get folded together; the rest lands as issues
against the roadmap's own verify list ([`roadmap.md`](roadmap.md)).
