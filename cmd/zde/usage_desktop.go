package main

const usageDesktop = `  zde window jump-to [ID]
                         open the window picker (Mod+w); prints the list when
                         no shell is up - id, workspace, app, title - and with
                         an id goes straight to that window, desk and all
  zde ask oneshot [QUESTION]
                         the quick LLM (Mod+a). With a question typed here the
                         answer arrives here, streamed as it comes; without one
                         it opens the popup, and says so when no shell can
  zde ask panel [QUESTION]
                         the window that stays open to keep asking
                         (Mod+Shift+a), carrying what was asked in it into the
                         next question. With a question it opens the panel with
                         that one asked and nothing in front of it, so the
                         answer arrives in the window and not here; with no
                         shell to draw one, nothing is asked and it says so.
                         Use oneshot for an answer on stdout
  zde ask local QUESTION the private tier, which is the one that runs with no
                         network
  zde ask escalate QUESTION
                         the tier kept for a question the first two got wrong.
                         Every tier is a command this machine was configured
                         with (zde.ask.tiers), and an unset one is unset:
                         nothing here talks to anybody's API.
                         A question written as an argument is visible in ps for
                         as long as the answer takes, so all four of these read
                         it from stdin when it is piped in instead:
                         zde ask local < the-question
  zde clip history [ID]  what was copied recently (Mod+v); prints the rows when
                         no shell is up - id, when, what it is, and the text -
                         and with an id puts that entry back on the clipboard.
                         Text only, in memory only, and every entry expires:
                         nothing an app marked as a secret is ever recorded, and
                         an image or a file is a row saying so rather than
                         content
  zde clip clear         forget the history now rather than when it expires
  zde keys               the whole keymap, one key per line (Mod+slash opens
                         this in a terminal)
  zde palette [NAME]     every action by name (Mod+semicolon); prints the list
                         when no shell is up - name, key, why it would do
                         nothing, what it does - and with a name runs that one,
                         doing exactly what its key would do
  zde desk switch NAME   bring a desk up on every monitor it owns, and start
                         what its manifest declares
  zde workspace next|prev
                         one along this desk's band, stopping at its ends
  zde nav down|up        the window along the stack, else the desk beside this
  zde desk move-window next|prev
                         carry the focused window to the desk beside, and go
  zde desk move-window-to [NAME]
                         carry the focused window to that desk (Mod+Ctrl+Tab).
                         With no name it opens the desk picker and the row you
                         choose is the destination; prints the desks when no
                         shell is up, and the name goes here
  zde desk move-workspace-to [NAME]
                         hand this whole workspace to that desk, which is how
                         the regulars are made and how work comes back out
                         (Mod+Ctrl+Shift+Tab). The same two forms, and the
                         picker offers the regulars whether or not the band
                         exists yet - it comes into being by being chosen
  zde desk next          the desk after this one, wrapping (regulars excluded)
  zde desk prev          the desk before this one, wrapping
  zde queue              what is waiting, oldest first
                         (id, urgency, * for the desktop's own, desk, sender,
                         text - tab separated)
  zde queue add TEXT     make something wait, on the desk you are on
  zde queue done ID      it is not waiting any more
  zde queue clear        none of it is waiting any more; prints how many went
  zde attn [MODE]        the attn mode, or set it: work queues everything,
                         focus queues only what the sender called urgent,
                         quiet queues none of it. All three keep the lot in
                         the notification center, so a mode changes what
                         interrupts you and never what happened
  zde system quiet       toggle quiet (Mod+q), which is the same mode by the
                         key you reach for when you need silence now
  zde system notif-center
                         what arrived (Mod+n); prints the history when no
                         shell is up - id, urgency, * for the desktop's own,
                         when, sender, whether it is waiting, done or silent,
                         and the text
  zde system notif-reach put the keyboard on the newest popup (Mod+Ctrl+n), so
                         its sender's buttons can be pressed. A popup never
                         takes the keyboard on its own, which is why this key
                         exists; says so when there is no popup to reach
  zde desk zen [STATE]   toggle zen (Mod+Shift+z): the bar goes, and so do
                         niri's borders, focus ring and gaps, on every screen.
                         on, off or toggle to say which rather than flip. It
                         hides chrome and nothing else - notifications arrive
                         as they would have, the lock and panic keys are
                         untouched, and the bar comes back by itself while
                         something is holding the microphone
  zde desk queue-jump    go to where the oldest thing waiting is
  zde desk regulars      the band that belongs to no desk (comms, music)
  zde desk last          go back to the desk you came from
  zde desk panic         hide (Mod+Shift+Escape): switch to the decoy desk, mute
                         the output, and let nothing interrupt. The decoy is
                         zde.panic.decoy in your home-manager config, and with
                         none set - or one naming a desk that is gone or one
                         declared private - it changes nothing and says which of
                         those it was. The same verb again comes back: your
                         desk, the sound as it was, the mode as it was. It hides
                         a screen and forgets nothing: what arrived while it
                         held is in the notification center afterwards
  zde desk reconcile     make the workspace names true again
  zde desk snapshot [N]  write down the desk you are on, so you can ask for it
  zde desk apps [NAME]   what a desk declares: the address of each app, where
                         it goes, and where zinc says its state lives

Updating zde, which is two deliberate steps and nothing automatic
(docs/update.md). Use your own host name from your flake:

  cd /etc/nixos
  sudo nix flake update zde
  sudo nixos-rebuild switch --flake .#zdebox

The sandbox is its own pin in that flake: edit the zinc tag, then
nix flake update zinc, then rebuild. Two decisions rather than one, because the
thing that isolates every app should not move because the bar did.

Use boot rather than switch for a kernel or mesa change, and log out after a
niri one: a running compositor is not replaced by a rebuild, and its config is.
If the new one is worse, sudo nixos-rebuild switch --rollback, or pick the
generation above the newest in the boot menu.

Most of the action map (docs/model.md, section 6) is not wired yet; the
generated keybinds that call it land with roadmap 0.1.
`
