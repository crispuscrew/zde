package keymap

// Entry pins what one action means for niri. Exactly one of Native (a niri
// action line) or Spawn (an argv) is set; argPlaceholder marks where a
// parametric argument lands in a Native template. The registry is code on
// purpose: the YAML assigns chords and nothing else.
//
// TestRegistryInvariants holds all of that, since none of it is expressible in
// the type and every violation is silent in the generated config.
type Entry struct {
	Group  string
	Desc   string
	Native string
	Spawn  []string
	Arg    argKind

	// Repeat says whether holding the key re-fires the action. niri's own
	// default is true, which is right for navigating and resizing and wrong for
	// everything that opens something: held, Mod+t launches a terminal per
	// key-repeat tick. So the default here is the opposite of niri's, and the
	// entries that are meant to be held say so.
	Repeat bool

	// WhenLocked keeps the bind working on the lock screen. It is what makes
	// volume, mic-mute, brightness and media transport usable while locked,
	// and it must stay off for everything else - a bind that reaches past the
	// lock screen is a way around it.
	WhenLocked bool

	// Unsuppressible writes allow-inhibiting=false, which is what keeps a bind
	// working while the focused application is holding a keyboard-shortcuts
	// inhibitor.
	//
	// The hole it closes is niri's. zwp_keyboard_shortcuts_inhibit_manager_v1
	// is one of the two sensitive globals niri 26.04 does not gate on the
	// security context every zinc app arrives through: in src/niri.rs the layer
	// shell, the session lock, screencopy, both data-control protocols, the
	// virtual keyboard and pointer and five more - twelve, which is every call
	// site grep for client_is_unrestricted finds beside the definition - are
	// built with that filter, thirteen counting the gamma control, which
	// inlines the same restricted bit rather than naming the function. Both
	// KeyboardShortcutsInhibitState::new and IdleInhibitManagerState::new are
	// built without one, and the second costs a screen rather than a keyboard
	// (internal/zded, Idle; docs/verify.md, section 11). A new inhibitor is
	// then activated on the spot, with no dialog and nobody asked - niri's own
	// FIXME in src/handlers/mod.rs says the confirmation is missing. While that
	// surface has the keyboard, niri forwards rather than acts on every bind
	// whose allow-inhibiting is true (src/input/mod.rs, should_intercept_key),
	// and true is what a bind gets when it says nothing
	// (niri-config/src/binds.rs). So one Wayland global, bound by any
	// container, takes every key on this machine.
	//
	// Deliberately not the whole keymap. A bind marked here is a chord no
	// application can ever receive, and that protocol exists for the ones that
	// have a real claim on a chord: a VM, a nested compositor, a remote desktop,
	// a browser holding the keyboard lock. So the set is the exceptions
	// docs/vision.md, principle 2 already names - panic, lock, mode exit - plus
	// the key that hands the rest back, and each entry below says beside itself
	// why it is in. Everything else is a key an app may take, which is the
	// trade, and it is not free in either direction.
	//
	// The set is enumerated in TestTheKeysAnAppCannotSuppress, so widening it
	// takes two edits and a reason written down.
	Unsuppressible bool

	// written says somebody has written the command this action spawns. Most of
	// the keymap has none: the bind spawns `zde`, which does not know that verb,
	// prints usage to a stderr no keypress has and exits - a key that does
	// nothing, silently (README, Missing). The palette lists those anyway and
	// says so on the row, which is the difference between a key that teaches
	// what is coming and one that is merely dead.
	//
	// Lowercase because this file is the only place allowed to say it, and off
	// by default so that an action added without a thought about it reads as
	// silent rather than as a promise. A niri native needs none: niri wrote it.
	// Whether the program is on this machine is a different question, asked
	// where the palette is built (internal/zded, whyNot), and this one is
	// pinned against the only thing that decides it - `zde`'s own dispatch -
	// by TestLiveActionsAreTheOnesZdeKnows (cmd/zde).
	written bool

	// performs says niri will do this action when zde asks for it over the IPC
	// socket, and not only when the bind fires. It is the palette's licence to
	// run a native row (internal/niri, Perform).
	//
	// Two reasons a native does not have it, both niri's rather than ours. An
	// action whose line carries an argument - set-column-width "-10%" - takes
	// that argument on the wire as a niri type and not as the string the KDL
	// spells, so zde asking for one would be guessing at another project's
	// protocol. And the three screenshots take a field that has no default over
	// IPC, where niri's own config parser supplies one: asked as a bare action
	// niri 26.04 answers "error parsing request" to each of them, while the key
	// works. That asymmetry is the exact thing this palette exists to expose, so
	// it must not be the palette's own bug.
	//
	// Opt-in, because the two failures are not the same size. A native wrongly
	// marked here is a row the palette offers and niri refuses; a native wrongly
	// left unmarked is a row that says press the key, which is true. The set is
	// enumerated in TestOnlyTheNativesNiriTakesOverIPCAreMarked, so a new one
	// cannot arrive without somebody deciding.
	performs bool
}

type argKind int

const (
	argNone argKind = iota
	argName
)

// argPlaceholder is where a parametric argument lands in a Native template.
// Deliberately not a printf verb: niri action lines carry literal percent
// signs.
const argPlaceholder = "{arg}"

func (e Entry) parametric() bool { return e.Arg != argNone }

// groups fixes the cheatsheet order; same order as the action map
// (docs/model.md, section 6).
var groups = []string{
	"desk",
	"monitor",
	"workspace",
	"window",
	"launch",
	"ask",
	"pass",
	"clip",
	"capture",
	"media",
	"audio",
	"net",
	"modes",
	"system",
}

var registry = map[string]Entry{
	// desk: zde-level, everything goes through the zde CLI (zded's client).
	// The vertical axis (see keymap.yaml) navigates desks; horizontal is
	// windows. nav.down/up unify the two - focus a stacked window first, then
	// rotate the desk at the column's edge. switcher opens the chooser, last
	// toggles to the last-active desk.
	"desk.switcher":         {Group: "desk", Desc: "open the desk switcher", Spawn: []string{"zde", "desk", "switcher"}, written: true},
	"desk.next":             {Group: "desk", Desc: "switch to the next desk", Spawn: []string{"zde", "desk", "next"}, written: true},
	"desk.prev":             {Group: "desk", Desc: "switch to the previous desk", Spawn: []string{"zde", "desk", "prev"}, written: true},
	"desk.last":             {Group: "desk", Desc: "toggle to the last-active desk", Spawn: []string{"zde", "desk", "last"}, written: true},
	"nav.down":              {Group: "desk", Desc: "focus the window below, else rotate to the next desk", Spawn: []string{"zde", "nav", "down"}, written: true},
	"nav.up":                {Group: "desk", Desc: "focus the window above, else rotate to the previous desk", Spawn: []string{"zde", "nav", "up"}, written: true},
	"desk.move-window-next": {Group: "desk", Desc: "move the window to the next desk", Spawn: []string{"zde", "desk", "move-window", "next"}, written: true},
	"desk.move-window-prev": {Group: "desk", Desc: "move the window to the previous desk", Spawn: []string{"zde", "desk", "move-window", "prev"}, written: true},
	// The two that say which desk outright, rather than counting to it. They are
	// how a window reaches a desk that is not beside this one, and
	// move-workspace-to is the only way the regulars come into being at all
	// (docs/roadmap.md) - which until now was a sentence about a terminal,
	// because neither had a row here.
	//
	// Deliberately not parametric. app.launch takes its argument in the keymap
	// because "terminal" and "editor" are names this repo ships and every machine
	// has; a desk name is the person's own, so there is nothing a shipped keymap
	// could write after the verb. The bind spawns it bare, which opens the same
	// picker Mod+Tab opens - the surface that already exists for naming a desk
	// with the keyboard - and the row chosen there is the argument (internal/zded,
	// deskPicker). That also keeps one row in the palette per verb instead of one
	// per desk somebody happens to have.
	"desk.move-window-to":    {Group: "desk", Desc: "pick a desk, and send the focused window there", Spawn: []string{"zde", "desk", "move-window-to"}, written: true},
	"desk.move-workspace-to": {Group: "desk", Desc: "pick a desk, and hand it this whole workspace (how the regulars are made)", Spawn: []string{"zde", "desk", "move-workspace-to"}, written: true},
	"desk.queue-jump":        {Group: "desk", Desc: "jump to the queue's top item", Spawn: []string{"zde", "desk", "queue-jump"}, written: true},
	"desk.regulars":          {Group: "desk", Desc: "go to the regulars (shared singletons: comms, music, personal browser)", Spawn: []string{"zde", "desk", "regulars"}, written: true},
	// panic and block are Unsuppressible because they are the two keys whose
	// whole job is to work when something on the screen is wrong. panic is the
	// vision's first named exception; block is the same reflex one notch harder
	// (a hard lock), so it goes in beside it rather than waiting for the day it
	// gets a chord - unbound, it costs nothing now and is already right then.
	//
	// panic is one key both ways, like the kill switch: the decoy desk, the
	// output muted and nothing allowed to interrupt, and the same key gives all
	// three back (internal/zded, deskPanic). Pressed anywhere but on the decoy
	// it hides again rather than putting the work back, which is the one thing
	// this key must never do by accident.
	"desk.panic": {Group: "desk", Desc: "panic: the decoy desk, mute, silence - and the same key comes back", Spawn: []string{"zde", "desk", "panic"}, written: true, Unsuppressible: true},
	"desk.block": {Group: "desk", Desc: "block: hard lock, no notifications or capture leak", Spawn: []string{"zde", "desk", "block"}, Unsuppressible: true},
	// zen hides chrome and nothing else, so it is deliberately not
	// Unsuppressible and deliberately not WhenLocked: it is the comfort toggle
	// beside two keys whose job is to work when something is wrong, and a key
	// that only makes a screen quieter has no claim on either exception.
	"desk.zen": {Group: "desk", Desc: "toggle zen: no bar, no borders, no gaps", Spawn: []string{"zde", "desk", "zen"}, written: true},
	// pause is 0.3's, with the background policies and the per-desk cost widgets
	// (docs/roadmap.md, resources), and nothing in this tree pauses anything: the
	// manifest's `background: pause` is parsed and acted on by nobody
	// (docs/model.md, section 5). Registered unwritten and unbound, which is what
	// the rest of this list does with an action that is coming: the palette
	// carries the row and says nothing is written behind it, and no key is spent
	// on it until something is.
	//
	// `guest <name>` from the same table is deliberately not here. It carries a
	// desk name, and a parametric entry is listed only where a keymap fills the
	// argument in (actions.go, Actions) - so an unbound one would be a name in
	// this file that no surface can ever show and no key can reach. It is also
	// the one row in the desk group that is a security posture rather than a verb
	// (docs/glossary.md: an unlock limited to one desk), and 0.2 is where that set
	// is decided. Nothing to register until it is.
	"desk.pause": {Group: "desk", Desc: "pause this desk's apps, freeing what they hold", Spawn: []string{"zde", "desk", "pause"}},

	// monitor: niri natives. The window axis owns h/l, so monitors get their
	// own pair of chords (keymap.yaml).
	"monitor.focus left":        {Group: "monitor", Desc: "focus the monitor to the left", Native: "focus-monitor-left", performs: true},
	"monitor.focus right":       {Group: "monitor", Desc: "focus the monitor to the right", Native: "focus-monitor-right", performs: true},
	"monitor.move-window left":  {Group: "monitor", Desc: "move the window one monitor left", Native: "move-window-to-monitor-left", performs: true},
	"monitor.move-window right": {Group: "monitor", Desc: "move the window one monitor right", Native: "move-window-to-monitor-right", performs: true},

	// window: niri natives except jump-to, which travels the whole hierarchy.
	// Horizontal grid: plain focus, Shift moves, Ctrl resizes. Vertical focus
	// lives on nav.down/up (desk group). consume/expel stack a column: i = in,
	// o = out.
	"window.focus left":  {Group: "window", Desc: "focus the column to the left", Native: "focus-column-left", Repeat: true, performs: true},
	"window.focus right": {Group: "window", Desc: "focus the column to the right", Native: "focus-column-right", Repeat: true, performs: true},
	"window.move left":   {Group: "window", Desc: "move the column left", Native: "move-column-left", Repeat: true, performs: true},
	"window.move right":  {Group: "window", Desc: "move the column right", Native: "move-column-right", Repeat: true, performs: true},
	"window.narrower":    {Group: "window", Desc: "make the column narrower", Native: "set-column-width \"-10%\"", Repeat: true},
	"window.wider":       {Group: "window", Desc: "make the column wider", Native: "set-column-width \"+10%\"", Repeat: true},
	"window.shorter":     {Group: "window", Desc: "make the window shorter", Native: "set-window-height \"-10%\"", Repeat: true},
	"window.taller":      {Group: "window", Desc: "make the window taller", Native: "set-window-height \"+10%\"", Repeat: true},
	"window.fullscreen":  {Group: "window", Desc: "fullscreen", Native: "fullscreen-window", performs: true},
	"window.float":       {Group: "window", Desc: "toggle floating", Native: "toggle-window-floating", performs: true},
	"window.close":       {Group: "window", Desc: "close the window", Native: "close-window", performs: true},
	"window.consume":     {Group: "window", Desc: "consume: pull the next window into this column (in)", Native: "consume-window-into-column", performs: true},
	"window.expel":       {Group: "window", Desc: "expel: push the window out of its column (out)", Native: "expel-window-from-column", performs: true},
	"window.jump-to":     {Group: "window", Desc: "jump to any open window by name", Spawn: []string{"zde", "window", "jump-to"}, written: true},

	// workspace: numbered switching is gone (desks replace it); overview zooms
	// out to the whole band.
	//
	// next and prev are zde's, not niri's, because niri's own would scroll
	// out of the desk and into somebody else's workspaces. Clamping to the
	// band is invariant 4, and it is the difference between a strip you scroll
	// and a desk you leave on purpose.
	"workspace.next":     {Group: "workspace", Desc: "one along this desk's band, stopping at its end", Spawn: []string{"zde", "workspace", "next"}, written: true},
	"workspace.prev":     {Group: "workspace", Desc: "one back along this desk's band, stopping at its end", Spawn: []string{"zde", "workspace", "prev"}, written: true},
	"workspace.overview": {Group: "workspace", Desc: "toggle the overview (zoom out to all workspaces)", Native: "toggle-overview", performs: true},

	// launch: zlg is zinc's launcher; the rest goes through zde. "terminal"
	// and "editor" are logical names zde resolves to the configured zinc apps
	// ($EDITOR is whichever editor app you set, not a hardcoded one).
	// launch-at prompts for a directory; launch opens with ambient context.
	"launcher.open": {Group: "launch", Desc: "zlg, the app launcher", Spawn: []string{"zlg"}, written: true},
	"palette.open":  {Group: "launch", Desc: "search and run any action by name", Spawn: []string{"zde", "palette"}, written: true},
	"app.launch":    {Group: "launch", Desc: "launch", Spawn: []string{"zde", "app", "launch"}, Arg: argName, written: true},
	"app.launch-at": {Group: "launch", Desc: "prompt for a location, then launch", Spawn: []string{"zde", "app", "launch-at"}, Arg: argName},

	// ask: the quick LLM.
	"ask.oneshot": {Group: "ask", Desc: "one-shot question popup", Spawn: []string{"zde", "ask", "oneshot"}, written: true},
	"ask.panel":   {Group: "ask", Desc: "the ask panel", Spawn: []string{"zde", "ask", "panel"}, written: true},

	// pass: the trusted secrets window - the safe path for passwords. Types
	// into the focused field over the clipboard's dead body (never touches
	// it). This is clip's secure sibling.
	//
	// Silent, and it stays silent while the derivation behind it is written:
	// `zde pass get` derives a password today (internal/pass), and `zde pass`
	// on its own is this window, which needs a compositor block-out and an
	// exclusive keyboard grab that nothing has yet. A key that opened a window
	// which cannot be blocked out of a screencast would be worse than a key
	// that does nothing.
	"pass.open": {Group: "pass", Desc: "open zde-pass (types a secret into the focused field, never via the clipboard)", Spawn: []string{"zde", "pass"}},

	// clip. Text only, in memory only, expiring, and nothing a password manager
	// marks as a secret is ever in it (internal/clip). `clip.clear` is in the
	// action map (docs/model.md, section 6) and is a CLI verb rather than a row
	// here: no key is free for it, and a palette row for "forget the clipboard"
	// beside the one that opens it is a row somebody presses by mistake once.
	"clip.history": {Group: "clip", Desc: "what was copied recently, and Enter puts one back", Spawn: []string{"zde", "clip", "history"}, written: true},

	// capture: screenshots and the replay clip.
	//
	// The screenshots are niri's own. They were `zde capture ...` here, which
	// is a command nothing has written, so three bound keys did nothing at all
	// while the compositor underneath them had been taking screenshots the
	// whole time. What zde would add - a send-to, a desk-aware filename, the
	// replay clip - is 0.2 work, and none of it is a reason for Print to be
	// dead until then. The day zde owns capture, these go back to being
	// spawns and the keys keep working across the change.
	//
	// niri's own defaults decide where a shot lands (screenshot-path) and put
	// it on the clipboard either way, which is the behaviour anybody who has
	// used niri already expects.
	//
	// None of the three is marked performs, and they are the only natives with
	// no argument that are not. Over the IPC socket each takes a field that has
	// no default and the bind does not carry, where the config parser fills it
	// in - so niri 26.04 answers "error parsing request" to a bare Screenshot
	// while Print works. The palette says so and points at the key.
	"capture.shot-region": {Group: "capture", Desc: "screenshot a region (niri's picker)", Native: "screenshot"},
	"capture.shot-window": {Group: "capture", Desc: "screenshot the focused window", Native: "screenshot-window"},
	"capture.shot-full":   {Group: "capture", Desc: "screenshot the whole output", Native: "screenshot-screen"},

	// media: through zde so the media target decides who plays
	// (docs/vision.md, media targeting). The target auto-routes to the
	// most-recent player by default; the picker pins or re-enables auto.
	"media.play-pause": {Group: "media", Desc: "play/pause the media target", Spawn: []string{"zde", "media", "play-pause"}, WhenLocked: true},
	"media.next":       {Group: "media", Desc: "next track on the media target", Spawn: []string{"zde", "media", "next"}, WhenLocked: true},
	"media.prev":       {Group: "media", Desc: "previous track on the media target", Spawn: []string{"zde", "media", "prev"}, WhenLocked: true},
	"media.panel":      {Group: "media", Desc: "the music control panel", Spawn: []string{"zde", "media", "panel"}},
	"media.like":       {Group: "media", Desc: "like the current track", Spawn: []string{"zde", "media", "like"}},
	"media.target":     {Group: "media", Desc: "the media target picker (pin a player, or auto-route)", Spawn: []string{"zde", "media", "target"}},

	// audio: raw wpctl until zde audio lands; the OSD comes with the shell.
	"audio.vol-up":   {Group: "audio", Desc: "volume up", Spawn: []string{"wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%+"}, Repeat: true, WhenLocked: true, written: true},
	"audio.vol-down": {Group: "audio", Desc: "volume down", Spawn: []string{"wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%-"}, Repeat: true, WhenLocked: true, written: true},
	"audio.mute":     {Group: "audio", Desc: "mute the output", Spawn: []string{"wpctl", "set-mute", "@DEFAULT_AUDIO_SINK@", "toggle"}, WhenLocked: true, written: true},
	"audio.mic-mute": {Group: "audio", Desc: "mute the mic", Spawn: []string{"wpctl", "set-mute", "@DEFAULT_AUDIO_SOURCE@", "toggle"}, WhenLocked: true, written: true},

	// net: the observer widget does per-app cuts (0.3, and unwritten); the two
	// quick-cut sequences (keymap.yaml) wait for the input daemon. The kill
	// switch is written and is a palette row rather than a chord.
	"net.observe": {Group: "net", Desc: "the network observer widget (per-app connections; cut from here)", Spawn: []string{"zde", "net", "observe"}},
	"net.app-cut": {Group: "net", Desc: "cut the focused app's network", Spawn: []string{"zde", "net", "app-cut"}},
	// The kill switch, and it is one key both ways: NetworkManager's networking
	// switch off, and the same action back on. A cut that could only be undone
	// from another machine is a way to end a session rather than to protect one
	// (internal/link, Kill). Still unbound - the two quick-cut sequences above
	// wait for the input daemon - and reachable by name from the palette.
	"net.kill": {Group: "net", Desc: "cut every link NetworkManager holds, and put it back (the radios stay on)", Spawn: []string{"zde", "net", "kill"}, written: true},

	// modes: not wired yet - the mode mechanism is a verify item
	// (docs/roadmap.md). menu is the entry point (pick a mode); the rest are
	// what it invokes. Registered so it just works once the daemon lands.
	//
	// All five are Unsuppressible, and that is the vision's third exception -
	// mode exit - read the only way it can be. There is no exit action: leaving
	// a mode is opening the picker or entering another one, so the exception is
	// either this whole group or nothing. Passthrough is the case that decides
	// it. It is the mode that hands the keyboard to the application on purpose,
	// which makes the way back out the one key in the group that must not be
	// takeable by the thing it was handed to. Only modes.menu has a chord today,
	// so the cost of the other four is a chord nobody can lose.
	"modes.menu":        {Group: "modes", Desc: "open the mode picker (Window / Kb-mouse / One-hand / Passthrough)", Spawn: []string{"zde", "mode", "menu"}, Unsuppressible: true},
	"modes.window":      {Group: "modes", Desc: "enter Window mode", Spawn: []string{"zde", "mode", "window"}, Unsuppressible: true},
	"modes.kb-mouse":    {Group: "modes", Desc: "enter Keyboard-mouse mode", Spawn: []string{"zde", "mode", "kb-mouse"}, Unsuppressible: true},
	"modes.one-hand":    {Group: "modes", Desc: "enter One-hand mode", Spawn: []string{"zde", "mode", "one-hand"}, Unsuppressible: true},
	"modes.passthrough": {Group: "modes", Desc: "enter Passthrough mode", Spawn: []string{"zde", "mode", "passthrough"}, Unsuppressible: true},

	// system: raw tools until zde grows its own (brightnessctl now; layout
	// switch is a niri native). The rest routes through zde.
	//
	// lock is the vision's second named exception, and the one whose failure is
	// worst: a screen that will not lock is a machine somebody walks up to.
	//
	// It is the dedicated chord that carries this, not Mod+Tab-then-l. That
	// route is the one the keymap tells people to use and it goes through
	// desk.switcher, which is left suppressible on purpose: it is also the
	// desk picker, one of the most-pressed keys on the board, and a chord no
	// application may ever have. Lock stays reachable while a grab is on
	// because this bind exists, so the awkward chord is the one that has to
	// work and the frequent one does not.
	"system.lock": {Group: "system", Desc: "lock the screen", Spawn: []string{"zde", "system", "lock"}, written: true, Unsuppressible: true},
	// The same lock, over a desk chosen in advance: what is on the screen when
	// the lock takes it is what a shoulder reads and what an unlock puts back
	// (docs/vision.md, W23). Unsuppressible for the reason lock is - it is the
	// same act - and unbound, so that costs nothing now and is already right
	// the day it gets a chord, which is what this file does with desk.block.
	"system.lock-preset": {Group: "system", Desc: "switch to the preset desk, then lock, so an unlock shows that desk and not your work", Spawn: []string{"zde", "system", "lock-preset"}, written: true, Unsuppressible: true},
	"system.quiet":       {Group: "system", Desc: "toggle quiet (do not disturb)", Spawn: []string{"zde", "system", "quiet"}, written: true},
	// The way back, and the general answer the named exceptions are not: it
	// turns the focused surface's inhibitor off, so every other zde key works
	// again, and a second press hands them back. Without it the protected set is
	// a list somebody has to have guessed right, and an app that grabs the
	// keyboard costs you the whole keymap until you kill it or switch VT.
	//
	// Unsuppressible for the obvious reason - a key that undoes a grab is
	// useless if the grab takes it - and niri agrees hard enough to enforce it:
	// its parser sets allow_inhibiting=false on this action whatever the config
	// says (niri-config/src/binds.rs, "the toggle-inhibit action must always be
	// uninhibitable"). Written here anyway, because the property being niri's
	// job is not a thing zde should have to remember it is relying on.
	"system.shortcut-grab": {Group: "system", Desc: "toggle the focused app's keyboard grab: take zde's keys back from it, or hand them over", Native: "toggle-keyboard-shortcuts-inhibit", Unsuppressible: true, performs: true},
	// power is one surface and five verbs, and the lock among them is the same
	// locker the key above runs: the row spawns `zde system lock` rather than
	// growing a second idea of what locks this screen (internal/zded, powerRun).
	"system.power": {Group: "system", Desc: "the power menu: lock, log out, suspend, reboot, power off", Spawn: []string{"zde", "system", "power"}, written: true},
	// connections: the wifi networks and the link you are on. The description
	// promised bluetooth too, and a cheatsheet line is a promise a person reads
	// before pressing the key - pairing is a conversation of its own with its
	// own daemon, and it is not on this key yet.
	"system.connections": {Group: "system", Desc: "the wifi networks, and the link you are on (no bluetooth yet)", Spawn: []string{"zde", "system", "connections"}, written: true},
	// bluetooth on its own, and deliberately unbound (common/keymap/keymap.yaml
	// binds nothing to it): Mod+Shift+c is system.connections, and a second
	// chord for half of one surface is a key to remember for no reason.
	// Registered so the palette can run it by name, and so the verb has an
	// action the day the connections surface has a bluetooth section in it.
	"system.bluetooth":  {Group: "system", Desc: "bluetooth: what is around, pairing, and what is connected", Spawn: []string{"zde", "system", "bluetooth"}, written: true},
	"system.calendar":   {Group: "system", Desc: "the calendar and clock widget", Spawn: []string{"zde", "system", "calendar"}},
	"system.wallpapers": {Group: "system", Desc: "the wallpapers widget", Spawn: []string{"zde", "system", "wallpapers"}},
	// help is a terminal on the keymap, not a widget: `zde help` printed usage
	// to a stderr no keypress has, so the one key whose job is to say which
	// keys work was itself one of the silent ones. It goes back to being a
	// surface when the shell has one; the action does not change when it does.
	"system.help":          {Group: "system", Desc: "the keymap, in a terminal", Spawn: []string{"zde", "app", "launch", "help"}, written: true},
	"system.brightness-up": {Group: "system", Desc: "brightness up", Spawn: []string{"brightnessctl", "set", "5%+"}, Repeat: true, WhenLocked: true, written: true},
	"system.brightness-dn": {Group: "system", Desc: "brightness down", Spawn: []string{"brightnessctl", "set", "5%-"}, Repeat: true, WhenLocked: true, written: true},
	"system.layout-switch": {Group: "system", Desc: "switch keyboard layout (language)", Native: "switch-layout \"next\""},
	"system.notif-center":  {Group: "system", Desc: "the notification center", Spawn: []string{"zde", "system", "notif-center"}, written: true},
	// The other half of a popup, and the reason there can be one at all. A
	// notification popup appears without taking the keyboard - every other
	// surface the shell draws over the bar already fights over that grab, and a
	// surface that took it at the choice of any app on the session bus would be
	// the worst of them - so its buttons are clickable whenever it is up and
	// pressable only after somebody asks. This is the asking, and it is a key
	// rather than a gesture because a grab nobody requested is the thing being
	// avoided.
	"system.notif-reach": {Group: "system", Desc: "put the keyboard on the newest notification popup", Spawn: []string{"zde", "system", "notif-reach"}, written: true},
	// doctor is a screen of text and there is nothing yet to show one: a bind
	// spawns a process whose stdout goes to niri's log, so a chord for this
	// would be a key that answers into a file nobody is reading. Registered
	// anyway, because the action map has it (docs/model.md, section 6) and the
	// palette runs actions by name - the chord comes with a surface to print
	// into. Until then it is `zde doctor` in a terminal.
	"system.doctor": {Group: "system", Desc: "the doctor: every check on one screen", Spawn: []string{"zde", "doctor"}, written: true},
	// The state snapshot, and it is registered for the opposite reason doctor
	// is. Doctor's whole product is text, so a bind that sends it to a log
	// nobody reads is a key that answers into a file - it waits for a surface.
	// This one's product is the file: run from the palette on a session that is
	// half up, it writes down what the machine looks like while it still looks
	// like that, and the line it prints afterwards is the least of it. So it is
	// reachable by name today and loses nothing by having no chord, which is
	// the owner's rule met on the honest side of it - inside a session it is an
	// action you can reach, and `zde report` in a TTY is the same thing when
	// there is no session left to reach anything from.
	//
	// Nothing binds it in common/keymap/keymap.yaml on purpose. A chord for the
	// day something has gone wrong is a chord carried every other day.
	"system.report": {Group: "system", Desc: "write down what this machine looks like, for a session that will not come up", Spawn: []string{"zde", "report"}, written: true},
}
