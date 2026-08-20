package zded

import (
	"errors"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/crispuscrew/zde/internal/attn"
)

// capture-block is "hide one window from capture" (docs/vision.md, A10). What
// is behind it is a niri window rule, so it goes where zen's layout block goes:
// into the dynamic.kdl zded already owns and niri already includes (rules.go).
// niri 26.04 has no IPC action for block-out-from - the only per-window rule its
// socket can toggle is ToggleWindowRuleOpacity - so a file niri reloads is the
// whole of the mechanism.
//
// What niri accepts, read off its own parser (`niri validate` on a bogus value
// answers "expected one of `screencast`, `screen-capture`") and off
// niri-config/src/appearance.rs. What each one covers is one match in
// src/render_helpers/mod.rs:
//
//	None          => never blocked
//	Screencast    => blocked when the render target is Screencast
//	ScreenCapture => blocked on every target that is not the physical Output
//
// Screencast is niri's PipeWire casting (src/screencasting), which on a zde
// machine is the xdg-desktop-portal-gnome path: OBS, a browser sharing a
// screen. ScreenCapture is that plus wlr-screencopy - grim, wf-recorder,
// xdg-desktop-portal-wlr - plus niri's own automatic screenshot actions,
// screenshot-screen and screenshot-window, which are zde's capture.shot-full
// and capture.shot-window.
//
// So the two values nest: screen-capture is a superset of screencast, and there
// is no third. vision.md's "OBS yes, screenshots no" is the one combination
// niri cannot express - blocking the screenshot path necessarily blocks the
// cast - and the honest half of that sentence is the other half it makes,
// "default blocks all capture paths". That is what zde writes, always, and it
// is why the weaker value is not offered as a choice: it would offer the mirror
// of what was asked, and it reopens the hole niri's own documentation warns
// about, where a third-party screenshot tool showing a preview during a cast
// puts the blocked window on the cast.
//
// The hole screen-capture still leaves, because it is niri's design rather than
// an oversight: the interactive screenshot UI - niri's `screenshot` action,
// zde's capture.shot-region - saves the Output render
// (src/ui/screenshot_ui.rs, `data.screenshot[0]`), so it is not blocked. niri's
// reasoning is that a selection you drag is one you can see. It is written down
// here and in docs/verify.md because the failure this file exists to prevent is
// somebody believing a window is covered when it is not.
const blockOutFrom = "screen-capture"

// Per app id, not per window, and that is niri's rule language rather than a
// choice. A window rule matches on app-id and title, both regexes, plus seven
// booleans about how the window is being displayed right now - is-active,
// is-focused, is-floating and so on (niri-config/src/window_rule.rs, Match).
// None of them names an instance. is-focused comes closest and is exactly
// wrong: a rule carrying it blocks whatever is focused at that moment, so it
// would block a different window every time you moved.
//
// Title is not the way out either. It is a string the application rewrites
// whenever it likes, two windows of one app routinely share one, and niri's own
// documentation warns against blocking on it for that reason - a rule that
// stops matching when a tab changes is a block that goes away silently.
//
// So the per-window toggle of docs/vision.md section 3 is not expressible on
// niri 26.04, and what is delivered is the per-app flag beside it: the verb
// acts on the window in front of you, and what it blocks is every window that
// calls itself what that window calls itself. The reply says which app id it
// acted on, so that this is something somebody reads rather than something they
// discover.

// errNothingFocused is what focusedAppID says when there is no window for this
// to be about - nothing focused, or a focused id that is not in the list niri
// just gave. One error rather than a sentence at each of those two returns, so
// they cannot drift into saying different things about the same situation.
var errNothingFocused = errors.New("nothing is focused, so there is no window to block")

// What a private desk would need is captureRules and a second list. vision.md
// section 3 asks for private desks to be capture-blocked during a screencast and
// during guest mode, and that is one more set of app ids - the ones a private
// manifest pins - handed to the function below, rather than a second mechanism.
// Neither trigger is written: guest is its own change, and the screencast half
// needs niri's Casts stream watched, since blocking a private desk all day is
// not what that sentence says.

// captureRules is one window rule per blocked app id, and nothing when none is
// blocked. It goes into dynamic.kdl beside the placement rules: the two set
// disjoint fields on the same match, so niri merging both leaves each intact.
//
// The app id is quoted twice, inner first, for the reason placementRules quotes
// twice and with more riding on it here: this value is chosen by the
// application (docs/vision.md, ask 1), so it is the one string in that file a
// sandboxed app writes. QuoteMeta makes it a literal to niri's regex, kdlString
// makes that a KDL string niri's parser accepts, and between them there is no
// byte an app can send that closes the quote - which matters because
// dynamic.kdl is included by the config carrying the keymap, and a `binds` node
// smuggled into it would replace the panic key.
func captureRules(blocked []string) string {
	var b strings.Builder
	for _, id := range blocked {
		if id == "" {
			continue
		}
		b.WriteString("\nwindow-rule {\n")
		b.WriteString("    match app-id=" + kdlString("^"+regexp.QuoteMeta(id)+"$") + "\n")
		b.WriteString("    block-out-from " + kdlString(blockOutFrom) + "\n")
		b.WriteString("}\n")
	}
	return b.String()
}

// CaptureBlock is what the toggle answers with.
type CaptureBlock struct {
	// AppID is the window class this call was about, cleaned for a terminal
	// (internal/attn, Line): it is the application's own string and it is
	// printed. What goes into the window rule is the raw one, because a cleaned
	// app id is a rule that matches nothing - this action failing in the one
	// direction it must not fail in.
	AppID string `json:"appId,omitempty"`
	// Blocked is whether that app id is blocked now. Not omitempty: off is an
	// answer.
	Blocked bool `json:"blocked"`
	// All is every app id blocked right now, so somebody can find the one they
	// blocked on a window they have since closed and let it back in.
	All []string `json:"all"`
}

// captureBlocked is the set as the journal has it, and empty when there is no
// journal - the honest answer for a daemon with nowhere to remember a decision.
func (s *Server) captureBlocked() []string {
	if s.jrn == nil {
		return nil
	}
	return s.jrn.CaptureBlocked()
}

// focusedAppID is the app id of the window this was run on, raw.
//
// Two calls to niri rather than one, because the Compositor interface answers
// the focused window as an id and the app ids come with the window list. The
// gap between them is not a race worth closing: an id names one window for as
// long as that window exists, so a focus that moved in between leaves this
// acting on the window that was focused when the verb started, which is the
// window the person was looking at.
func (s *Server) focusedAppID() (string, error) {
	id, err := s.niri.FocusedWindow()
	if err != nil {
		return "", err
	}
	if id == 0 {
		return "", errNothingFocused
	}
	windows, err := s.niri.Windows()
	if err != nil {
		return "", err
	}
	for _, w := range windows {
		if w.ID == id {
			return w.AppID, nil
		}
	}
	return "", errNothingFocused
}

// setCaptureBlock writes the decision down, puts it into niri's config, and
// asks niri to read it now.
//
// The journal first, because it is the record; everything after it is that
// record being applied to something. SyncRules rewrites the whole file from the
// journal at every startup and reconcile, so a session that missed one of these
// converges rather than staying split. The same order setZen has.
func (s *Server) setCaptureBlock(appID string, on bool) Response {
	if s.jrn == nil {
		return Response{Error: "no journal, so a capture block could not be remembered - and a window that is blocked only until the next login is a window nobody should trust"}
	}
	if err := s.jrn.SetCaptureBlocked(appID, on); err != nil {
		return Response{Error: err.Error()}
	}
	s.SyncRules()
	// Now rather than at niri's next poll of the file, which is up to half a
	// second (internal/niri, ReloadConfig). A refusal is that delay and not a
	// failure, so it is logged and the answer stands: the file is written and
	// niri's own watcher is still watching it.
	if err := s.niri.ReloadConfig(); err != nil {
		log.Printf("zded: niri would not reload for a capture block, so it arrives when niri next reads the file: %v", err)
	}
	return ok(CaptureBlock{AppID: attn.Line(appID), Blocked: on, All: printableIDs(s.captureBlocked())})
}

// printableIDs is the list on its way to a terminal and to a picker row. Every
// string in it was chosen by an application.
//
// Sorted again after cleaning rather than trusted from the journal's ordering
// of the raw strings, because dropping a rune can put two ids out of order.
func printableIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, attn.Line(id))
	}
	sort.Strings(out)
	return out
}

// captureBlock answers the request. No argument reads the state back; a word
// acts on the focused window; a word and an app id act on that id, which is how
// a block is lifted from an application whose window has since been closed.
// `toggle` is what a key spawns, so whatever chord this is given later is one
// key both ways - the shape net.kill, panic and zen all have.
func (s *Server) captureBlock(args []string) Response {
	switch len(args) {
	case 0:
		appID, err := s.focusedAppID()
		if err != nil {
			// Not a refusal: reading the list back is a question that does not
			// need a window. Which window is focused is the part that could not
			// be answered, and that row is left empty.
			return ok(CaptureBlock{All: printableIDs(s.captureBlocked())})
		}
		return ok(CaptureBlock{
			AppID:   attn.Line(appID),
			Blocked: s.isCaptureBlocked(appID),
			All:     printableIDs(s.captureBlocked()),
		})
	case 1, 2:
		appID := ""
		if len(args) == 2 {
			appID = args[1]
		} else {
			var err error
			if appID, err = s.focusedAppID(); err != nil {
				return Response{Error: err.Error()}
			}
		}
		if strings.TrimSpace(appID) == "" {
			// A window that told niri nothing about what it is. There is
			// nothing to write a rule against, and an empty pattern is a rule
			// that matches every window on the machine - so this refuses and
			// says why rather than blacking out the session.
			return Response{Error: "that window has no app id, so there is nothing for a window rule to match: `zde window jump-to` lists what each window calls itself"}
		}
		switch args[0] {
		case "on":
			return s.setCaptureBlock(appID, true)
		case "off":
			return s.setCaptureBlock(appID, false)
		case "toggle":
			return s.setCaptureBlock(appID, !s.isCaptureBlocked(appID))
		}
	}
	return Response{Error: "window.capture-block takes on, off or toggle - with an app id to name one, or without to mean the focused window - or nothing to read it back"}
}

// isCaptureBlocked compares raw against raw. The cleaned form is for printing
// and nothing else: two app ids that differ only in a rune Line drops are two
// different rules to niri, and answering about one of them by matching the
// other is how a window ends up reported as blocked while nothing blocks it.
func (s *Server) isCaptureBlocked(appID string) bool {
	for _, id := range s.captureBlocked() {
		if id == appID {
			return true
		}
	}
	return false
}
