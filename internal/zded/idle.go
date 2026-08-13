package zded

import (
	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/power"
)

// The idle hold (docs/vision.md, principle 4: whatever a keypress depends on is
// on the bar).
//
// What this answers is "is something stopping this session going idle", which
// on a machine that locks on idle is "is the screen going to lock". It is here
// and not in the shell for the reason the power menu is: logind is a system bus
// this process already holds a connection to, and a QML file shelling out to
// `systemd-inhibit --list` would be parsing a table meant for people, on a
// timer, inside the process that holds the keyboard.
//
// The hard part of this method is not the reading. It is that the reading is
// half a mechanism, and the half it cannot see is the bigger one:
//
//   - logind inhibitors, What "idle", mode block. Visible here, with the name
//     and the reason the holder gave. This is what `systemd-inhibit
//     --what=idle` takes, and what a service or a login-session job would use.
//   - zwp_idle_inhibit_manager_v1, the Wayland protocol. Invisible here.
//     niri 26.04 builds that global with no security-context filter
//     (src/niri.rs, IdleInhibitManagerState::new with no client_is_unrestricted
//     where thirteen other sensitive globals take one), so any sandboxed app
//     can take one; niri honours it while the surface is merely visible rather
//     than focused; and niri keeps the result in a field with no way out - no
//     IPC request, no event, and an org.freedesktop.ScreenSaver with Inhibit
//     and no getter. Nothing zde can call returns it.
//
// The two do not overlap. A client holding a Wayland inhibitor on a mapped
// surface changes nothing in ListInhibitors and nothing in the session's
// IdleHint - measured on a live session, and true by construction in niri's own
// source, where the computed bool goes to the idle notifier and nowhere else.
//
// So `known` here means "logind answered", never "nothing is holding the screen
// awake", and every place this is drawn says so: the bar keeps a word for what
// it can see and nothing for what it cannot, `zde doctor` spells the gap out in
// full, and docs/verify.md asks somebody to go and confirm the invisible half
// by hand with a real application, because that is the kind of thing only a
// session settles.

// Idle is what `system.idle` answers.
//
// Known and Holds are two facts and not one, which is the same bargain the bar
// makes everywhere else (shell.qml, netState.known): "logind did not answer" and
// "logind answered, nothing is holding it" are different, and a strip that drew
// them the same way would say the screen is fine on a machine that has lost its
// system bus.
type Idle struct {
	// Known is whether logind answered at all. False means the question was not
	// put or came back as an error - not that the answer was no.
	Known bool `json:"known"`
	// Holds is what logind says is holding idle off, and never the Wayland
	// inhibitors, which nothing on this machine can enumerate. Empty with Known
	// true is "logind sees nothing", which is a smaller claim than "nothing".
	Holds []IdleHold `json:"holds,omitempty"`
	// Why is why logind could not say, when it could not. Carried so the surface
	// and `zde system idle` give the same reason rather than each inventing one.
	Why string `json:"why,omitempty"`
}

// IdleHold is one holder, in the words it gave logind.
//
// And those words are anybody's: `systemd-inhibit --who=... --why=...` takes
// two strings from whoever runs it and every local account can run it, so these
// are the same kind of thing as a notification's summary and get the same
// filter (see idleHolds).
type IdleHold struct {
	Who string `json:"who"`
	Why string `json:"why,omitempty"`
}

// idleHold is `system.idle`. It never fails: a machine with no logind is a
// state to be drawn, not an error to be shown, because the bar asks this on a
// timer and a refusal every five seconds is a refusal nobody reads.
func (s *Server) idleHold() Response {
	st, err := s.powerState()
	if err != nil {
		return ok(Idle{Why: err.Error()})
	}
	return ok(Idle{Known: true, Holds: idleHolds(st.HoldingIdle())})
}

// idleHolds is the rows, filtered.
//
// Filtered here rather than in internal/power for the reason the power menu's
// cost lines are (power.go, held): that package is the logind client and has no
// business knowing where a string is about to be drawn. This one goes to a Qt
// Text on the layer-shell bar and to a terminal for `zde system idle`, and
// `--why="$(printf '\033[2J')"` would clear the second one.
func idleHolds(blocks []power.Block) []IdleHold {
	var out []IdleHold
	for _, b := range blocks {
		who := attn.Line(b.Who)
		if who == "" {
			// Never an empty row: the count is what the bar draws, so a holder
			// whose name filtered away to nothing still has to be one holder.
			who = "something on this machine"
		}
		out = append(out, IdleHold{Who: who, Why: attn.Line(b.Why)})
	}
	return out
}
