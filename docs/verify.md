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

Build it from the branch you mean to test. `dev` has the desks, the queue and
notifications; **the input layer is on `feat/input-layer` and is most of section
2**, so if that branch is still open, build from it and do the whole list in one
boot.

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
  sticky. It is one number in
  [`input/kanata.kbd`](../input/kanata.kbd) and this is the only way to pick it.
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

- **The shell**: no bar, no picker, no notification centre, no desk switcher.
  Pressed as a key, `Mod+Tab` does nothing visible - it lists the desks on a
  stdout nobody is reading, which is the honest thing it can do without a
  surface (`zde desk switcher` in a terminal shows the same list). Every bind
  that spawns `zde ask`, `zde clip`, `zde capture`, `zde media`, `zde system`,
  `zde palette`, `zde window jump-to` or `zlg` does nothing, silently. What is
  live today: the desk group, `zde workspace next|prev`, `zde nav up|down`,
  the queue, `zde status`, and the niri natives (columns, monitors,
  fullscreen, overview).
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
