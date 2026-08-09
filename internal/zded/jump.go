package zded

import (
	"sort"
	"strconv"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
)

// Window is one open window as the jump list shows it: enough to recognise,
// and the id to act on.
//
// Workspace is the zde name where the workspace has one -
// <desk>.<monitor>.<label> - because that is the thing a person recognises,
// and it is how one column says which desk a window is on as well as which
// screen. Title and app id both, since neither alone tells two terminals apart.
type Window struct {
	ID        uint64 `json:"id"`
	Title     string `json:"title,omitempty"`
	AppID     string `json:"appId,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// Jump is what `window.jump-to` answers with no argument: the windows, and
// whether a surface took the job of showing them. Shown means here exactly what
// it means for the desk switcher, and for the same reason (see Switcher).
type Jump struct {
	Shown   bool     `json:"shown"`
	Windows []Window `json:"windows"`
}

// windows is what is open, in the shape a surface may draw and a terminal may
// print.
//
// Everything goes through here rather than through the compositor directly,
// because a window's title is the one field in this list that the thing being
// listed writes for itself. niri hands it over as it came off the wire - raw
// JSON, whatever the application set - and both readers of this list are places
// where that matters: the CLI prints it to a terminal, where ESC is not a
// character but the start of an instruction, and the picker draws it as one row
// among many, where a newline is a row that overlaps the one below.
//
// A window title is a thing an application chooses, and an application is
// exactly what a sandboxed one is (docs/vision.md, principle 7). So this is the
// same filter the notification path runs on everything an app sends, at the
// same moment: where the value enters zde rather than where each of two
// consumers happens to draw it (internal/attn, Line).
//
// The app id goes through it too. It is meant to be a desktop-file name, so a
// control character in one is already a lie about what it is - and it is set by
// the same process that set the title.
//
// The workspace name as well, which is a smaller risk and a cheaper line than
// arguing about it: nothing an app can call names a workspace, but a name zde
// did not mint is a name zde did not check either (desk.ParseName is what
// checks the ones it owns), and this column is printed beside the other two.
// Cleaning it changes nothing about a name the model owns - those are letters,
// digits, dots and dashes - so what it can only do is spoil a lie.
//
// Written through rather than copied: Windows answers with a list built for
// this call - the daemon's builds it out of two niri replies (cmd/zded), and
// the test double hands back a copy of its own - so there is nothing else
// holding the slice this edits.
func (s *Server) windows() ([]Window, error) {
	windows, err := s.niri.Windows()
	if err != nil {
		return nil, err
	}
	for i := range windows {
		windows[i].Title = attn.Line(windows[i].Title)
		windows[i].AppID = attn.Line(windows[i].AppID)
		windows[i].Workspace = attn.Line(windows[i].Workspace)
	}
	return windows, nil
}

// jumpTo opens the window picker, or says that nothing could open it.
func (s *Server) jumpTo() Response {
	windows, err := s.windows()
	if err != nil {
		return Response{Error: err.Error()}
	}
	if len(windows) == 0 {
		// A surface with no rows is one you have to press Escape to get out of,
		// and it answers a question nobody asked. Refusing says the same thing
		// in words, and leaves the key having done something.
		return Response{Error: "nothing is open to jump to"}
	}
	sortWindows(windows)
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		// Which screen is a detail; not knowing it is not worth refusing over,
		// and the shell falls back to the screen it can see.
		output = ""
	}
	token := s.nextToken()
	acked := s.await(token)
	defer s.stopAwaiting(token)

	sent := s.broadcast(Event{
		Kind:    EventWindows,
		Windows: windows,
		Output:  output,
		Token:   token,
	})
	if sent == 0 {
		return ok(Jump{Shown: false, Windows: windows})
	}
	select {
	case <-acked:
		return ok(Jump{Shown: true, Windows: windows})
	case <-time.After(ackWait):
		return ok(Jump{Shown: false, Windows: windows})
	}
}

// sortWindows puts the list in an order that does not move between two presses
// of the key. niri's order is niri's - it follows the strip and the stacking,
// both of which change as you work - and a picker whose third row is a
// different window each time is one you cannot learn, which is the whole point
// of numbering the rows. Workspace first, so the list reads as the places it
// came from; the id breaks ties, because it is the one thing two windows of the
// same app on one workspace do not share.
func sortWindows(windows []Window) {
	sort.SliceStable(windows, func(i, j int) bool {
		if windows[i].Workspace != windows[j].Workspace {
			return windows[i].Workspace < windows[j].Workspace
		}
		return windows[i].ID < windows[j].ID
	})
}

// focusWindow goes to a window, wherever it is. It is what the picker's choice
// arrives as, and the whole of `zde window jump-to ID`.
//
// Going there is more than one call to niri when the window is on another desk.
// A desk is what every monitor shows at once (docs/model.md, section 2), so
// focusing the window on its own would bring its workspace up on one screen and
// leave the others on the desk you came from - a session on two desks, which
// the model has no name for. The desk comes up first, the way a switch brings
// it up, and the window is focused inside it. That is what makes jumping end
// where walking would have ended.
//
// Nothing is rearranged either way (invariant 6): niri's FocusWindow moves
// focus and moves nothing else, which is the difference between going to a
// window and fetching it.
func (s *Server) focusWindow(arg string) Response {
	id, err := strconv.ParseUint(arg, 10, 64)
	if err != nil {
		return Response{Error: "window.jump-to wants the id from the list, not " + strconv.Quote(arg)}
	}
	// The same list the picker was drawn from, cleaned the same way: this one
	// answers with the workspace it landed on, and the CLI prints that line.
	windows, err := s.windows()
	if err != nil {
		return Response{Error: err.Error()}
	}
	target, found := Window{}, false
	for _, w := range windows {
		if w.ID == id {
			target, found = w, true
			break
		}
	}
	if !found {
		// Closed between the list and the choice, which a list on screen makes
		// ordinary rather than exotic. niri would refuse this too, in words
		// about an id that nobody typed and cannot look up.
		return Response{Error: "no window with id " + arg + ": it is not open any more"}
	}
	// A workspace with no zde name belongs to no desk, so there is no desk to
	// bring up first: the window is on the strip in front of us, or on one
	// adoption has not claimed yet, and focusing it is the whole journey.
	landing, parseErr := desk.ParseName(target.Workspace)
	onADesk := parseErr == nil
	if onADesk {
		m, err := s.niri.DeskMap()
		if err != nil {
			return Response{Error: err.Error()}
		}
		if from := s.activeDesk(m); from != landing.Desk {
			if resp := s.switchFrom(landing.Desk, from); resp.Error != "" {
				// The desk did not come up, so the window is not somewhere the
				// jump can leave the session looking at. Nothing has moved.
				return resp
			}
		}
	}
	if err := s.niri.FocusWindow(id); err != nil {
		return Response{Error: err.Error()}
	}
	if s.jrn != nil && onADesk {
		// Where the desk was left, so coming back lands on the window you
		// jumped to rather than on where that desk was before (invariant 5).
		// A switch, when there was one, wrote that desk's own landing slot on
		// the way past; this corrects it to where the jump actually ended.
		//
		// Which desk you are on is not written here, deliberately: reading the
		// active desk above already recorded it, and a switch records the one it
		// arrived at. A third writer would be a line no test could fail on.
		s.jrn.SetActive(landing)
	}
	if target.Workspace == "" {
		// Nothing to print: the window is on a workspace with no name to say.
		return ok([]string{})
	}
	// The workspace it went to, the way every other verb that moves the session
	// answers - one name per line, so the CLI needs no case of its own.
	return ok([]string{target.Workspace})
}
