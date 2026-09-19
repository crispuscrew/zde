package zded

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeTiers isolates configuration from the host and returns the test's home directory.
func writeTiers(t *testing.T, tiers map[string][]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, "config")
	t.Setenv("XDG_CONFIG_HOME", config)
	if err := os.MkdirAll(filepath.Join(config, "zde"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tiers)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "zde", askFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// askAll exercises the real socket: ask.run is connection-owned, not Dispatch-only.
func askAll(t *testing.T, path, tier, question string, prior ...string) (text, failure string) {
	t.Helper()
	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodAskRun, nil, append([]string{tier, question}, prior...)...); err != nil {
		return "", err.Error()
	}
	var b strings.Builder
	for {
		ev, err := c.NextEventBefore(time.Now().Add(20 * time.Second))
		if err != nil {
			t.Fatalf("waiting for the answer: %v", err)
		}
		if ev.Kind != EventAskText {
			continue
		}
		b.WriteString(ev.Text)
		if ev.Done {
			return b.String(), ev.Error
		}
	}
}

func askServer(t *testing.T) string {
	t.Helper()
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	return serve(t, s)
}

// Wait for the subprocess to publish its PID before testing shutdown, rather than racing exec.
func tierPid(t *testing.T, path string) int {
	t.Helper()
	for i := 0; i < 500; i++ {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatalf("the tier wrote %q where a pid was expected", raw)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the tier never said where it was (%s)", path)
	return 0
}
