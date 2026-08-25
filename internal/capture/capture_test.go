package capture

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Fixed values keep expected capture names literal and readable in tests.
var noon = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func noonNow() time.Time { return noon }

const noonName = "zde-20260820T120000Z.png"

const writePNG = `printf 'pixels\000\000\000\000IEND\256B\140\202' > "$3"`

var png = append([]byte("the compositor's pixels"), pngEnd...)

// fakeShots records requests and emits the events a compositor would.
type fakeShots struct {
	mu     sync.Mutex
	asked  []string
	events chan string
	do     func(path string) (event string, send bool)

	subscribeErr error
	actionErr    error
}

func newFake(action func(string) (string, bool)) *fakeShots {
	return &fakeShots{events: make(chan string, 4), do: action}
}

func wrote(path string) (string, bool) {
	os.WriteFile(path, png, 0o600) //nolint:errcheck // the test reads it back
	return path, true
}

func refusedShot(string) (string, bool) { return "", true }

func (fake *fakeShots) ScreenshotWindow(path string) error { return fake.shot("window", path) }
func (fake *fakeShots) ScreenshotScreen(path string) error { return fake.shot("screen", path) }

func (fake *fakeShots) shot(kind, path string) error {
	fake.mu.Lock()
	fake.asked = append(fake.asked, kind+" "+path)
	fake.mu.Unlock()
	if fake.actionErr != nil {
		return fake.actionErr
	}
	if fake.do != nil {
		if event, send := fake.do(path); send {
			fake.events <- event
		}
	}
	return nil
}

func (fake *fakeShots) Captured() (<-chan string, error) {
	if fake.subscribeErr != nil {
		return nil, fake.subscribeErr
	}
	return fake.events, nil
}

func (fake *fakeShots) all() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.asked...)
}

func quick(t *testing.T) {
	t.Helper()
	region, prompt := regionWait, promptWait
	t.Cleanup(func() { regionWait, promptWait = region, prompt })
	regionWait, promptWait = 150*time.Millisecond, 150*time.Millisecond
}

func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func captureNamesIn(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	for _, name := range namesIn(t, dir) {
		if nameShape.MatchString(name) {
			names = append(names, name)
		}
	}
	return names
}

func plant(t *testing.T, dir, name string, at time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	data := []byte(name)
	if nameShape.MatchString(name) {
		data = append(data, pngEnd...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}
