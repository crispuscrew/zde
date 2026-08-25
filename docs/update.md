# zde - Updating

How a pin moves, how it gets verified, and how a real machine takes it. The
0.3 update work (staleness and CVE collection, an agent-drafted re-pin, a human
signing it) automates the first half of this; the runbook is what it automates.

## What is pinned

Everything, in `flake.lock`. Three inputs decide what a machine runs:

| Input | Tracks | Moves when |
|---|---|---|
| `nixpkgs` | `nixos-26.05`, the current stable | you bump it |
| `home-manager` | `release-26.05`, matched to nixpkgs | with nixpkgs |
| `zinc` | a tag, `v0.10.1` | you edit the tag |

Nothing floats. `nixos-26.05` is a branch that receives backports, so the
niri in it can change within the release - but a machine only sees any of it
when the lock moves. The lock is the pin. Keep home-manager's release matched
to nixpkgs: mismatched pairs break in ways that are tedious to read.

zinc is the one pinned to a tag rather than a branch, which is what zde asks of
anybody pinning zde: an update should be a decision, and a tag is a thing to
decide about. `nix flake update zinc` on a tag re-resolves to the same commit,
so moving it means editing the URL in `flake.nix` - and in
`templates/host/flake.nix`, which is the pin a real machine actually has. It
follows this repo's nixpkgs, so a machine has one and not two.

Zinc 0.10.0 moves app definitions to schema v3. Before applying this update,
migrate every file under `~/.config/zinc/apps`: set `SchemaVersion: 3`, replace
the old audio booleans with `Playback`, `Microphone`, and `Monitor`, and express
each `Configs` entry as a `BundlePath`, `InnerMount`, and optional `Writable`.
Zinc rejects v2 and unknown keys rather than silently ignoring them; its 0.10.0
changelog has the full migration and enforcement details.

**zde does not yet offer what it asks for.** This repository has no tags, so
`templates/host/flake.nix` pins zde to the `dev` branch - named there rather
than left implicit, so it says what it is. A machine is still pinned, by its
own `flake.lock`, and still moves only when somebody runs `nix flake update
zde`; what it lacks is a version to have decided about. Cutting the first tag
is the fix, and it is one commit and one line: tag a commit of `dev` that
passes `nix flake check` and the smoke test, then change the template's `zde`
url to `github:crispuscrew/zde/vX.Y.Z`. From then on that line moves the way
zinc's does, by hand, and this section stops having an exception in it.

niri has no input of its own. Stable currently carries the release zde wants
(26.04) and builds it against the same mesa the system runs, which is what
keeps a session off a black screen. The day zde needs a niri that stable does
not carry, it goes back to being a pinned input - built with nixpkgs' own
package expression and a `src` override, not upstream's flake, which targets
nixpkgs-unstable and installs its systemd units where NixOS does not look.

## Bumping

Within the same releases, security and backport updates:

```
nix flake update                 # every input
nix flake update nixpkgs         # or just one
```

BlueZ currently has one package-level exception: `nix/bluez.nix` appends the
exact upstream AVRCP parser fix `bd898962` and its corrective follow-up
`58088149`, with content hashes, for CVE-2026-75032. Keep them ordered and
remove the override only after the pinned BlueZ source contains both commits;
the follow-up repairs a premature length read introduced by the first patch.

To a new NixOS release, twice a year: edit both URLs in `flake.nix` to the new
release (`nixos-XX.YY` and `release-XX.YY`), then `nix flake update`. Seven
other places name the release and none of them can read the flake, so they
move by hand in the same commit:

- `testedRelease` in `nix/system.nix`, what the build-time warning compares
  against. Forget this one and zde warns at every correctly pinned machine
  that *it* is wrong. `checks.zde-nixos-eval` fails if you do, which is what
  that assertion is for.
- the two input URLs in `templates/host/flake.nix`.
- `system.stateVersion` in `templates/host/configuration.nix` and
  `home.stateVersion` in `templates/host/flake.nix`. These are the exception
  to the rule below: in a template they are not a machine's creation stamp but
  what every *new* machine will be created at, and a stranger installing on
  27.05 should not start out declaring 26.05.
- `home.stateVersion` in `nix/zde-user.nix` and `system.stateVersion` in
  `nix/test-host.nix`, for the same reason and one more: those two are what
  `nix flake check`, the smoke test and the live image evaluate, so a stale
  number there means every automated check is evaluating a machine nobody has.
  It was 25.05 against a template saying 26.05 once, and the live image booted
  with one of each.
- the table at the top of this file.

Read the release notes for renames - the 25.05 to 26.05 move alone renamed
`greetd.tuigreet` to `tuigreet` and `nixfmt-rfc-style` to `nixfmt`, both of
which are eval warnings rather than errors and are easy to miss.

Do not touch `system.stateVersion` or `home.stateVersion`. They record which
release a machine was *created* at, and changing them asks for data migrations
nobody wanted.

A nixpkgs bump can move niri under you. That is what the smoke test is for:
it runs `niri validate` against the generated config with the niri that bump
brought, so a config-breaking niri release fails there rather than at a login.

## Verifying, cheapest first

```
nix flake check          # ~1 min. Both layers evaluate; the tools build.
nix build .#zde-smoke    # a few min. Boots it: greeter, units, config, podman.
```

`nix flake check` builds nothing of the system, so a machine that cannot come
up still passes it - that is the smoke test's job. It boots a host in QEMU and
checks that greetd is up, that niri's user units are where systemd reads them,
that home-manager wrote the generated config, and that niri's own parser
accepts it. It has grown well past that since: a compositor runs nested inside
it, so the test also drives the desks, the queue, a notification arriving and
being read back in the centre, a desk starting what its manifest declares, and
the bar saying it has no microphone and no NetworkManager rather than guessing
([`verify.md`](verify.md) says what it still cannot reach). It runs in CI on any
change to the system definition, so in practice you push and let it answer; run
it locally when you want it sooner.

The smoke test needs KVM. In the CI workflow a udev rule opens `/dev/kvm` to
non-root; locally, `nix build` needs the `kvm` system feature, which nix
advertises when `/dev/kvm` exists.

## Applying it to a real machine

zde is a module, not a system: your own flake imports `nixosModules.zde` and
pins zde as an input. So a machine takes an update in two steps - move the
pin, then rebuild.

```
cd /etc/nixos                           # wherever your machine's flake lives
sudo nix flake update zde
sudo nixos-rebuild switch --flake .#zdebox
```

Until zde tags a release, that middle line takes dev's head rather than a
version (What is pinned, above). Read what is between first.

zinc is a separate input in that flake, on its own tag, so the sandbox and the
desktop move independently: `sudo nix flake update zinc` after editing its tag,
applied by the same rebuild. Two decisions rather than one is deliberate - the
thing that isolates every app should not change because the bar did.

That flake is the one that matters, and not only for the disks. **A module is
evaluated by whichever nixpkgs imports it**, so the lock in this repo pins what
CI tests and says nothing about what your machine runs: import zde from a flake
on unstable and zde is built against unstable, whatever `flake.lock` here says.
`nix flake init -t github:crispuscrew/zde#host` writes a flake that starts on
the tested release with the `follows` already wired, and layer 0 warns at build
time if the release underneath it is not the one zde is tested against. It also
sets `home-manager.useGlobalPkgs`, which is what makes an overlay or a
`nixpkgs.config` in your `configuration.nix` reach layer 1's packages as well as
the system's: without it home-manager instantiates nixpkgs a second time, from
the same input, and that second one never sees your instructions.

`switch` builds the new generation, activates it, and restarts what changed.
Layer 1 comes with it: home-manager runs as part of the system generation, so
the generated `~/.config/niri/config.kdl` is replaced in the same step.

What survives and what does not:

- A running niri session is **not** restarted by `switch`. The new compositor
  is on disk, the old one keeps running until you log out. niri does reload
  its config on write, so binds change under you, in a session whose binary is
  the older one. Log out and back in after a niri bump.
- `nixos-rebuild boot` stages the generation for the next reboot instead,
  which is the honest choice for a kernel or mesa update. It really does touch
  nothing: `switch-to-configuration boot` installs the bootloader and exits
  before it looks at a single unit.
- greetd is `restartIfChanged = false` upstream, so a rebuild will not drop you
  out of a session it is holding. niri carries the same flag, for the same
  reason, in its own nixpkgs module.
- **zded is restarted**, mid-session, by any switch that changes it. It is a
  home-manager unit, and home-manager restarts its changed user units during
  activation. Most of it is safe by design - the journal is replayed on the way
  back up, which is what the desks are rebuilt from, and the notification
  history is written to a snapshot and read back - but it is a real restart: a
  `zde` running at that instant gets a closed socket rather than an answer.
- **The clipboard history does not survive it.** It is held in memory and
  nowhere else, on purpose and permanently (`internal/clip`: a clipboard
  history on disk is every password ever pasted, in one file, outliving the
  TTL that is supposed to shred it). The journal does not hold it and no
  snapshot does either, so any switch that touches the Go tree empties `Mod+v`,
  silently, mid-session. Nothing warns and nothing is wrong; it is the one
  piece of session state a rebuild costs you. Paste it somewhere first if it
  matters.
- **Nothing restarts a running app** when zinc moves. The tools on PATH are the
  new ones and the containers already up were started by the old ones; what
  changes them is stopping and starting the app.
- The portals are restarted too, but on their own schedule rather than zded's:
  they are NixOS-level user units, so they go when their own packages change,
  which is a nixpkgs bump and not a zde one. A screencast in flight dies with
  them.

To try it before committing to it:

```
sudo nixos-rebuild test --flake .#<host>    # activate, do not add to the boot menu
```

## When it goes wrong

Rollback is the reason NixOS is the reference platform:

```
sudo nixos-rebuild switch --rollback     # previous generation, now
```

or pick any older generation from the boot menu. Generations are kept until
garbage collection, and `nix profile history --profile /nix/var/nix/profiles/system`
lists them with dates.

A bad *layer 1* generation is smaller: `home-manager generations` lists them
and each one has an `activate` script that switches back, without touching
layer 0.

The failure this repo has already caught once is a session that starts and
immediately returns to the greeter. That is layer 0, and it looks like nothing
on screen, so switch to a VT (`Ctrl+Alt+F2`), log in there, and read
`journalctl --user -u niri -b`. If systemd says the unit does not exist, then
the niri build installed its user units to `share/systemd/user`, which NixOS
never globs - the smoke test asserts against exactly that.

## The portable path

On a non-NixOS host, layer 0 is `install.sh` (not written yet) and layer 1 is
home-manager on its own:

```
home-manager switch --flake .#<user>
```

Same generated config, same rollback story via `home-manager generations`. The
compositor there comes from the distro, so its version is not pinned by this
flake and the generated config is handed to whatever niri the distro ships.
