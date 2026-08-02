// Package niri talks to the running compositor over its IPC socket.
//
// The protocol is niri's, documented in niri-ipc and pinned by the niri in
// nixpkgs: connect to $NIRI_SOCKET, write one JSON request per line, read one
// JSON reply per line. A reply is Rust's Result, so it arrives as {"Ok": ...}
// or {"Err": "..."}, and a request that carries no payload is a bare string.
//
// Only what zded needs is modelled here. niri adds struct fields in patch
// releases, which is fine in one direction: unknown fields are ignored, and
// every field this package reads is one niri has documented as stable.
package niri

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/crispuscrew/zde/internal/desk"
)

// SocketEnv is where niri says its socket is.
const SocketEnv = "NIRI_SOCKET"

// requestTimeout bounds one request and its reply. A compositor that stops
// answering must not wedge zded, which is the process that would otherwise be
// asked why the desktop is not responding. A variable so the tests can make
// the wait short enough to actually assert on.
var requestTimeout = 5 * time.Second

// Workspace is niri's workspace, narrowed to what the desk model uses.
//
// Id is stable across moves and monitors but not across a compositor restart,
// which is why the desk map keys on names and not on this. It is here because
// it is the only way to address one workspace unambiguously within a session -
// a rename has to name the workspace it renames.
type Workspace struct {
	ID      uint64  `json:"id"`
	Idx     uint8   `json:"idx"`
	Name    *string `json:"name"`
	Output  *string `json:"output"`
	Active  bool    `json:"is_active"`
	Focused bool    `json:"is_focused"`
}

// Window is niri's window, narrowed to what naming a workspace after its first
// app needs, and to what picking one out of a list needs.
//
// Title is what the app calls the window and app id is what it is; both are
// here because neither alone tells two terminals apart, and a list you cannot
// tell apart is a list you cannot choose from.
type Window struct {
	ID          uint64  `json:"id"`
	Title       *string `json:"title"`
	AppID       *string `json:"app_id"`
	WorkspaceID *uint64 `json:"workspace_id"`
	IsFocused   bool    `json:"is_focused"`
}

// Output is niri's output, narrowed to its name. Which outputs exist is the
// input that separates a workspace someone moved from one whose monitor was
// unplugged (internal/desk).
type Output struct {
	Name string `json:"name"`
}

// Client is a connection to niri. It is not safe for concurrent use: one
// request, one reply, in order, on one socket.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
}

// Dial connects to the socket named by $NIRI_SOCKET.
func Dial() (*Client, error) {
	path := os.Getenv(SocketEnv)
	if path == "" {
		return nil, fmt.Errorf("niri: %s is not set: is this running inside a niri session?", SocketEnv)
	}
	return DialPath(path)
}

// DialPath connects to a niri socket at an explicit path.
func DialPath(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("niri: dial %s: %w", path, err)
	}
	return &Client{conn: conn, r: bufio.NewReader(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// reply is niri's Result: exactly one of these is present.
type reply struct {
	Ok  json.RawMessage `json:"Ok"`
	Err *string         `json:"Err"`
}

// call writes one request and reads one reply, returning the Ok payload.
func (c *Client) call(req any, what string) (json.RawMessage, error) {
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.conn.SetDeadline(time.Now().Add(requestTimeout)); err != nil {
		return nil, err
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("niri: write %s: %w", what, err)
	}
	raw, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("niri: read %s: %w", what, err)
	}
	var rep reply
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("niri: %s: reply is not niri's: %w", what, err)
	}
	if rep.Err != nil {
		return nil, fmt.Errorf("niri: %s: %s", what, *rep.Err)
	}
	if len(rep.Ok) == 0 {
		return nil, fmt.Errorf("niri: %s: reply carried neither Ok nor Err", what)
	}
	return rep.Ok, nil
}

// request is call for the responses that carry data, which niri wraps in the
// name of the response variant.
func (c *Client) request(req any, variant string) (json.RawMessage, error) {
	okRaw, err := c.call(req, variant)
	if err != nil {
		return nil, err
	}
	var byVariant map[string]json.RawMessage
	if err := json.Unmarshal(okRaw, &byVariant); err != nil {
		return nil, fmt.Errorf("niri: %s: %w", variant, err)
	}
	payload, ok := byVariant[variant]
	if !ok {
		return nil, fmt.Errorf("niri: asked for %s, got %v", variant, keys(byVariant))
	}
	return payload, nil
}

// Action performs a niri action. An action that niri accepted answers with the
// bare string "Handled" rather than a payload, so anything else means it did
// not do what was asked.
func (c *Client) Action(action any, what string) error {
	okRaw, err := c.call(map[string]any{"Action": action}, what)
	if err != nil {
		return err
	}
	var handled string
	if err := json.Unmarshal(okRaw, &handled); err != nil || handled != "Handled" {
		return fmt.Errorf("niri: %s: not handled: %s", what, okRaw)
	}
	return nil
}

// Perform runs one of niri's own actions, named the way a bind names it:
// kebab-case, as the keymap registry writes it (internal/keymap).
//
// It is how the palette runs a row that niri handles itself. Half the keymap is
// niri's own - the columns, the monitors, the screenshots - and those keys work
// today, so a palette that could not run them would be listing what it cannot
// do beside what it can.
//
// Only the actions that carry no argument, which is what the empty object says.
// niri's arguments are its own types on the wire - a column width is not the
// string "-10%" but the change that string parses to - and spelling one here
// would be zde guessing at another project's protocol from the outside. The key
// still does those; the palette says so rather than guessing (internal/zded,
// whyNot). The empty object is the shape niri already takes for an action with
// no argument (see FocusWindowVertically), and an action it does not know comes
// back as niri's own refusal rather than as silence.
func (c *Client) Perform(action string) error {
	name := variant(action)
	return c.Action(map[string]any{name: map[string]any{}}, name)
}

// variant is a bind's action name in the spelling niri's IPC uses for it:
// focus-column-left is FocusColumnLeft. One rule rather than a table of twenty,
// because a table would be a second copy of the same list to keep true.
func variant(action string) string {
	parts := strings.Split(action, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// FocusWorkspace focuses a workspace by name. Focusing one makes the monitor
// it is on show it, which is how a desk switch moves every monitor at once.
func (c *Client) FocusWorkspace(name string) error {
	return c.Action(map[string]any{
		"FocusWorkspace": map[string]any{"reference": map[string]any{"Name": name}},
	}, "FocusWorkspace "+name)
}

// SetWorkspaceName renames a workspace addressed by niri's id. Adoption uses
// the id because the workspace it is naming has no name to be addressed by.
func (c *Client) SetWorkspaceNameByID(id uint64, name string) error {
	return c.Action(map[string]any{
		"SetWorkspaceName": map[string]any{"name": name, "workspace": map[string]any{"Id": id}},
	}, "SetWorkspaceName "+name)
}

// RenameWorkspace renames a workspace that already has one.
func (c *Client) RenameWorkspace(from, to string) error {
	return c.Action(map[string]any{
		"SetWorkspaceName": map[string]any{"name": to, "workspace": map[string]any{"Name": from}},
	}, "SetWorkspaceName "+to)
}

// FocusedName is the name of the focused workspace, empty if it has none.
func (c *Client) FocusedName() (string, error) {
	all, err := c.Workspaces()
	if err != nil {
		return "", err
	}
	for _, w := range all {
		if w.Focused {
			return deref(w.Name), nil
		}
	}
	return "", nil
}

// FocusedOutput is the monitor the focused workspace is on, empty if nothing
// is focused. A window carried to another desk needs it: the desk changes, the
// screen does not.
func (c *Client) FocusedOutput() (string, error) {
	all, err := c.Workspaces()
	if err != nil {
		return "", err
	}
	for _, w := range all {
		if w.Focused {
			return deref(w.Output), nil
		}
	}
	return "", nil
}

// FocusedPlace is the focused workspace's name and the output it is on, from
// one reply.
//
// Asked separately they are two replies with a gap in between, and the gap is
// long enough for a workspace to be dragged to another monitor - which leaves
// a name saying one screen and an output saying another, a pair that describes
// nowhere. Whatever moves, these two agree with each other.
func (c *Client) FocusedPlace() (name, output string, err error) {
	all, err := c.Workspaces()
	if err != nil {
		return "", "", err
	}
	for _, w := range all {
		if w.Focused {
			return deref(w.Name), deref(w.Output), nil
		}
	}
	return "", "", nil
}

// MoveWindowToWorkspace moves the focused window to a workspace by name.
//
// focus stays with the workspace it came from: a desk switch follows, and that
// moves every monitor at once (docs/model.md, invariant 1). Letting niri
// follow the window would move one screen and leave the rest behind.
func (c *Client) MoveWindowToWorkspace(name string) error {
	return c.Action(map[string]any{
		"MoveWindowToWorkspace": map[string]any{
			"reference": map[string]any{"Name": name},
			"focus":     false,
		},
	}, "MoveWindowToWorkspace "+name)
}

// Workspaces lists every workspace niri knows about.
func (c *Client) Workspaces() ([]Workspace, error) {
	payload, err := c.request("Workspaces", "Workspaces")
	if err != nil {
		return nil, err
	}
	var out []Workspace
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("niri: Workspaces: %w", err)
	}
	return out, nil
}

// Outputs lists the connected outputs, by name.
func (c *Client) Outputs() ([]string, error) {
	payload, err := c.request("Outputs", "Outputs")
	if err != nil {
		return nil, err
	}
	// niri keys this map by output name; the value repeats it.
	var byName map[string]Output
	if err := json.Unmarshal(payload, &byName); err != nil {
		return nil, fmt.Errorf("niri: Outputs: %w", err)
	}
	return keysOf(byName), nil
}

// Windows lists the open windows.
func (c *Client) Windows() ([]Window, error) {
	payload, err := c.request("Windows", "Windows")
	if err != nil {
		return nil, err
	}
	var out []Window
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("niri: Windows: %w", err)
	}
	return out, nil
}

// OpenWindow is one open window as somebody picks it out of a list: the id to
// act on, two things to recognise it by, and where it is.
//
// Workspace is niri's name for the workspace, which for one zde owns is the
// zde name - <desk>.<monitor>.<label>, the thing a person reads and says out
// loud (docs/model.md, section 3). A workspace nothing has named yet leaves it
// empty rather than showing an id that means nothing outside the compositor.
type OpenWindow struct {
	ID        uint64
	Title     string
	AppID     string
	Workspace string
}

// OpenWindows lists every open window with the workspace it is on.
//
// The join is here rather than in the caller because it takes two replies, and
// the two describe one moment only if nothing else asks in between: a caller
// that asked separately could show a window against a workspace name that had
// already been corrected under it (invariant 1 renames on a monitor move).
func (c *Client) OpenWindows() ([]OpenWindow, error) {
	windows, err := c.Windows()
	if err != nil {
		return nil, err
	}
	workspaces, err := c.Workspaces()
	if err != nil {
		return nil, err
	}
	named := make(map[uint64]string, len(workspaces))
	for _, w := range workspaces {
		named[w.ID] = deref(w.Name)
	}
	out := make([]OpenWindow, 0, len(windows))
	for _, w := range windows {
		where := ""
		if w.WorkspaceID != nil {
			// A window on no workspace at all is one niri is still placing.
			// Nameless is the same answer as unnamed here: it is somewhere, and
			// jumping to it will find out where.
			where = named[*w.WorkspaceID]
		}
		out = append(out, OpenWindow{
			ID:        w.ID,
			Title:     deref(w.Title),
			AppID:     deref(w.AppID),
			Workspace: where,
		})
	}
	return out, nil
}

// FocusWindow focuses a window by id, wherever it is: niri brings up the
// workspace it is on, on the monitor showing that workspace.
//
// It moves nothing (docs/model.md, invariant 6), which is the whole difference
// between going to a window and fetching one. The id is niri's, stable within a
// session and not across one - which is fine for something read off a list and
// spent immediately, and is why nothing writes one down.
func (c *Client) FocusWindow(id uint64) error {
	return c.Action(map[string]any{
		"FocusWindow": map[string]any{"id": id},
	}, "FocusWindow "+strconv.FormatUint(id, 10))
}

// FocusedWindow is niri's focused window id, 0 when nothing is focused - an
// empty workspace, or a session that has not been touched yet.
func (c *Client) FocusedWindow() (uint64, error) {
	windows, err := c.Windows()
	if err != nil {
		return 0, err
	}
	for _, w := range windows {
		if w.IsFocused {
			return w.ID, nil
		}
	}
	return 0, nil
}

// FocusWindowVertically moves focus one window down or up the stack the
// focused window is in.
//
// It is niri's FocusWindowDown/Up, not the OrWorkspace variants: at the end of
// a stack this does nothing at all, which is the answer zde needs. What
// happens at that edge is a desk-level decision (docs/model.md, invariant 4),
// and niri is not the one to make it.
func (c *Client) FocusWindowVertically(down bool) error {
	action := "FocusWindowUp"
	if down {
		action = "FocusWindowDown"
	}
	return c.Action(map[string]any{action: map[string]any{}}, action)
}

// FirstApps is the app of the first window on each workspace, by workspace id.
// It is what a workspace gets named after when a desk adopts it: a name you
// can read on a bar beats a number you have to remember.
//
// niri lists windows in a stable order per workspace, so "first" is the one
// that has been there longest - the app that made the workspace worth having.
func (c *Client) FirstApps() (map[uint64]string, error) {
	windows, err := c.Windows()
	if err != nil {
		return nil, err
	}
	out := map[uint64]string{}
	for _, w := range windows {
		if w.WorkspaceID == nil || w.AppID == nil || *w.AppID == "" {
			continue
		}
		if _, taken := out[*w.WorkspaceID]; !taken {
			out[*w.WorkspaceID] = *w.AppID
		}
	}
	return out, nil
}

// Events subscribes to niri's event stream and yields the name of each event.
//
// Only the name: the payload is deliberately dropped. zded rebuilds the world
// from names when something happens rather than tracking deltas, so an event
// is a wake-up and not a fact to be trusted. That is the same reasoning that
// keeps the desk map out of zded's memory.
//
// The connection is consumed by the stream, so this Client answers nothing
// else afterwards. The channel closes when niri goes away, which is the signal
// to reconnect.
func (c *Client) Events() (<-chan string, error) {
	if _, err := c.call("EventStream", "EventStream"); err != nil {
		return nil, err
	}
	// No deadline from here on: an idle session is quiet for hours, and a
	// stream that timed out because nothing happened would be a bug that
	// looks like the compositor dying.
	if err := c.conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	out := make(chan string, 16)
	go func() {
		defer close(out)
		for {
			line, err := c.r.ReadBytes('\n')
			if err != nil {
				return
			}
			var byName map[string]json.RawMessage
			if err := json.Unmarshal(line, &byName); err != nil {
				continue // not an event we can read; the next one may be
			}
			for name := range byName {
				select {
				case out <- name:
				default: // a slow reader is not a reason to stall niri
				}
			}
		}
	}()
	return out, nil
}

// EmptyByOutput is the unnamed workspaces with nothing in them, by output.
// niri keeps one at the end of every strip, which is what a desk's declared
// workspaces get made out of.
func (c *Client) EmptyByOutput() (map[string][]uint64, error) {
	workspaces, err := c.Workspaces()
	if err != nil {
		return nil, err
	}
	firstApp, err := c.FirstApps()
	if err != nil {
		return nil, err
	}
	out := map[string][]uint64{}
	for _, w := range workspaces {
		if w.Output == nil || (w.Name != nil && *w.Name != "") {
			continue
		}
		if _, hasApp := firstApp[w.ID]; hasApp {
			continue
		}
		out[*w.Output] = append(out[*w.Output], w.ID)
	}
	return out, nil
}

// DeskMap reads niri and rebuilds the desk map from it. This is the join
// between the compositor and the spatial model: workspaces carry the names,
// the output list is what tells a move apart from an unplug.
func (c *Client) DeskMap() (*desk.Map, error) {
	workspaces, err := c.Workspaces()
	if err != nil {
		return nil, err
	}
	outputs, err := c.Outputs()
	if err != nil {
		return nil, err
	}
	return desk.Rebuild(AsDeskWorkspaces(workspaces), outputs), nil
}

// AsDeskWorkspaces narrows niri's workspaces to what the desk model reads.
// niri reports an absent name or output as null, which is a workspace it made
// itself, or one on no monitor at all.
func AsDeskWorkspaces(in []Workspace) []desk.Workspace {
	out := make([]desk.Workspace, 0, len(in))
	for _, w := range in {
		out = append(out, desk.Workspace{ID: w.ID, Idx: w.Idx, Name: deref(w.Name), Output: deref(w.Output)})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOf(m map[string]Output) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
