package zded

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crispuscrew/zde/internal/journal"
)

// Private answers must never reach an unrelated event subscriber.
func TestAnAnswerGoesOnlyToTheConnectionThatAsked(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("echo")})
	path := askServer(t)

	watcher, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	var ack string
	if err := watcher.Call(MethodEvents, &ack); err != nil {
		t.Fatalf("subscribing: %v", err)
	}

	text, failure := askAll(t, path, TierProvider, "the sort of question nobody wants repeated")
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if !strings.Contains(text, "nobody wants repeated") {
		t.Fatalf("the tier did not answer: %q", text)
	}

	if ev, err := watcher.NextEventBefore(time.Now().Add(250 * time.Millisecond)); err == nil {
		t.Errorf("a subscriber that asked nothing was sent a %q event carrying %q", ev.Kind, ev.Text)
	}
}

// Isolate and compare every XDG directory and temporary storage; no transcript may persist.
func TestAskWritesNothingToDisk(t *testing.T) {
	home := writeTiers(t, map[string][]string{TierProvider: fakeTier("echo")})
	state := filepath.Join(home, "state")
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	jrn, err := journal.Open(filepath.Join(state, "zde", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer jrn.Close()
	s := New("test", jrn, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	path := serve(t, s)
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_RUNTIME_DIR", tmp)

	before := tree(t, home)
	text, failure := askAll(t, path, TierProvider, "the sort of question nobody wants written down")
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if !strings.Contains(text, "nobody wants written down") {
		t.Fatalf("the tier did not answer: %q", text)
	}
	after := tree(t, home)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("asking changed what is on disk:\nbefore %v\nafter  %v", before, after)
	}
}

// Compare contents, not only filenames: appending to a journal must also be detected.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
