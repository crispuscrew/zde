package zded

import (
	"encoding/json"
	"errors"
	"io"
	"time"
)

// errSinkBusy means no write began; the current writer owns liveness detection.
var errSinkBusy = errors.New("zded: the connection is busy with another line")

// sendWait bounds a broadcast's total latency, including gate contention.
const sendWait = 200 * time.Millisecond

// replyWait matches Client.Call's deadline; every gate holder must be bounded.
const replyWait = 5 * time.Second

// reply closes a busy connection rather than leave an unanswered request on a live socket.
func (k *sink) reply(resp Response) {
	if err := k.writeWithin(responseLine(resp), replyWait); errors.Is(err, errSinkBusy) {
		if c, ok := k.w.(io.Closer); ok {
			c.Close()
		}
	}
}

// replyTo takes the ID by value; async replies must not share a mutable current ID.
func (k *sink) replyTo(requestID string, resp Response) {
	resp.ID = requestID
	k.reply(resp)
}

func (k *sink) send(ev Event) error { return k.sendWithin(ev, sendWait) }

func (k *sink) sendWithin(ev Event, wait time.Duration) error {
	line, err := json.Marshal(struct {
		Event Event `json:"event"`
	}{ev})
	if err != nil {
		return err
	}
	return k.writeWithin(append(line, '\n'), wait)
}

// writeWithin shares one deadline across gate and write. Failed or partial writes
// close the connection because another line cannot repair a partial JSON frame.
func (k *sink) writeWithin(line []byte, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	if !k.lockBefore(deadline) {
		return errSinkBusy
	}
	defer k.unlock()
	if d, ok := k.w.(interface{ SetWriteDeadline(time.Time) error }); ok {
		d.SetWriteDeadline(deadline)
		defer d.SetWriteDeadline(time.Time{})
	}
	n, err := k.w.Write(line)
	if err == nil && n < len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		if c, ok := k.w.(io.Closer); ok {
			c.Close()
		}
	}
	return err
}

// drop always closes, even when its bounded explanation could not be sent.
func (k *sink) drop(reason string) {
	k.writeWithin(responseLine(Response{Error: reason}), sendWait)
	if c, ok := k.w.(io.Closer); ok {
		c.Close()
	}
}
