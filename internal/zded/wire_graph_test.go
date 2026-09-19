package zded

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellSourceFollowsOnlyInstantiatedLocalComponents(t *testing.T) {
	directory := t.TempDir()
	for name, source := range map[string]string{
		"shell.qml": `Scope {
    property string marker: "root-marker"
    Used {} Used {}
    Item {} // Built-in types have no local file.
    // Bluetooth {}
    /* Unused {} */
    property string example: "Bluetooth {}"
    property string other: 'Unused {}'
}`,
		"Used.qml": `Inherited { property string marker: "used-marker"; Leaf {} }`,
		"Inherited.qml": `Scope {
    property string marker: "inherited-marker"
    Used {} // A cycle must not duplicate sources or recurse forever.
}`,
		"Leaf.qml": `QtObject { property string marker: "leaf-marker" }`,
		// Merely declaring files, even ones with their own dependencies, adds no edges.
		"Unused.qml":    `Bluetooth { property string marker: "unused-marker" }`,
		"Bluetooth.qml": `Item { property string marker: "bluetooth-marker" }`,
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := shellSource(os.DirFS(directory), "shell.qml")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"root-marker", "used-marker", "inherited-marker", "leaf-marker"} {
		if count := strings.Count(string(source), marker); count != 1 {
			t.Errorf("reachable %q appeared %d times, want once", marker, count)
		}
	}
	for _, marker := range []string{"unused-marker", "bluetooth-marker"} {
		if strings.Contains(string(source), marker) {
			t.Errorf("uninstantiated component %q was scanned", marker)
		}
	}
}

func TestShellSourceReportsReadFailures(t *testing.T) {
	directory := t.TempDir()
	files := os.DirFS(directory)
	if _, err := shellSource(files, "shell.qml"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing entry error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "shell.qml"), []byte("Scope { Broken {} }"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "Broken.qml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := shellSource(files, "shell.qml"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unreadable local component was treated as a built-in: %v", err)
	}
}
