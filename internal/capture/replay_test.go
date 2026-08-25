package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeReplayCLI(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, replayCLI), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestReplayClipNamesTheFileTheRecorderFinished(t *testing.T) {
	runtime := t.TempDir()
	clip := filepath.Join(t.TempDir(), "Replay_2026-08-22_12-00-00.mp4")
	if err := os.WriteFile(clip, []byte("finished mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("ZDE_TEST_REPLAY", clip)
	t.Setenv("ZDE_TEST_ARGS", args)
	fakeReplayCLI(t, `printf '%s\n' "$*" >"$ZDE_TEST_ARGS"
	printf '%s\n' "$ZDE_TEST_REPLAY"
`)

	got, err := ReplayClip()
	if err != nil {
		t.Fatal(err)
	}
	if got != clip {
		t.Errorf("ReplayClip = %q, want %q", got, clip)
	}
	called, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	want := "-ipc " + filepath.Join(runtime, replaySocket) + " save-replay"
	if strings.TrimSpace(string(called)) != want {
		t.Errorf("gsr-cli got %q, want %q", strings.TrimSpace(string(called)), want)
	}
}

func TestReplayClipCarriesTheRecordersRefusal(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	fakeReplayCLI(t, "printf '%s\\n' 'the replay service is not running' >&2\nexit 1\n")

	_, err := ReplayClip()
	if err == nil || !strings.Contains(err.Error(), "the replay service is not running") {
		t.Errorf("ReplayClip = %v, want the recorder's refusal", err)
	}
}

func TestReplayClipSaysHowToEnableItsRecorder(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	_, err := ReplayClip()
	if err == nil || !strings.Contains(err.Error(), "zde.capture.replay.enable") {
		t.Errorf("ReplayClip = %v, want the option that starts the recorder", err)
	}
}
