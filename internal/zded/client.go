package zded

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client talks to a running zded. The CLI and, later, the shell's adapter both
// come through here rather than each inventing the protocol again.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	// Events that arrived while waiting for a reply. A connection that has
	// subscribed carries both kinds of line, in whatever order they happen, so
	// a client reading a reply has to be able to meet an event and keep going -
	// otherwise the first event to land mid-call is read as a malformed reply.
	queued []Event
}

func DialPath(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("zde: no zded at %s: %w", path, err)
	}
	return &Client{conn: conn, r: bufio.NewReader(conn)}, nil
}

// Dial connects to the socket in the runtime directory.
func Dial() (*Client, error) {
	path, err := DefaultSocket()
	if err != nil {
		return nil, err
	}
	return DialPath(path)
}

func (c *Client) Close() error { return c.conn.Close() }

// Call sends one request and decodes the reply into out.
func (c *Client) Call(method string, out any, args ...string) error {
	if err := c.conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	line, err := json.Marshal(Request{Method: method, Args: args})
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return err
	}
	resp, err := c.reply(method)
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("zde: %s: %s", method, resp.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Ok, out)
}

// line is one message from zded, either kind.
type line struct {
	Event *Event          `json:"event"`
	Ok    json.RawMessage `json:"ok"`
	Error string          `json:"error"`
}

// reply reads until a reply arrives, queueing any events it passes on the way.
func (c *Client) reply(method string) (Response, error) {
	for {
		raw, err := c.r.ReadBytes('\n')
		if err != nil {
			return Response{}, fmt.Errorf("zde: %s: %w", method, err)
		}
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			return Response{}, fmt.Errorf("zde: %s: reply is not zded's: %w", method, err)
		}
		if l.Event != nil {
			c.queued = append(c.queued, *l.Event)
			continue
		}
		return Response{Ok: l.Ok, Error: l.Error}, nil
	}
}

// NextEvent waits for the next event on a connection that has asked for them.
// Events already passed while reading a reply come back first, in order.
func (c *Client) NextEvent() (Event, error) {
	if len(c.queued) > 0 {
		ev := c.queued[0]
		c.queued = c.queued[1:]
		return ev, nil
	}
	if err := c.conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return Event{}, err
	}
	for {
		raw, err := c.r.ReadBytes('\n')
		if err != nil {
			return Event{}, fmt.Errorf("zde: waiting for an event: %w", err)
		}
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			return Event{}, fmt.Errorf("zde: event is not zded's: %w", err)
		}
		if l.Event == nil {
			// A reply with nothing waiting for it. Not fatal, and not ours.
			continue
		}
		return *l.Event, nil
	}
}
