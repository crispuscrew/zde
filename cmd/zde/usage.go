package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprint(os.Stderr, usageSystem+usageDesktop)
}

const usageSystem = `zde - the command line into zded

  zde status             what zded and the compositor are doing
  zde doctor             every check on one screen: the daemon, the compositor,
                         the shell, notifications, the units, the manifests and
                         whether the screen lock could accept a password.
                         Non-zero when something failed, so it is worth piping
                         into a bug report
  zde report             write down what this machine looks like: the graphics
                         device and whether niri ever reached a renderer on it,
                         the versions, the hardware, and the whole of doctor.
                         It lands in /var/log/zde, 0600, one file per boot, for
                         the failure nothing else survives - a black screen with
                         no terminal to ask anything from. Needs zde.debug on;
                         with nowhere to write it prints the report instead.
                         No notification text, no clipboard, no queue, no window
                         titles, and nothing about a desk declared private
  zde desk list          the desks that exist right now
  zde desk switcher      open the picker; prints the list when no shell is up
  zde app launch NAME    run what this machine calls that (Mod+t, Mod+e)
  zde system lock        lock the screen (Mod+Ctrl+semicolon)
  zde system lock-preset switch to the preset desk, then lock, so that what an
                         unlock shows - and what a shoulder reads at the lock
                         screen - is that desk and not what you were doing. The
                         desk is zde.lock.preset in your home-manager config.
                         With none set, one naming a desk that is gone, or one
                         naming a desk declared private, it locks where you are
                         and says which of those it was
  zde system power       the power menu (Mod+Shift+x): lock, log out, suspend,
                         reboot, power off. Prints the five when no shell is
                         up, each with what it is about to cost underneath -
                         the windows that close, what the notification center
                         is holding that the queue never got, anybody else
                         logged in here, and whatever is holding a suspend off
  zde system power NAME  run one of them, by the name in column one. The menu
                         is where the three that end things are asked about;
                         typing the word here is the answer, the same bargain
                         zde system bluetooth confirm makes. The lock runs the
                         same locker zde system lock does, and the other four
                         are logind's - so a refusal, an inhibitor holding
                         sleep or a second person logged in, comes back as a
                         refusal and not as silence
  zde system connections open the connections widget (Mod+Shift+c); prints the
                         link and what is in range when no shell is up -
                         signal, security, note, network - and says so plainly
                         on a machine with no NetworkManager
  zde net status         what the link is right now: wifi and its signal,
                         wired, nothing, no NetworkManager to ask, or cut
  zde net kill           cut every link NetworkManager holds - wired and wifi
                         together - and press it again to put them back. It is
                         NetworkManager's own networking switch, so saved
                         networks stay saved and the radios stay powered: a
                         bluetooth keyboard still works to undo it. What it
                         does not reach is anything NetworkManager does not
                         manage. While it holds, the bar and zde net status say
                         cut rather than offline
  zde net connect SSID   join a wifi network. The password is read from stdin,
                         never from the command line - anybody with an account
                         on this machine can read a running process's
                         arguments - and it is only asked for when the network
                         is secured and NetworkManager has no profile for it
  zde net forget SSID    drop what NetworkManager has saved for a network,
                         which is how a changed password gets typed again -
                         nothing asks for one while a profile is there
  zde net disconnect     drop the wifi link, keeping the saved profile
  zde app list           what it can start
  zde system bluetooth   the radio, and what is around it: the adapter, then
                         one device per line - address, flags, signal, name.
                         The flags are three positions: paired, trusted,
                         connected, a dash where it is not. Says "adapter none"
                         on a machine with no radio rather than failing
  zde system bluetooth power on|off
                         the radio itself. Off unless somebody said otherwise,
                         and nothing here turns it on as a side effect
  zde system bluetooth scan on|off
                         look for what is around, and stop again: a radio left
                         scanning is one that keeps announcing itself
  zde system bluetooth pair ADDR
                         pair, which asks before anything happens: the passkey
                         is shown here and has to match what the device shows.
                         Pairing does not trust
  zde system bluetooth confirm ID yes|no
                         answer one pairing question, by the id printed with it.
                         The id is not decoration: questions come and go on
                         their own - one expires after 45 seconds, another
                         arrives - and an answer that did not name one would be
                         spent on whichever is waiting when it lands
  zde system bluetooth connect|disconnect ADDR
                         open or drop the link to a device already paired
  zde system bluetooth trust|untrust ADDR
                         trusted means it reconnects and uses its services
                         without asking again - a decision, never a side effect
                         of having paired once
  zde system bluetooth forget ADDR
                         remove it: the key, the trust, the lot
`
