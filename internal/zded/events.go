package zded

import (
	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/clip"
	"github.com/crispuscrew/zde/internal/link"
)

// Event payloads accompany the surface request. Clipboard rows contain previews only.
// Token acknowledges display, not delivery. ask.text is private to its requesting
// connection and ends only at Done; events never carry request IDs.
type Event struct {
	Kind          string         `json:"kind"`
	Desks         []string       `json:"desks,omitempty"`
	Windows       []Window       `json:"windows,omitempty"`
	Notifications []attn.Record  `json:"notifications,omitempty"`
	Actions       []Action       `json:"actions,omitempty"`
	Choices       []PowerChoice  `json:"choices,omitempty"`
	Clips         []clip.Row     `json:"clips,omitempty"`
	Networks      []link.Network `json:"networks,omitempty"`
	Link          *link.Status   `json:"link,omitempty"`
	On            string         `json:"on,omitempty"`
	Output        string         `json:"output,omitempty"`
	Token         string         `json:"token,omitempty"`
	Question      string         `json:"question,omitempty"`
	Text          string         `json:"text,omitempty"`
	Done          bool           `json:"done,omitempty"`
	Error         string         `json:"error,omitempty"`
}

const EventPicker = "picker"

const EventWindows = "windows"

// Distinct kinds make old shells ignore unsupported actions instead of switching desks.
const (
	EventPickerMoveWindow    = "picker.move-window"
	EventPickerMoveWorkspace = "picker.move-workspace"
)

const EventCenter = "notif-center"

// EventAttnPopup is unsolicited: no acknowledgement, waiting or keyboard grab.
const EventAttnPopup = "attn.popup"

// EventAttnReach grants popup keyboard focus only after an explicit user action.
const EventAttnReach = "attn.reach"

// EventAttnHide changes display only; queue and history remain intact.
const EventAttnHide = "attn.hide"

const (
	EventAsk      = "ask"
	EventAskPanel = "ask.panel"
	EventAskText  = "ask.text"
)

const EventPalette = "palette"

const EventClip = "clip"

const EventZen = "zen"

const MethodShown = "shown"

const MethodEvents = "events"
