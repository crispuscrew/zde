package bus

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// A bus that accepts and then says nothing is what this whole package is for.
// Break the bound and the caller waits for as long as the other end feels like
// waiting: a keybind that never answers, and a Close that sits behind it, which
// is how a zded came to ignore SIGTERM.
func TestAConnectThatNeverFinishesIsGivenUpOn(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)

	start := time.Now()
	conn, err := Connect(50*time.Millisecond, func() (*dbus.Conn, error) {
		<-hang
		return nil, errors.New("too late")
	})
	if err == nil {
		t.Fatalf("a connect that never finished came back with %v", conn)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("giving up took %s, so the bound is not the bound", took)
	}
}

// The connection an abandoned dial eventually opens is closed by whoever is
// left holding it, which is nobody. Break this and every question asked while a
// bus is slow leaks a socket and the goroutine reading it, for the life of a
// daemon that is meant to run for weeks.
func TestAConnectionThatArrivesTooLateIsClosed(t *testing.T) {
	hang := make(chan struct{})
	p := &countingPipe{}
	late, err := dbus.NewConn(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(20*time.Millisecond, func() (*dbus.Conn, error) {
		<-hang
		return late, nil
	}); err == nil {
		t.Fatal("the slow connect was waited for after all")
	}
	close(hang)

	for i := 0; i < 200; i++ {
		if p.wasClosed() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the connection nobody is waiting for was left open")
}

// A connect that finishes in time is handed back as it is, error and all.
func TestAConnectThatFinishesIsHandedBack(t *testing.T) {
	want := errors.New("no such bus")
	if _, err := Connect(time.Second, func() (*dbus.Conn, error) { return nil, want }); !errors.Is(err, want) {
		t.Errorf("err = %v, want what the dial said", err)
	}
}

// countingPipe is something a dbus.Conn can be built on without a bus: it
// carries no messages, and the only thing asked of it is whether it was closed.
type countingPipe struct {
	mu     sync.Mutex
	closed bool
}

func (p *countingPipe) Read([]byte) (int, error)    { return 0, io.EOF }
func (p *countingPipe) Write(b []byte) (int, error) { return len(b), nil }

func (p *countingPipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *countingPipe) wasClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}
