package bus

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
	conn, err := Connect(50*time.Millisecond, func(...dbus.ConnOption) (*dbus.Conn, error) {
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

// The socket an abandoned open eventually produces is closed by whoever is left
// holding it, which is nobody. Break this and every question asked while a bus
// is slow leaks a socket and the goroutine reading it, for the life of a daemon
// that is meant to run for weeks.
func TestAConnectionThatArrivesTooLateIsClosed(t *testing.T) {
	hang := make(chan struct{})
	p := &countingPipe{}
	late, err := dbus.NewConn(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(20*time.Millisecond, func(...dbus.ConnOption) (*dbus.Conn, error) {
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
	open := func(...dbus.ConnOption) (*dbus.Conn, error) { return nil, want }
	if _, err := Connect(time.Second, open); !errors.Is(err, want) {
		t.Errorf("err = %v, want what the dial said", err)
	}
}

// The real shape of a wedged bus, and the one walking away could not survive.
//
// A socket that is up and accepts leaves the handshake parked in a read nothing
// will ever satisfy, so the goroutine inside it never returns, so nothing ever
// closes what it opened. Giving up on the wait is not giving up on the socket,
// and the difference is a descriptor an attempt that a daemon holds until the
// process ends: forty presses of the power key ran a session to forty-seven of
// them, and a session with no descriptors left has no power menu, no wifi list
// and no bar.
//
// Counted in descriptors because that is the thing that ran out. The goroutines
// go with them and are counted too, with slack, since a test binary has its own.
func TestABusThatAcceptsAndSaysNothingIsLetGoOfEveryTime(t *testing.T) {
	addr := blackhole(t)
	open := func(opts ...dbus.ConnOption) (*dbus.Conn, error) { return dbus.Dial(addr, opts...) }

	// One first, so that whatever the library allocates once has been allocated
	// before the count that matters is taken.
	if _, err := Connect(20*time.Millisecond, open); err == nil {
		t.Fatal("a bus that never said anything came back as connected")
	}
	files, routines := settled(t)

	for i := 0; i < 20; i++ {
		if _, err := Connect(20*time.Millisecond, open); err == nil {
			t.Fatalf("attempt %d against a silent bus came back as connected", i)
		}
	}

	after, running := settled(t)
	if after > files {
		t.Errorf("twenty attempts against a bus that never answered left %d descriptors open, from %d: "+
			"the socket under an abandoned handshake is never closed", after-files, files)
	}
	// Three a time was what the leak cost, so anything near twenty attempts'
	// worth is the same fault seen from the other side.
	if running > routines+5 {
		t.Errorf("twenty attempts left %d goroutines running, from %d", running-routines, routines)
	}
}

// settled is the descriptor and goroutine count once the last attempt has
// finished letting go, or what it was still at after a second of waiting.
//
// A loop rather than one reading: the close happens on the way out of Connect,
// but the goroutines it unblocks are scheduled whenever the runtime feels like
// it, and a test that read the numbers at once would fail on a busy machine
// while the code was right.
func settled(t *testing.T) (files, routines int) {
	t.Helper()
	files, routines = openFiles(t), runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
		f, r := openFiles(t), runtime.NumGoroutine()
		if f >= files && r >= routines {
			return files, routines
		}
		files, routines = f, r
	}
	return files, routines
}

// openFiles is how many descriptors this process has open. /proc, because that
// is where the truth about a leaked socket is and this only ever runs on Linux.
func openFiles(t *testing.T) int {
	t.Helper()
	names, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd to count descriptors in, so a leaked socket cannot be seen: %v", err)
	}
	return len(names)
}

// blackhole is a bus that takes a connection and then says nothing: the socket
// is up, the connect succeeds, and the handshake waits for a greeting that
// never comes. A wedged system bus is this, and so is one whose daemon is alive
// but stuck on a disk.
//
// It listens and never accepts, which is not a detail. A unix connect completes
// as soon as the kernel has queued it on the listener's backlog, so the client
// is connected and writable with nothing on the other end - and the queued half
// is a kernel object rather than a descriptor in this process. Accepting would
// put both ends of every attempt in the one process that is counting
// descriptors, and a leak of one would read as two.
func blackhole(t *testing.T) string {
	t.Helper()
	// Not t.TempDir: a unix socket path is capped at about 108 bytes, and a
	// directory named after a test this long has spent most of that already.
	dir, err := os.MkdirTemp("", "zde-bus-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ln.Close()
		os.RemoveAll(dir)
	})
	return "unix:path=" + path
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
