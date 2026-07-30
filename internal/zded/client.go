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

// queueMax bounds the events kept for a caller that is not reading them.
const queueMax = 64

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
			// Bounded, because a client that subscribes and only ever calls
			// would otherwise grow this for the life of the session. Dropping
			// the oldest is right for a picker request: a stale one is a
			// surface nobody is waiting for any more.
			if len(c.queued) >= queueMax {
				c.queued = c.queued[1:]
			}
			c.queued = append(c.queued, *l.Event)
			continue
		}
		return Response{Ok: l.Ok, Error: l.Error}, nil
	}
}

// NextEvent waits for the next event, for as long as it takes. A stream is
// meant to sit idle: six seconds between two presses of a key is ordinary, and
// this used to give up after five, which made the exported API useless for the
// one thing it exists for. Close the connection to stop waiting, or use
// NextEventBefore.
func (c *Client) NextEvent() (Event, error) {
	return c.NextEventBefore(time.Time{})
}

// NextEventBefore is NextEvent with a deadline, for a caller that would rather
// fail than wait - a test, mostly. The zero time means no deadline.
//
// Events already passed while reading a reply come back first, in order.
func (c *Client) NextEventBefore(deadline time.Time) (Event, error) {
	if len(c.queued) > 0 {
		ev := c.queued[0]
		c.queued = c.queued[1:]
		return ev, nil
	}
	// Cleared rather than left alone: Call sets an absolute deadline of its own,
	// and inheriting a spent one would fail this read instantly.
	if err := c.conn.SetDeadline(deadline); err != nil {
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
