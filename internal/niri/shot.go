package niri

import (
	"encoding/json"
	"fmt"
	"time"
)

// Shots is niri's screenshot half.
//
// Two connections, because subscribing spends one: after EventStream niri only
// writes events on that socket, and this package's reader goroutine consumes
// everything on it (Captured). So there is a connection to ask on and a
// connection to listen on, and both are opened before anything is asked - the
// event that says where the file went is sent once, and a subscription taken
// out afterwards would miss it.
type Shots struct {
	act *Client
	ev  *Client
}

// DialShots opens both connections to the socket named by $NIRI_SOCKET.
func DialShots() (*Shots, error) {
	act, err := Dial()
	if err != nil {
		return nil, err
	}
	ev, err := Dial()
	if err != nil {
		act.Close()
		return nil, err
	}
	return &Shots{act: act, ev: ev}, nil
}

// Close closes both.
func (s *Shots) Close() error {
	s.ev.Close() //nolint:errcheck // the stream is one-way and already ending
	return s.act.Close()
}

// niri's two automatic screenshot actions, with every field its IPC struct has
// written out, because none of them has a default on the wire.
//
// The region is not here. niri's third action opens its interactive picker,
// which saves the frame that goes to the monitor and therefore keeps a window
// that was blocked from capture - so zde takes a region another way
// (internal/capture, region.go). These two render niri's ScreenCapture frame
// and the block covers them. niri-ipc's Action derives
// plain Serialize/Deserialize and the defaults are the KDL bind parser's
// (niri-config, binds.rs), which is why a bare {"Screenshot":{}} comes back as
// "error parsing request" and the key works - the asymmetry the palette used to
// trip over (internal/keymap, performs). `path` is required too: serde does not
// supply a default for an Option inside a struct variant, so it is sent as null
// when there is nothing to say.
//
// show_pointer is niri's own bind default for each action, so a key that now
// spawns zde takes the same picture it took when it was a native. The three do
// not agree about it - ScreenshotWindow's default is false and the other two are
// true - so each is written out rather than shared.
//
// path is absolute or niri refuses it. It is also what turns off the two things
// niri does for its own screenshot-path and not for an explicit one: no strftime
// over the string, and no creating the parent directory (niri 26.04,
// src/niri.rs, save_screenshot). The caller makes the directory.

// ScreenshotWindow writes the focused window to path.
func (s *Shots) ScreenshotWindow(path string) error {
	return s.act.Action(map[string]any{
		"ScreenshotWindow": map[string]any{
			"id": nil, "write_to_disk": true, "show_pointer": false, "path": path,
		},
	}, "ScreenshotWindow")
}

// ScreenshotScreen writes the focused monitor to path.
func (s *Shots) ScreenshotScreen(path string) error {
	return s.act.Action(map[string]any{
		"ScreenshotScreen": map[string]any{
			"write_to_disk": true, "show_pointer": true, "path": path,
		},
	}, "ScreenshotScreen")
}

// Captured reports the file each screenshot niri finishes was written to, and
// empty for one it did not write.
//
// It exists because the reply to the action says nothing about the file. niri
// encodes and saves in a thread and save_screenshot returns Ok before that
// thread runs, so "Handled" means the request parsed and nothing more: a
// directory that is not writable, or a disk with no room, is a warning in niri's
// own log and a success on the socket. This event is the other end of that -
// niri sends it when the thread is done, carrying the path when the write landed
// and null when it did not.
//
// Not part of the state niri replays to a new subscriber (niri-ipc, state.rs:
// no part of EventStreamState claims it), so a subscription taken out now
// answers about the screenshot asked for next and not about an older one.
func (s *Shots) Captured() (<-chan string, error) { return s.ev.captured() }

func (c *Client) captured() (<-chan string, error) {
	if err := c.subscribe(); err != nil {
		return nil, err
	}
	out := make(chan string, 4)
	go func() {
		defer close(out)
		for {
			line, err := c.r.ReadBytes('\n')
			if err != nil {
				return
			}
			var event struct {
				Shot *struct {
					Path *string `json:"path"`
				} `json:"ScreenshotCaptured"`
			}
			if err := json.Unmarshal(line, &event); err != nil || event.Shot == nil {
				continue // some other event, or one this cannot read
			}
			select {
			case out <- deref(event.Shot.Path):
			default: // a slow reader is not a reason to stall niri
			}
		}
	}()
	return out, nil
}

// subscribe turns this connection into an event stream and takes the deadline
// off it: an idle session is quiet for hours, and a stream that timed out
// because nothing happened would be a bug that looks like the compositor dying.
func (c *Client) subscribe() error {
	if _, err := c.call("EventStream", "EventStream"); err != nil {
		return err
	}
	if err := c.conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("niri: event stream: %w", err)
	}
	return nil
}
