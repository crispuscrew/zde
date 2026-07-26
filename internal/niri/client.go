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
	ID     uint64  `json:"id"`
	Idx    uint8   `json:"idx"`
	Name   *string `json:"name"`
	Output *string `json:"output"`
	Active bool    `json:"is_active"`
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

// request writes one request and reads one reply, returning the payload of the
// named response variant.
func (c *Client) request(req any, variant string) (json.RawMessage, error) {
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.conn.SetDeadline(time.Now().Add(requestTimeout)); err != nil {
		return nil, err
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("niri: write %s: %w", variant, err)
	}
	raw, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("niri: read %s: %w", variant, err)
	}
	var rep reply
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("niri: %s: reply is not niri's: %w", variant, err)
	}
	if rep.Err != nil {
		return nil, fmt.Errorf("niri: %s: %s", variant, *rep.Err)
	}
	if len(rep.Ok) == 0 {
		return nil, fmt.Errorf("niri: %s: reply carried neither Ok nor Err", variant)
	}
	// Response is an enum too, so the payload sits under its variant name.
	var byVariant map[string]json.RawMessage
	if err := json.Unmarshal(rep.Ok, &byVariant); err != nil {
		return nil, fmt.Errorf("niri: %s: %w", variant, err)
	}
	payload, ok := byVariant[variant]
	if !ok {
		return nil, fmt.Errorf("niri: asked for %s, got %v", variant, keys(byVariant))
	}
	return payload, nil
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
		out = append(out, desk.Workspace{Name: deref(w.Name), Output: deref(w.Output)})
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
