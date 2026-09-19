package zded

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// Requests have method first; unrelated properties or HTTP objects are not requests.
	methodInQML = regexp.MustCompile(`\{\s*"?method"?\s*:\s*"([a-zA-Z0-9.-]+)"`)
	// Helpers and picker assignments carry dotted methods, not arbitrary filenames.
	methodHandedOnInQML = regexp.MustCompile(`(?:\w+Method\s*[:=]|\b(?:ask|send|call|command)\s*\()\s*"([a-z][a-z0-9]*\.[a-z0-9.-]+)"`)
	kindInQML           = regexp.MustCompile(`\bevent\.kind\s*(?:===|!==|==|!=)\s*"([a-zA-Z0-9.-]+)"`)
	kindInGo            = regexp.MustCompile(`\bEvent[A-Za-z0-9]*\s*=\s*"([a-zA-Z0-9.-]+)"`)
	qmlType             = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\s*\{`)
	qmlNonCode          = regexp.MustCompile("(?s)//[^\n]*|/\\*.*?\\*/|\"(?:\\\\.|[^\"\\\\])*\"|'(?:\\\\.|[^'\\\\])*'|`(?:\\\\.|[^`\\\\])*`")
)

// shellSource follows local object instantiations, including a file's custom root
// type. Missing files are platform types. visited bounds cycles and repeated uses.
// This deliberately covers static sibling components, not dynamic URLs or imports.
func shellSource(files fs.FS, entry string) ([]byte, error) {
	pending := []string{entry}
	visited := map[string]bool{}
	var combined []byte
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		if visited[name] {
			continue
		}
		visited[name] = true
		source, err := fs.ReadFile(files, name)
		if name != entry && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		combined = append(combined, source...)
		combined = append(combined, '\n')
		code := qmlNonCode.ReplaceAll(source, []byte(" "))
		for _, match := range qmlType.FindAllSubmatch(code, -1) {
			pending = append(pending, string(match[1])+".qml")
		}
	}
	return combined, nil
}

func readShell(t *testing.T) []byte {
	t.Helper()
	source, err := shellSource(os.DirFS(filepath.Join("..", "..", "shell")), "shell.qml")
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func readQML(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "shell", name)
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return source
}

func kindsIn(pattern *regexp.Regexp, source []byte) map[string]bool {
	out := map[string]bool{}
	for _, match := range pattern.FindAllSubmatch(source, -1) {
		out[string(match[1])] = true
	}
	return out
}

// Event constants can live beside any backend surface, not only in events.go.
func eventKinds(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for kind := range kindsIn(kindInGo, source) {
			kinds[kind] = true
		}
	}
	if len(kinds) == 0 {
		t.Fatal("no Event constants found: the contract scan would check nothing")
	}
	return kinds
}
