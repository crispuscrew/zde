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
func (c *Client) Call(method string, out any) error {
	if err := c.conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	line, err := json.Marshal(Request{Method: method})
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return err
	}
	raw, err := c.r.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("zde: %s: %w", method, err)
	}
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("zde: %s: reply is not zded's: %w", method, err)
	}
	if resp.Error != "" {
		return fmt.Errorf("zde: %s: %s", method, resp.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Ok, out)
}
