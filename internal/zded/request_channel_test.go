package zded

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequestChannelQt(t *testing.T) {
	binary, err := exec.LookPath("quickshell")
	if err != nil {
		binary, err = exec.LookPath("qs")
	}
	if err != nil {
		if os.Getenv("ZDE_REQUIRE_QUICKSHELL") == "1" {
			t.Fatal("Quickshell is required for the request-channel regression gate")
		}
		t.Skip("Quickshell unavailable; nix build .#zde-shell requires these Qt tests")
	}
	fixture := os.Getenv("ZDE_CHANNEL_FIXTURE")
	if fixture == "" {
		fixture = filepath.Join("..", "..", "tests", "shell", "RequestChannelTest.qml")
	}
	fixture, err = filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fixture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"modern", "legacy", "legacy-ack", "disconnect", "timeout", "restart", "retired-stream"} {
		t.Run(scenario, func(t *testing.T) { runChannelQt(t, binary, fixture, scenario) })
	}
}

func runChannelQt(t *testing.T, binary, fixture, scenario string) {
	t.Helper()
	// Test names can exceed the Unix socket path limit; keep this private path short.
	directory, err := os.MkdirTemp("/tmp", "zc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	for _, name := range []string{"runtime/zde", "config", "state", "cache"} {
		if err := os.MkdirAll(filepath.Join(directory, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("unix", filepath.Join(directory, "runtime", "zde", "zded.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	peerDone := make(chan error, 1)
	go func() { peerDone <- runChannelPeer(ctx, listener, scenario) }()
	command := exec.CommandContext(ctx, binary, "--no-color", "--path", stageChannelFixture(t, directory, fixture))
	command.WaitDelay = 2 * time.Second
	command.Env = channelEnvironment(directory, scenario)
	output, runErr := command.CombinedOutput()
	listener.Close()
	select {
	case peerErr := <-peerDone:
		if peerErr != nil {
			t.Errorf("fake peer: %v", peerErr)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Error("fake peer did not stop after Qt exited")
	}
	marker := []byte("ZDE_CHANNEL_PASS:" + scenario)
	if runErr != nil || !bytes.Contains(output, marker) || bytes.Contains(output, []byte("ZDE_CHANNEL_FAIL:")) {
		t.Fatalf("Qt request channel %s: %v\n%s", scenario, runErr, output)
	}
	t.Logf("real Qt scenario passed: %s", scenario)
}

func channelEnvironment(directory, scenario string) []string {
	settings := map[string]string{
		"HOME": directory, "XDG_RUNTIME_DIR": filepath.Join(directory, "runtime"),
		"XDG_CONFIG_HOME": filepath.Join(directory, "config"), "XDG_STATE_HOME": filepath.Join(directory, "state"),
		"XDG_CACHE_HOME": filepath.Join(directory, "cache"), "QT_QPA_PLATFORM": "offscreen",
		"QT_QUICK_BACKEND": "software", "ZDE_CHANNEL_SCENARIO": scenario,
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + filepath.Join(directory, "no-session-bus"),
	}
	var environment []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, replace := settings[name]; !replace {
			environment = append(environment, entry)
		}
	}
	for name, value := range settings {
		environment = append(environment, fmt.Sprintf("%s=%s", name, value))
	}
	return environment
}

// Quickshell only imports within its config root. Preserve the original relative
// imports below a private root; execute exact copies of the production QML.
func stageChannelFixture(t *testing.T, directory, fixture string) string {
	t.Helper()
	fixtures := filepath.Dir(fixture)
	sourceRoot := filepath.Dir(filepath.Dir(fixtures))
	for name, source := range map[string]string{
		"shell": filepath.Join(sourceRoot, "shell"), "tests/shell": fixtures,
	} {
		if err := os.CopyFS(filepath.Join(directory, name), os.DirFS(source)); err != nil {
			t.Fatal(err)
		}
	}
	entry := filepath.Join(directory, "shell.qml")
	if err := os.WriteFile(entry, []byte("import \"tests/shell\"\nRequestChannelTest {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return entry
}
