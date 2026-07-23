package keymap

// Entry pins what one action means for niri. Exactly one of Native (a niri
// action line) or Spawn (an argv) is set; %s marks where a parametric
// argument lands. The registry is code on purpose: the YAML assigns chords
// and nothing else.
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
	argNum
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
	"clip",
	"audio",
	"media",
	"system",
}

var registry = map[string]Entry{
	// desk: zde-level, everything goes through the zde CLI (zded's client).
	"desk.pick":       {Group: "desk", Desc: "open the desk picker", Spawn: []string{"zde", "desk", "pick"}},
	"desk.queue-jump": {Group: "desk", Desc: "jump to the queue's top item", Spawn: []string{"zde", "desk", "queue-jump"}},
	"desk.previous":   {Group: "desk", Desc: "back to the previous desk", Spawn: []string{"zde", "desk", "previous"}},
	"desk.commons":    {Group: "desk", Desc: "go to the commons", Spawn: []string{"zde", "desk", "commons"}},
	"desk.panic":      {Group: "desk", Desc: "panic: decoy desk, lock down", Spawn: []string{"zde", "desk", "panic"}},
	"desk.zen":        {Group: "desk", Desc: "toggle zen", Spawn: []string{"zde", "desk", "zen"}},

	// monitor: niri natives.
	"monitor.focus left":           {Group: "monitor", Desc: "focus the left monitor", Native: "focus-monitor-left"},
	"monitor.focus right":          {Group: "monitor", Desc: "focus the right monitor", Native: "focus-monitor-right"},
	"monitor.move-window left":     {Group: "monitor", Desc: "move the window one monitor left", Native: "move-window-to-monitor-left"},
	"monitor.move-window right":    {Group: "monitor", Desc: "move the window one monitor right", Native: "move-window-to-monitor-right"},
	"monitor.move-workspace left":  {Group: "monitor", Desc: "move the workspace one monitor left", Native: "move-workspace-to-monitor-left"},
	"monitor.move-workspace right": {Group: "monitor", Desc: "move the workspace one monitor right", Native: "move-workspace-to-monitor-right"},

	// workspace: niri natives; the strip is vertical, next = down. Band
	// clamping arrives with zded (model.md, invariant 4).
	"workspace.next":        {Group: "workspace", Desc: "next workspace (down the strip)", Native: "focus-workspace-down"},
	"workspace.prev":        {Group: "workspace", Desc: "previous workspace (up the strip)", Native: "focus-workspace-up"},
	"workspace.overview":    {Group: "workspace", Desc: "toggle the overview", Native: "toggle-overview"},
	"workspace.go":          {Group: "workspace", Desc: "focus workspace", Native: "focus-workspace %s", Arg: argNum},
	"workspace.move-window": {Group: "workspace", Desc: "move the window to workspace", Native: "move-window-to-workspace %s", Arg: argNum},

	// window: niri natives except jump, which travels the whole hierarchy.
	"window.focus left":  {Group: "window", Desc: "focus the column left", Native: "focus-column-left"},
	"window.focus right": {Group: "window", Desc: "focus the column right", Native: "focus-column-right"},
	"window.focus up":    {Group: "window", Desc: "focus the window above", Native: "focus-window-up"},
	"window.focus down":  {Group: "window", Desc: "focus the window below", Native: "focus-window-down"},
	"window.move left":   {Group: "window", Desc: "move the column left", Native: "move-column-left"},
	"window.move right":  {Group: "window", Desc: "move the column right", Native: "move-column-right"},
	"window.move up":     {Group: "window", Desc: "move the window up the column", Native: "move-window-up"},
	"window.move down":   {Group: "window", Desc: "move the window down the column", Native: "move-window-down"},
	"window.float":       {Group: "window", Desc: "toggle floating", Native: "toggle-window-floating"},
	"window.fullscreen":  {Group: "window", Desc: "fullscreen", Native: "fullscreen-window"},
	"window.close":       {Group: "window", Desc: "close the window", Native: "close-window"},
	"window.consume":     {Group: "window", Desc: "consume the next window into this column", Native: "consume-window-into-column"},
	"window.expel":       {Group: "window", Desc: "expel the window from its column", Native: "expel-window-from-column"},
	"window.jump":        {Group: "window", Desc: "fuzzy-jump to any window", Spawn: []string{"zde", "window", "jump"}},

	// launch: zlg is zinc's launcher; the rest goes through zde.
	"launcher.open": {Group: "launch", Desc: "zlg, the app launcher", Spawn: []string{"zlg"}},
	"palette.open":  {Group: "launch", Desc: "the command palette (every action lives here)", Spawn: []string{"zde", "palette"}},
	"app.launch":    {Group: "launch", Desc: "launch", Spawn: []string{"zde", "app", "launch"}, Arg: argName},

	// ask: the quick LLM.
	"ask.oneshot": {Group: "ask", Desc: "one-shot question popup", Spawn: []string{"zde", "ask", "oneshot"}},
	"ask.panel":   {Group: "ask", Desc: "the ask panel", Spawn: []string{"zde", "ask", "panel"}},

	// clip.
	"clip.history": {Group: "clip", Desc: "clipboard history", Spawn: []string{"zde", "clip", "history"}},

	// audio: raw wpctl until zde audio lands; the OSD comes with the shell.
	"audio.vol-up":   {Group: "audio", Desc: "volume up", Spawn: []string{"wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%+"}},
	"audio.vol-down": {Group: "audio", Desc: "volume down", Spawn: []string{"wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", "5%-"}},
	"audio.mute":     {Group: "audio", Desc: "mute the output", Spawn: []string{"wpctl", "set-mute", "@DEFAULT_AUDIO_SINK@", "toggle"}},
	"audio.mic-mute": {Group: "audio", Desc: "mute the mic", Spawn: []string{"wpctl", "set-mute", "@DEFAULT_AUDIO_SOURCE@", "toggle"}},

	// media: through zde so the media target decides who plays
	// (vision.md, media targeting).
	"media.play-pause": {Group: "media", Desc: "play/pause the media target", Spawn: []string{"zde", "media", "play-pause"}},
	"media.next":       {Group: "media", Desc: "next track on the media target", Spawn: []string{"zde", "media", "next"}},
	"media.prev":       {Group: "media", Desc: "previous track on the media target", Spawn: []string{"zde", "media", "prev"}},

	// system.
	"system.lock":         {Group: "system", Desc: "lock the screen", Spawn: []string{"zde", "system", "lock"}},
	"system.quiet":        {Group: "system", Desc: "toggle quiet (do not disturb)", Spawn: []string{"zde", "system", "quiet"}},
	"system.notif-center": {Group: "system", Desc: "the notification center", Spawn: []string{"zde", "system", "notif-center"}},
}
