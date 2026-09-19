package zded

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// brokenConn models failed and partial writes, recording whether the sink closes it.
type brokenConn struct {
	mu     sync.Mutex
	short  bool
	closed bool
	wrote  int
}

func (b *brokenConn) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.short {
		b.wrote += len(p) / 2
		return len(p) / 2, nil
	}
	return 0, errClosed
}

func (b *brokenConn) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *brokenConn) shut() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// A partial JSON frame cannot be repaired by writing the next reply onto it.
func TestASinkThatCannotTakeAWholeLineIsClosed(t *testing.T) {
	for _, c := range []struct {
		name string
		conn *brokenConn
	}{
		{"a write that fails", &brokenConn{}},
		{"a write that stops halfway", &brokenConn{short: true}},
	} {
		k := &sink{w: c.conn}
		if err := k.sendWithin(Event{Kind: EventAskText, Text: "half an answer"}, time.Second); err == nil {
			t.Errorf("%s: sendWithin said the line went out", c.name)
		}
		if !c.conn.shut() {
			t.Errorf("%s: the connection was left open for the next line", c.name)
		}
	}
}

func TestAShortWriteIsAFailure(t *testing.T) {
	conn := &brokenConn{short: true}
	k := &sink{w: conn}
	err := k.sendWithin(Event{Kind: EventAskText, Text: "half an answer"}, time.Second)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("a half-written line came back as %v", err)
	}
}
