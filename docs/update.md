# zde - Updating

How a pin moves, how it gets verified, and how a real machine takes it. The
0.3 update work (staleness and CVE collection, an agent-drafted re-pin, a human
signing it) automates the first half of this; the runbook is what it automates.

## What is pinned

Everything, in `flake.lock`. Two inputs decide what a machine runs:

| Input | Tracks | Moves when |
|---|---|---|
| `nixpkgs` | `nixos-26.05`, the current stable | you bump it |
| `home-manager` | `release-26.05`, matched to nixpkgs | with nixpkgs |

Nothing floats. `nixos-26.05` is a branch that receives backports, so the
niri in it can change within the release - but a machine only sees any of it
when the lock moves. The lock is the pin. Keep home-manager's release matched
to nixpkgs: mismatched pairs break in ways that are tedious to read.

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

To a new NixOS release, twice a year: edit both URLs in `flake.nix` to the new
release (`nixos-XX.YY` and `release-XX.YY`), then `nix flake update`. Five
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
accepts it. It runs in CI on any change to the system definition, so in
practice you push and let it answer; run it locally when you want it sooner.

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

That flake is the one that matters, and not only for the disks. **A module is
evaluated by whichever nixpkgs imports it**, so the lock in this repo pins what
CI tests and says nothing about what your machine runs: import zde from a flake
on unstable and zde is built against unstable, whatever `flake.lock` here says.
`nix flake init -t github:crispuscrew/zde#host` writes a flake that starts on
the tested release with the `follows` already wired, and layer 0 warns at build
time if the release underneath it is not the one zde is tested against.

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
  activation. This is safe by design - the journal is replayed on the way back
  up, which is what the desks are rebuilt from - but it is a real restart: a
  `zde` running at that instant gets a closed socket rather than an answer.
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
