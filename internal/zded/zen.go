package zded

import "log"

// zen is "hide bar, borders, gaps; content only" (docs/glossary.md). Three
// things, and zde owns exactly one of them.
//
// The bar is zde's own layer-shell surface, so the shell unmaps it and niri
// gives the space it was reserving back to the windows. That half is in
// shell/shell.qml.
//
// The borders, the focus ring and the gaps are niri's, and niri 26.04 exposes
// no way to change them while it runs: the whole IPC action list is windows,
// columns, workspaces, monitors, screenshots and screencasts, and there is
// nothing in it for the layout section. What there is, is a config it reloads.
// So zen writes a `layout` block into dynamic.kdl - the file zded already owns
// and niri already includes (rules.go) - and asks niri to reload. niri merges a
// later file's layout over an earlier one, so the block overrides what
// niri/config.kdl and the host's local.kdl set, and removing it puts both back
// without either file being touched.
//
// What zen therefore cannot hide: anything an application draws for itself. A
// window that comes with its own titlebar keeps it, `prefer-no-csd` being a
// request and not a rule. zen says content only about the chrome this desktop
// puts there.
//
// Every screen, not the focused one. The niri half has no per-output spelling -
// gaps and borders are one layout section for the session - so a zen scoped to
// one monitor could only ever hide half of what it promises, and a bar that
// vanished from one screen while the borders stayed everywhere is worse than
// either answer.
//
// Not a security mode, and this is the line that keeps it honest: zen changes
// no display policy at all. What may pop up is attn's question and the desk's
// (docs/vision.md, principle 3), so an urgent arrival still arrives, the
// notification center still fills, and the popup that says something needs you
// is drawn over a zen screen exactly as over any other. The panic and lock
// keys are untouched, because zen writes no binds. The one thing zen will not
// take off the screen is the microphone: the bar comes back by itself while
// something is holding it, since that is the only line on the strip that is
// about the room rather than about the desktop (shell/shell.qml).

// zenLayout is what zen puts in front of the placement rules, and nothing when
// it is off: an absent block is how the values in niri's own config come back.
//
// `off` on both border and focus ring rather than a width of zero. niri merges
// these node by node (niri-config, merge_on_off), so `off` beats whatever the
// host set in local.kdl, where a width would leave a ring somebody had coloured
// still drawn at zero pixels.
func zenLayout(on bool) string {
	if !on {
		return ""
	}
	return "\nlayout {\n" +
		"    gaps 0\n" +
		"    focus-ring {\n" +
		"        off\n" +
		"    }\n" +
		"    border {\n" +
		"        off\n" +
		"    }\n" +
		"}\n"
}

// Zen is what the shell asks for and what `zde desk zen` prints.
type Zen struct {
	// Not omitempty: off is an answer, and a field that disappeared when zen
	// was off would be indistinguishable to the shell from a reply about
	// something else (shell/shell.qml reads it by asking whether it is there).
	Zen bool `json:"zen"`
}

// zenState is what the journal says. Off when there is no journal, which is the
// honest answer for a daemon that has nowhere to remember a toggle.
func (s *Server) zenState() bool {
	if s.jrn == nil {
		return false
	}
	return s.jrn.Zen()
}

// setZen writes the state down, puts it into niri's config, and tells the shell.
//
// The journal first, because it is the record. Everything after it is that
// record being applied to something, and each of those can fail on its own
// without making the answer wrong: dynamic.kdl is rewritten from the journal at
// every startup and reconcile (SyncRules), and the shell asks for this state on
// a clock, so a session that missed one of them converges rather than staying
// split.
func (s *Server) setZen(on bool) Response {
	if s.jrn == nil {
		return Response{Error: "no journal, so zen could not be remembered - and a bar that comes back at the next login with the borders still gone is worse than no zen at all"}
	}
	if err := s.jrn.SetZen(on); err != nil {
		return Response{Error: err.Error()}
	}
	// niri's half. SyncRules writes the whole file, so this is also what puts
	// the placement rules back if a manifest changed under a running session.
	s.SyncRules()
	// And now rather than at niri's next poll of the file. A refusal is half a
	// second of delay and not a failure (internal/niri, ReloadConfig), so it is
	// logged and the answer still stands.
	if err := s.niri.ReloadConfig(); err != nil {
		log.Printf("zded: niri would not reload for zen, so its half arrives when niri next reads the file: %v", err)
	}
	// A nudge and not a copy of the state: the shell answers it by asking, so
	// there is one place this fact is published and no second one to disagree
	// with it. Without the nudge the bar would be up to one poll behind a key
	// whose whole job is to change the screen now.
	s.broadcast(Event{Kind: EventZen})
	return ok(Zen{Zen: on})
}

// zen answers the request. No argument reads the state, which is what the shell
// asks on its clock; a word sets it, and `toggle` is what the key spawns. The
// same one-verb-two-directions shape attn.mode has, and for the same reason - it
// is one question, asked and answered.
func (s *Server) zen(args []string) Response {
	switch {
	case len(args) == 0:
		return ok(Zen{Zen: s.zenState()})
	case len(args) == 1:
		switch args[0] {
		case "on":
			return s.setZen(true)
		case "off":
			return s.setZen(false)
		case "toggle":
			return s.setZen(!s.zenState())
		}
	}
	return Response{Error: "desk.zen takes on, off or toggle, or nothing to read it back"}
}
