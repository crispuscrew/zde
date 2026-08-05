# zde - Verify by hand

The list of things a machine has to answer, because nothing else can.

CI settles a lot: `nix flake check` evaluates both layers, the go tests pin
the model, and the smoke test ([`nix/tests/smoke.nix`](../nix/tests/smoke.nix))
boots a NixOS host in QEMU and drives a real niri through the desks, the queue,
a notification and the centre that reads it back, a desk starting what its
manifest declares, and the bar saying it has no microphone and no
NetworkManager to ask. What it boots has no GPU, no keyboard, one virtual
screen and nobody looking at it - and no microphone, no access point and no
bluetooth adapter, which is why it can only check that those read as absent.
Everything below is what that leaves over.

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

Build it from the branch you mean to test. `dev` has the desks, the queue,
notifications and the shell; **the input layer is on `feat/input-layer` and is
most of section 2**, so if that branch is still open, build from it and do the
whole list in one boot.

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
you would actually plug in. Nor the radios and the microphone (section 7): it
has no adapter, no access point and no capture device, so the most it can say
is that the bar and the CLI call each of them absent instead of guessing.

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
- `zde status` says `zinc       yes`, which is the session finding layer 2's
  runner on its own PATH. It is a different question from `command -v zcr` in a
  terminal, and the one that matters: a keybind runs with the session's
  environment.
- `Mod+g` opens zinc's launcher (`zlg`) over an empty list. Empty is right on
  a fresh machine - nothing has defined an app yet - and a key that draws
  nothing at all is the report.
- `Mod+slash` opens the keymap in a pager, and `q` closes it. `Mod+semicolon`
  is the palette: the same actions, filtered as you type, run by name, and with
  the ones nobody has written yet marked as such rather than left to be found by
  pressing them. Between the two they are what to reach for when a key does
  nothing, and they are worth pressing before anything else on this list.
- `Mod+Print` takes a screenshot and puts it on the clipboard. `Mod+Shift+s`
  opens niri's region picker, `Mod+Ctrl+w` takes the focused window. They land
  where niri's `screenshot-path` default puts them, `~/Pictures/Screenshots`,
  and whether that directory gets created on a machine that has never had one
  is the part only a real session answers.

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
- **A jump across desks moves both screens.** `Mod+w` to a window on another
  desk brings that desk up everywhere before focusing the window, so the monitor
  you were not looking at changes as well. One output cannot show this, which is
  why the smoke test cannot either.
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
  quietly carry on? Nothing pops up here at all - `Mod+n` is where you look -
  so a critical notification waits instead of interrupting, and whether that is
  livable for a whole day is the thing to find out.
- **`Mod+n`, the notification centre**, on the same apps. Newest first, `j`/`k`
  and the arrows walk it, `d` dismisses through `queue.done` so the sender is
  told, Escape closes. The rows worth hunting for are the ones from an app that
  declared actions: the line under the list numbers every one of them with the
  sender's own labels, a digit presses it, and the one the sender called default
  is marked and is what Enter does. Nine is the bound, and where a sender
  declared more, that same line says how many it cannot reach rather than
  quietly showing fewer.
- **What an app does once it sees "actions" claimed.** zded claims it, because
  every action a sender declares is offered and not only the default. What it
  does not claim is immediacy: there is no popup, so the buttons are behind
  `Mod+n`, and an app reading the capability as "there will be a button on the
  screen when I send" is the case nobody has met. A mail client offering archive
  and delete, or a download offering to open the file, is what to try it with.
- **The three modes, over a working day.** `zde attn work|focus|quiet`, and
  `Mod+q` for quiet when you need silence now. work queues everything, focus
  queues only what the sender called urgent, quiet queues none of it, and all
  three keep the lot in the centre. The bar says which one you are in, and the
  mode itself lives in the journal, so a `systemctl --user restart zded` comes
  back in the mode you left rather than quietly reverting to work. What to feel
  for is whether focus lets through what you actually wanted: urgency is the
  sender's own claim, and a sender that never sets it is invisible in focus.
- **A day's worth of arrivals.** History is a ring of 200, in memory: the 201st
  drops the oldest, and restarting zded empties it. The queue is the half that
  survives, because it is what you still owe. Whether 200 is a day or an hour
  is a question about your machine and not about the number.
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
- **Lock the screen**: `Mod+Tab` then `l`, which is the one to get into your
  fingers, and `Mod+Ctrl+semicolon` as a direct chord. Then unlock it. Deliberately
  not tested in CI: a VM with no input devices that locks itself cannot unlock
  itself. What CI does check is the half that strands you - that the locker has
  a PAM service and so can accept a password at all - and `zde doctor` answers
  the same question on the machine you are sitting at, before you find out the
  hard way. Try it before you need it in a cafe.
- **`Mod+Tab` when the shell is unwell.** Kill the bar (`systemctl --user stop
  zde-bar`) and press it: you should get the desk list printed to wherever the
  key's output goes, rather than nothing at all. The daemon waits 200ms for the
  shell to say it drew the surface, so a shell that is running but stuck should
  behave the same as one that is not running. Hard to arrange on purpose; worth
  recognising if a key ever seems to do nothing.
- **The keyboard, and the question it settles.** The roadmap has been carrying
  the worry that a layer surface holding focus reads to niri as nothing focused
  at all - so a nav key pressed just after one closes would spend itself putting
  focus back on a window. Every key that draws one: `Mod+Tab` and `Mod+w` for
  the picker, `Mod+n`, `Mod+Shift+c`, `Mod+semicolon`, `Mod+v`, and `Mod+a` or
  `Mod+Shift+a` for ask. Open each, close it with Escape, and press `Mod+j`
  immediately: if the first press goes nowhere, that is the thing, and it wants
  `keyboardFocus` on demand rather than exclusive. One surface behaving
  differently from the rest is worth as much as all of them behaving badly.
- **Whether the picker is what you want from `Mod+Tab`.** It has no text field
  on purpose - arrows, `j`/`k`, or a digit - and the palette next to it does
  have one, but it filters actions and not desks. If you find yourself typing a
  desk name at either, that is the argument for a field here.
- **The palette, on the day you have forgotten a key.** `Mod+semicolon`, a few
  letters of what you want, Enter. Getting on for eighty rows means `Ctrl+n`
  past the bottom of the list, which is the scrolling worth pressing on. A row
  for something nobody has written refuses with the reason rather than going
  quiet, and so does one whose program is not on this machine. Five niri natives
  say plainly
  that they cannot be run from here and still show their key, because the key
  works; anything else that goes quiet when picked is a report.
- **`Mod+w`, and whether you can tell your windows apart in it.** A row is the
  app id, the title, and the workspace the window is on, in the same surface the
  desk picker uses and with the same absence of a text field. Four terminals is
  the case to look at: if their titles do not distinguish them, the fix is
  either a field in this surface or a shell that sets its title, and which one
  it is decides whether 0.2 moves.
- **Where `Mod+w` leaves the screen you were not looking at.** Jumping to a
  window on another desk brings that whole desk up on every monitor and then
  focuses the window, because a desk is what all the monitors show at once
  (model.md, section 2) - focusing the window alone would leave one screen on
  the desk you came from. So the other screen moves too. It is the journey
  walking there would have made; decide whether it is the one you wanted from a
  key called jump.
- **Coming back**: `zde desk last` after a detour lands where you left, on the
  workspace you left, not on the desk's first one.
- **Making a regular, and unmaking it.** Stand on a workspace worth keeping and
  run `zde desk move-workspace-to regulars`; that is the only way the band comes
  into being. Then `zde desk move-workspace-to <desk>` to put it back. What to
  feel for: whether promoting an existing workspace is what you reach for, or
  whether you wanted to make an empty one and fill it afterwards - which is
  refused, because an empty workspace is never adopted and so has no name to
  move.
- **Where you land when you send a window to a band that is not empty.**
  `zde desk move-window-to regulars` carries the window and follows it, but it
  follows it to the *workspace*: if something is already there, you arrive
  beside the window you sent rather than on it. Decide whether that is right.
  Making it land on the carried window is a small change, and it is not obvious
  which is less surprising.
- **The queue**: leave things in it for a day. `zde desk queue-jump` should
  take you where the oldest one was written, and the desk it names should still
  make sense hours later.

## 7. The microphone and the radios

Three things the bar and the CLI report about hardware the VM has none of. CI
pins the empty cases - no capture device, no NetworkManager, no adapter, each
saying so rather than guessing - and everything below is the other half.

### The mic

- **`Mod+Ctrl+m`, and the strip changes within a frame.** That round trip is
  what proves the bar is subscribed to PipeWire rather than having read it once
  at startup. `mic muted` in grey, `mic live` in red, nothing at all otherwise.
- **A real call.** Something holding the microphone reads `mic live` for as long
  as it holds it, and clears when the call ends. Live means held and not that
  bytes are moving: a stream open and paused still reads live, which is the
  conservative reading a privacy indicator wants and the one to disagree with if
  it is wrong in practice.
- **Unplug a USB mic mid-session.** The strip should go empty rather than keep
  the last word it had.
- **A capture from something that is not the default source** goes unseen, on
  purpose: the key and the strip both mean `@DEFAULT_AUDIO_SOURCE@`, so they
  cannot disagree about which mic they are talking about. The whole graph
  belongs to the mixer widget in 0.2.

### Wifi

Needs a real access point. The VM leaves NetworkManager off on purpose, so none
of this has met one.

- `Mod+Shift+c` lists what is in range - signal, whether it is locked, whether
  there is a saved profile - with the link you are on above it. Enter joins the
  row you are on and asks for a password only when the network is secured and
  nothing is saved for it; `d` drops the link.
- **A wrong password.** Whether NetworkManager decides inside three and a half
  seconds, or the surface falls through to "joining" and leaves you watching the
  link. That bound is the known gap: the verdict is not pushed as an event yet,
  so a refusal it takes twenty seconds to reach arrives as nothing.
- **A user who is not in the `networkmanager` group** gets `AccessDenied` on
  activate. Whether that refusal reads as what it is, is the question.
- **A saved profile whose password has changed.** zde will not overwrite the
  saved secret, so this is `zde net forget SSID` and then join again - the
  refusal says so. Worth doing on a profile carrying other settings, and worth
  watching that nothing half-made is left behind after a failure.
- **Two access points on one SSID**: the merged row should join the strongest.
- `zde net status`, `zde net connect SSID` (the password on stdin, never in an
  argument), `zde net disconnect` and `zde net forget SSID` are the same thing
  from a terminal, and the path that does not take the keyboard.

### Bluetooth

Needs an adapter, something to pair with, and the radio turned on:
`zde.bluetooth.enable` on a desktop, or `zde.laptop.enable`, both off by
default. There is no bluetooth in any surface - `zde system bluetooth` is the
whole of it - so none of this is pressable from a key.

- **A phone through `pair`**, confirming the six digits on both sides, and then
  `zde system bluetooth` reading it as paired and **not** trusted. Reconnect it
  and it should ask again, until `zde system bluetooth trust ADDR`.
- **Answer nothing for 45 seconds.** The other device should report a failed
  pairing rather than hanging: an unanswered question is a refusal, and so is
  "no".
- **A headset**, to find out how noisy authorising each service really is before
  somebody decides to trust it. This is the decision in the branch most likely
  to be reverted, and only a day with real hardware settles it.
- **BlueZ's own D-Bus policy** for a normal user calling `RegisterAgent` on the
  reference host. If registration is refused there, incoming pairings are
  refused with it, and a machine nobody can pair to is the failure this asks
  about before it happens.
- **bluetoothd restarted underneath the session.** There is no
  `NameOwnerChanged` watch, so the agent registration goes with it and incoming
  pairings are refused by BlueZ until something here asks it for anything.
  Fail-closed and known; worth recognising rather than reporting.

## 8. ask

Nothing is configured by default, so the first check is the one a fresh machine
actually gets.

- **`Mod+a` on a machine with no tier set** opens the window like any other: the
  option name comes back when a question is sent, not before. So type anything
  and press Enter, and what should arrive is `zde.ask.tiers.provider` by name,
  the file that writes it, and what this machine does have. A window still
  saying nothing after Enter is the report - a question that never gets answered
  is the failure this component is arranged to design out.
- Set one - `zde.ask.tiers.provider = [ "zcr" "run" "ask-provider" "--exec" ];`
  in your flake's `home-manager.users.<name>` block, beside `zde.enable`, since
  this is layer 1 and not the system module - rebuild, and ask something. **The
  answer arrives piece by piece.** Whether that reads well, or whether a
  paragraph assembling itself is worse than one that appears at once, is a thing
  only eyes decide.
- **Two minutes** caps a tier that has stopped answering without exiting.
  Whether that is the right number with a real local model behind `local`, which
  can take a while to say anything at all, is the open half of it.
- **A tier whose binary is missing**, one that exits non-zero, and one that
  exits happily having said nothing all end in words on the screen. Worth
  breaking on purpose once, since this is the whole design.
- **Escape** stops the answer being shown, not the tier running. Nothing is
  written down anywhere - no journal line, no cache, no transcript - and the
  panel does not send the previous turns as context, so each question is its own
  run and calling it a conversation would outrun the code.
- `zde ask oneshot <question>` answers in the terminal it was typed in and never
  in a popup. `zde ask local` and `zde ask escalate` are the other tiers, and a
  question read from stdin (`zde ask local < note`) is how one stays out of the
  process list.

## 9. The clipboard history

`Mod+v`. Two of its rules are the reason it is zde's and not wl-clip-persist's,
and neither can be proved by CI: one needs a real password manager and the other
needs a real clock. Everything else here is a minute.

The daemon watches the clipboard through `wl-paste --watch`, so all of this
needs `wl-clipboard` installed - layer 1 does that (`nix/home.nix`). If it is
not there, `zde clip history` says so in one line instead of showing an empty
list, and that is the first thing to check if nothing below works.

- **The round trip.** Copy three different things from three windows, press
  `Mod+v`, type a few letters of one of them, Enter, and paste. What you chose
  is what arrives. The filter matches what is on the row and only that: the
  whole of an entry stays in zded and never reaches the shell, so a word that is
  past the end of the preview will not find it.
- **The loop.** Pick a row that is not the newest, then press `Mod+v` again.
  The list must be the same list in the same order: putting an entry back is a
  clipboard change like any other, and zde recording its own would reorder the
  history every time somebody used it.
- **The sensitive hint, with a real password manager.** This is the one that
  matters. KeePassXC is the usual one: copy a password out of it, then `Mod+v`.
  The password must not be there, and neither must a blank row where it would
  be - nothing about it is recorded, because zde asks what the offer is made of
  before it asks for any of it. Then check the daemon has never seen it either,
  with the memory check below.

  Without a password manager to hand, `wl-copy` produces the real shape by
  itself. `--sensitive` offers `x-kde-passwordManagerHint` *beside* the text
  types, which is exactly what a manager that still wants pasting to work does:

  ```sh
  wl-copy --sensitive hunter2
  wl-paste --list-types   # text/plain and x-kde-passwordManagerHint together
  zde clip history        # hunter2 must not be in it, and neither must a blank row
  ```

  That is the whole shape and not half of it, so the only thing left for a real
  manager to prove is that it spells the hint the way everything else does. If
  the `wl-copy` on the machine is older than 2.3.0 it has no `--sensitive`; then
  fall back to `wl-copy --type x-kde-passwordManagerHint hunter2`, which offers
  the hint and nothing else and proves only that the check reads the type list.

  Worth knowing while doing it: `wl-paste --watch` sets `CLIPBOARD_STATE`, and
  one of the values it defines is `sensitive`. Up to wl-clipboard 2.2.1 that
  value was never produced, and this document used to say so as a permanent
  fact. It is not one. 2.3.0, which is what the flake pins, "only sets it to
  `sensitive` when it encounters `x-kde-passwordManagerHint` among the MIME
  types" - the same type zde checks. zde still asks the offer rather than the
  variable, because the variable is silently absent on older wl-clipboard and
  would fail open there; `internal/clip/wl.go` has the argument in full.
- **The TTL, on a real clock.** Copy something distinctive, wait out
  `clip.TTL` (fifteen minutes, `internal/clip`), and press `Mod+v`. It is gone.
  Then the half that is the actual promise: it is gone from the daemon's memory
  and not only from the list.

  Copy the marker below rather than a word, and copy it with the tail past the
  200-character preview bound (`clip.PreviewMax`). That matters: pressing
  `Mod+v` hands a preview of every row to the shell and puts one in the JSON
  zded wrote, and the wipe cannot reach either of those. Grep for a marker no
  preview can contain and the answer means what it says.

  ```sh
  printf 'x%.0s' $(seq 250) > /tmp/zde-marker; printf 'ZDE-TTL-MARKER\n' >> /tmp/zde-marker
  wl-copy < /tmp/zde-marker
  pid=$(systemctl --user show -p MainPID --value zded.service)
  gcore -o /tmp/zded-live $pid && strings /tmp/zded-live.$pid | grep -c ZDE-TTL-MARKER
  # press Mod+v, look at the row, close it - then wait out the fifteen minutes
  gcore -o /tmp/zded-gone $pid && strings /tmp/zded-gone.$pid | grep -c ZDE-TTL-MARKER
  ```

  Non-zero for the first, zero for the second. Do both halves in one sitting: a
  zero that was zero all along proves nothing. Delete the cores and the marker
  file afterwards - one of those cores has your clipboard in it, which is the
  whole reason this is worth checking.

  What a stray non-zero on the second core is not, necessarily: a `[]byte` that
  grew while being read leaves its old copy in the heap until the collector
  reuses that memory, and zded's wipe reaches the entry rather than every buffer
  that ever held a prefix of it. 250 bytes fits the read buffer without growing
  it, so this particular check should be clean - but one that is not is a thing
  to look at rather than a promise broken.
- **An image, and a huge paste.** `Mod+Print` takes a screenshot and niri puts
  it on the clipboard; `Mod+v` should then show a row saying `image/png` is not
  text and that the history keeps text only, dimmed, with Enter doing nothing
  and saying why. The same for something enormous: `yes | head -c 200000 |
  wl-copy` is a row saying it was more than 64 KiB. Neither is content, and
  neither can be put back - half a file put back on the clipboard would be data
  loss that looks like a paste that worked.
- **`zde clip clear`**, which is the TTL by hand for the moment before somebody
  else looks at your screen. It says how many entries went.
- **A day of it.** Fifty entries and fifteen minutes are both guesses. Whether
  the list is ever full, and whether things expire while you still wanted them,
  are the two numbers only use can settle - and they are one line each in
  `internal/clip`.

## Expected to be missing

Not bugs, do not report them:

- **The rest of the shell**: the bar and six surfaces over it - the picker
  (desks on `Mod+Tab`, windows on `Mod+w`, one surface for both), the
  notification centre, the connections list, the palette, the ask window and the
  clipboard history. There is no mixer, no media panel, no calendar, no power
  menu, and no popup for anything: what arrives waits on `Mod+n`. The launcher
  on `Mod+g` is zinc's, not zde's.
- **Part of the cheatsheet.** A bind whose command is not written yet prints
  usage to a stderr nobody reads, so the key is silent and so is the machine.
  `Mod+semicolon` says which ones those are on the machine in front of you,
  which is the answer that comes from the build rather than from a table
  somebody kept by hand. The same split as of writing:

  | Works | Silent |
  |---|---|
  | `Mod+Tab` (the desk picker), `Mod+w` (the window one) | `Mod+Shift+Escape` (panic), `Mod+Shift+z` (zen) |
  | `Mod+j`/`k` and the arrows (nav) | `Mod+Shift+v` (pass) |
  | `Mod+Shift+j`/`k` (move window) | `Mod+Shift+t`, `Mod+Shift+e` (launch-at) |
  | `Mod+r` (regulars), `Mod+u` (queue jump) | `Mod+p`, `Mod+Shift+p`, `Mod+Ctrl+p` (media) |
  | `Mod+t` (terminal) | `Mod+m` (modes), `Mod+Shift+n` (net observer) |
  | `Mod+n` (the notification centre), `Mod+q` (quiet) | `Mod+c` (calendar), `Mod+Shift+w` (wallpapers) |
  | `Mod+semicolon` (the palette) | `Mod+Shift+x` (power) |
  | `Mod+a`, `Mod+Shift+a` (ask, once a tier is set) | `XF86AudioPlay`/`Next`/`Prev` (the media target) |
  | `Mod+Shift+c` (wifi, and the link you are on) | `Mod+e`, until `zde.apps.editor` names one (below) |
  | `Mod+v` (the clipboard history) | |
  | `Mod+g` (zinc's launcher), `Mod+Ctrl+semicolon` (lock) | |
  | `Mod+slash` (the keymap, in a pager) | |
  | `Mod+Print`, `Mod+Shift+s`, `Mod+Ctrl+w` (screenshots) | |
  | `Mod+Shift+Tab` (last desk) | |
  | `Mod+period`/`comma`, `Mod+Shift+m`, `Mod+Ctrl+m` (volume, mute, mic) | |
  | `Mod+b`, `Mod+Shift+b` (brightness, on a machine with a backlight) | |
  | `zde status`, `doctor`, `keys`, `palette`, `ask`, `attn`, `queue`/`add`/`done` | `zde net observe\|app-cut\|kill` |
  | `zde app list\|launch`, `window jump-to`, `workspace next\|prev`, `nav down\|up` | `zde desk panic\|zen\|block`, which are not verbs at all |
  | `zde net status\|connect\|disconnect\|forget` | `zde pass`, `media`, `mode` |
  | `zde clip history [ID]`, `zde clip clear` | |
  | `zde system lock\|quiet\|notif-center\|connections\|bluetooth` | `zde system power\|calendar\|wallpapers` |
  | every other `zde desk` verb: `list`, `switch`, `switcher`, `next`/`prev`/`last`, `apps`, `snapshot`, `reconcile`, `queue-jump`, `regulars`, `move-window`, `move-window-to`, `move-workspace-to` | |
  | the niri natives: columns, monitors, fullscreen, float, close, overview, consume/expel, layout switch | |

  The `zde` rows are the ones worth reading twice: the CLI is one binary with
  one dispatch, so a verb it does not know prints usage to stderr and exits, and
  from a keybind that is indistinguishable from a key that did nothing.
  `Mod+Shift+Escape` is worth singling out for the same reason: panic is the key
  you reach for first when something goes wrong, and it is one of the silent
  ones.
- **Modes** (`Mod+m`) and the leader sequences for panic and block. They wait
  on the input layer landing.
- **Brightness** (`Mod+b`, `Mod+Shift+b`) needs your user in the `video` group,
  which is what makes `brightnessctl`'s udev rules apply to the person pressing
  the key. The host template puts it there; whether the backlight then moves on
  your hardware is still one line either way.
- **The keyboard layout** is whatever niri defaults to until a host says
  otherwise, and `zde.niri.xkb.layout` and `zde.niri.xkb.options` are where it
  says so. Whether it should be taken from the system's console keymap instead
  is still open.
- **Where a desk's apps land.** With `app_id` in the manifest the window should
  open on the workspace the desk pins it to, without appearing anywhere else
  first. Two things only hands can answer: whether the app id you found with
  `niri msg windows` is the one the window actually arrives with, and what
  happens with two instances of the same app on one desk - niri matches a rule
  on the app id alone, so both windows take the first rule that fits.
- **Sandboxed apps.** `zcr`, `zc` and `zlg` are installed and rootless podman is
  running under them, so the machinery is there - but no app is defined, so
  `zlg` lists nothing and no key starts anything sandboxed. Defining one is
  zinc's `zc`. A desk does start what its manifest declares, which means a
  manifest naming apps nobody has defined starts nothing and says why in
  `journalctl --user -u zded`.
- **An editor**, and this is the one distinction in `zde.apps` worth reading
  once rather than meeting three times. That option is the seam between the
  keymap's names and this machine's programs, and three of its names have
  defaults that layer 1 also installs: `terminal` is foot, `lock` is swaylock,
  `help` is the keymap in a pager. `editor` has none. The editor zde ships is a
  zinc container (`common/apps/nvim`), which is layer 2's to build and pin, and
  a second editor on the host to cover for it would be a package nobody asked
  for. So `Mod+e` starts nothing until a machine says what an editor is, and
  what it does instead is legible: `no app called "editor"; there is [help lock
  terminal]`, written to the journal a keybind's output goes to rather than to
  the screen. `zde app list` prints the same three, which is the faster way to
  ask. One line ends it, in the flake's `home-manager.users.<name>` block:

  ```nix
  zde.apps.editor = [ "foot" "-e" "hx" ];              # a host program
  zde.apps.editor = [ "zcr" "run" "nvim" "--exec" ];   # or a sandboxed one
  ```
- **Bluetooth on a screen.** `shell/Bluetooth.qml` is a section that nothing
  instantiates yet, so no key and no surface reaches the radio: `zde system
  bluetooth` in a terminal is the whole of it (section 7).
- **A tier for ask.** There is none until a machine names one, deliberately -
  a default would be zde choosing somebody's cloud for them - so a question
  asked on a fresh install comes back with the option to set instead of an
  answer (section 8).
- **Persistence**: it is a live image. The journal, the queue and anything you
  configure are gone on reboot, and the notification history goes with the
  daemon rather than with the disk. So does the clipboard history, and that one
  is on purpose everywhere and not only here: a clipboard history on disk is a
  wallet (`internal/clip`).

## What to do with what you find

One finding, one line, with the machine it happened on. Anything in section 2
decides whether `feat/input-layer` merges as it stands; anything in section 3
decides whether the IPC reads get folded together; the rest lands as issues
against the roadmap's own verify list ([`roadmap.md`](roadmap.md)).
