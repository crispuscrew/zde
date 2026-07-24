package keymap

// Entry pins what one action means for niri. Exactly one of Native (a niri
// action line) or Spawn (an argv) is set; %s marks where a parametric
// argument lands in a Native template. The registry is code on purpose: the
// YAML assigns chords and nothing else.
type Entry struct {
	Group  string
	Desc   string
	Native string
	Spawn  []string
	Arg    argKind
}

type argKind int

const (
	argNone argKind = iota
	argName
)

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
	"desk.switcher":         {Group: "desk", Desc: "open the desk switcher", Spawn: []string{"zde", "desk", "switcher"}},
	"desk.next":             {Group: "desk", Desc: "switch to the next desk", Spawn: []string{"zde", "desk", "next"}},
	"desk.prev":             {Group: "desk", Desc: "switch to the previous desk", Spawn: []string{"zde", "desk", "prev"}},
	"desk.last":             {Group: "desk", Desc: "toggle to the last-active desk", Spawn: []string{"zde", "desk", "last"}},
	"nav.down":              {Group: "desk", Desc: "focus the window below, else rotate to the next desk", Spawn: []string{"zde", "nav", "down"}},
	"nav.up":                {Group: "desk", Desc: "focus the window above, else rotate to the previous desk", Spawn: []string{"zde", "nav", "up"}},
	"desk.move-window-next": {Group: "desk", Desc: "move the window to the next desk", Spawn: []string{"zde", "desk", "move-window", "next"}},
	"desk.move-window-prev": {Group: "desk", Desc: "move the window to the previous desk", Spawn: []string{"zde", "desk", "move-window", "prev"}},
	"desk.queue-jump":       {Group: "desk", Desc: "jump to the queue's top item", Spawn: []string{"zde", "desk", "queue-jump"}},
	"desk.regulars":         {Group: "desk", Desc: "go to the regulars (shared singletons: comms, music, personal browser)", Spawn: []string{"zde", "desk", "regulars"}},
	"desk.panic":            {Group: "desk", Desc: "panic: decoy desk, mute, silence", Spawn: []string{"zde", "desk", "panic"}},
	"desk.block":            {Group: "desk", Desc: "block: hard lock, no notifications or capture leak", Spawn: []string{"zde", "desk", "block"}},
	"desk.zen":              {Group: "desk", Desc: "toggle zen (content only)", Spawn: []string{"zde", "desk", "zen"}},

	// monitor: niri natives. The window axis owns h/l, so monitors get their
	// own pair of chords (keymap.yaml).
	"monitor.focus left":        {Group: "monitor", Desc: "focus the monitor to the left", Native: "focus-monitor-left"},
	"monitor.focus right":       {Group: "monitor", Desc: "focus the monitor to the right", Native: "focus-monitor-right"},
	"monitor.move-window left":  {Group: "monitor", Desc: "move the window one monitor left", Native: "move-window-to-monitor-left"},
	"monitor.move-window right": {Group: "monitor", Desc: "move the window one monitor right", Native: "move-window-to-monitor-right"},

	// window: niri natives except jump-to, which travels the whole hierarchy.
	// Horizontal grid: plain focus, Shift moves, Ctrl resizes. Vertical focus
	// lives on nav.down/up (desk group). consume/expel stack a column: i = in,
	// o = out.
	"window.focus left":  {Group: "window", Desc: "focus the column to the left", Native: "focus-column-left"},
	"window.focus right": {Group: "window", Desc: "focus the column to the right", Native: "focus-column-right"},
	"window.move left":   {Group: "window", Desc: "move the column left", Native: "move-column-left"},
	"window.move right":  {Group: "window", Desc: "move the column right", Native: "move-column-right"},
	"window.narrower":    {Group: "window", Desc: "make the column narrower", Native: "set-column-width \"-10%\""},
	"window.wider":       {Group: "window", Desc: "make the column wider", Native: "set-column-width \"+10%\""},
	"window.shorter":     {Group: "window", Desc: "make the window shorter", Native: "set-window-height \"-10%\""},
	"window.taller":      {Group: "window", Desc: "make the window taller", Native: "set-window-height \"+10%\""},
	"window.fullscreen":  {Group: "window", Desc: "fullscreen", Native: "fullscreen-window"},
	"window.float":       {Group: "window", Desc: "toggle floating", Native: "toggle-window-floating"},
	"window.close":       {Group: "window", Desc: "close the window", Native: "close-window"},
	"window.consume":     {Group: "window", Desc: "consume: pull the next window into this column (in)", Native: "consume-window-into-column"},
	"window.expel":       {Group: "window", Desc: "expel: push the window out of its column (out)", Native: "expel-window-from-column"},
	"window.jump-to":     {Group: "window", Desc: "jump to any open window by name", Spawn: []string{"zde", "window", "jump-to"}},

	// workspace: niri natives. Numbered switching is gone (desks replace it);
	// overview zooms out to the whole band.
	"workspace.overview": {Group: "workspace", Desc: "toggle the overview (zoom out to all workspaces)", Native: "toggle-overview"},

	// launch: zlg is zinc's launcher; the rest goes through zde. "terminal"
	// and "editor" are logical names zde resolves to the configured zinc apps
	// ($EDITOR is whichever editor app you set, not a hardcoded one).
	// launch-at prompts for a directory; launch opens with ambient context.
	"launcher.open": {Group: "launch", Desc: "zlg, the app launcher", Spawn: []string{"zlg"}},
	"palette.open":  {Group: "launch", Desc: "search and run any action by name", Spawn: []string{"zde", "palette"}},
	"app.launch":    {Group: "launch", Desc: "launch", Spawn: []string{"zde", "app", "launch"}, Arg: argName},
	"app.launch-at": {Group: "launch", Desc: "prompt for a location, then launch", Spawn: []string{"zde", "app", "launch-at"}, Arg: argName},

	// ask: the quick LLM.
	"ask.oneshot": {Group: "ask", Desc: "one-shot question popup", Spawn: []string{"zde", "ask", "oneshot"}},
	"ask.panel":   {Group: "ask", Desc: "the ask panel", Spawn: []string{"zde", "ask", "panel"}},

	// pass: the trusted secrets window - the safe path for passwords. Types
	// into the focused field over the clipboard's dead body (never touches
	// it). This is clip's secure sibling.
	"pass.open": {Group: "pass", Desc: "open zde-pass (types a secret into the focused field, never via the clipboard)", Spawn: []string{"zde", "pass"}},

	// clip.
	"clip.history": {Group: "clip", Desc: "clipboard history", Spawn: []string{"zde", "clip", "history"}},

	// capture: screenshots and the replay clip.
	"capture.shot-region": {Group: "capture", Desc: "screenshot a region", Spawn: []string{"zde", "capture", "region"}},
	"capture.shot-window": {Group: "capture", Desc: "screenshot the focused window", Spawn: []string{"zde", "capture", "window"}},
	"capture.shot-full":   {Group: "capture", Desc: "screenshot the whole output", Spawn: []string{"zde", "capture", "full"}},

	// media: through zde so the media target decides who plays
	// (docs/vision.md, media targeting). The target auto-routes to the
	// most-recent player by default; the picker pins or re-enables auto.
	"media.play-pause": {Group: "media", Desc: "play/pause the media target", Spawn: []string{"zde", "media", "play-pause"}},
	"media.next":       {Group: "media", Desc: "next track on the media target", Spawn: []string{"zde", "media", "next"}},
	"media.prev":       {Group: "media", Desc: "previous track on the media target", Spawn: []string{"zde", "media", "prev"}},
	"media.panel":      {Group: "media", Desc: "the music control panel", Spawn: []string{"zde", "media", "panel"}},
	"media.like":       {Group: "media", Desc: "like the current track", Spawn: []string{"zde", "media", "like"}},
	"media.target":     {Group: "media", Desc: "the media target picker (pin a player, or auto-route)", Spawn: []string{"zde", "media", "target"}},

	// audio: raw wpctl until zde audio lands; the OSD comes with the shell.
	"audio.vol-up":   {Group: "audio", Desc: "volume up", Spawn: []string{"wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%+"}},
	"audio.vol-down": {Group: "audio", Desc: "volume down", Spawn: []string{"wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%-"}},
	"audio.mute":     {Group: "audio", Desc: "mute the output", Spawn: []string{"wpctl", "set-mute", "@DEFAULT_AUDIO_SINK@", "toggle"}},
	"audio.mic-mute": {Group: "audio", Desc: "mute the mic", Spawn: []string{"wpctl", "set-mute", "@DEFAULT_AUDIO_SOURCE@", "toggle"}},

	// net: the observer widget does per-app cuts and the kill switch; the two
	// quick-cut sequences (keymap.yaml) wait for the input daemon.
	"net.observe": {Group: "net", Desc: "the network observer widget (per-app connections; cut from here)", Spawn: []string{"zde", "net", "observe"}},
	"net.app-cut": {Group: "net", Desc: "cut the focused app's network", Spawn: []string{"zde", "net", "app-cut"}},
	"net.kill":    {Group: "net", Desc: "cut all network (loud, reversible)", Spawn: []string{"zde", "net", "kill"}},

	// modes: not wired yet - the mode mechanism is a verify item
	// (docs/roadmap.md). menu is the entry point (pick a mode); the rest are
	// what it invokes. Registered so it just works once the daemon lands.
	"modes.menu":        {Group: "modes", Desc: "open the mode picker (Window / Kb-mouse / One-hand / Passthrough)", Spawn: []string{"zde", "mode", "menu"}},
	"modes.window":      {Group: "modes", Desc: "enter Window mode", Spawn: []string{"zde", "mode", "window"}},
	"modes.kb-mouse":    {Group: "modes", Desc: "enter Keyboard-mouse mode", Spawn: []string{"zde", "mode", "kb-mouse"}},
	"modes.one-hand":    {Group: "modes", Desc: "enter One-hand mode", Spawn: []string{"zde", "mode", "one-hand"}},
	"modes.passthrough": {Group: "modes", Desc: "enter Passthrough mode", Spawn: []string{"zde", "mode", "passthrough"}},

	// system: raw tools until zde grows its own (brightnessctl now; layout
	// switch is a niri native). The rest routes through zde.
	"system.lock":          {Group: "system", Desc: "lock the screen", Spawn: []string{"zde", "system", "lock"}},
	"system.quiet":         {Group: "system", Desc: "toggle quiet (do not disturb)", Spawn: []string{"zde", "system", "quiet"}},
	"system.power":         {Group: "system", Desc: "the power menu", Spawn: []string{"zde", "system", "power"}},
	"system.connections":   {Group: "system", Desc: "connections: bluetooth, wifi, ethernet", Spawn: []string{"zde", "system", "connections"}},
	"system.calendar":      {Group: "system", Desc: "the calendar and clock widget", Spawn: []string{"zde", "system", "calendar"}},
	"system.wallpapers":    {Group: "system", Desc: "the wallpapers widget", Spawn: []string{"zde", "system", "wallpapers"}},
	"system.help":          {Group: "system", Desc: "the help widget (the keybind cheatsheet)", Spawn: []string{"zde", "help"}},
	"system.brightness-up": {Group: "system", Desc: "brightness up", Spawn: []string{"brightnessctl", "set", "5%+"}},
	"system.brightness-dn": {Group: "system", Desc: "brightness down", Spawn: []string{"brightnessctl", "set", "5%-"}},
	"system.layout-switch": {Group: "system", Desc: "switch keyboard layout (language)", Native: "switch-layout \"next\""},
	"system.notif-center":  {Group: "system", Desc: "the notification center", Spawn: []string{"zde", "system", "notif-center"}},
}
