package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Latest is the newest complete capture zde wrote in dir. Names from niri or a
// person are excluded so an implicit send-to cannot hand away an unrelated file.
func Latest(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("no captures to send: %w", err)
	}
	var best string
	for _, entry := range entries {
		if entry.IsDir() || !nameShape.MatchString(entry.Name()) {
			continue
		}
		if !completePNG(filepath.Join(dir, entry.Name())) {
			continue
		}
		// The name is the creation order ZDE assigned. mtime is mutable metadata
		// and must not make an old capture become the implicit send target.
		if best == "" || newerName(entry.Name(), best) {
			best = entry.Name()
		}
	}
	if best == "" {
		return "", fmt.Errorf("nothing in %s was captured by zde: take one first, "+
			"or name the file to send", dir)
	}
	return filepath.Join(dir, best), nil
}

func newerName(left, right string) bool {
	leftMatch := nameShape.FindStringSubmatch(left)
	rightMatch := nameShape.FindStringSubmatch(right)
	if leftMatch[1] != rightMatch[1] {
		return leftMatch[1] > rightMatch[1]
	}
	sequence := func(match []string) int {
		if match[2] == "" {
			return 1
		}
		number, _ := strconv.Atoi(match[2])
		return number
	}
	return sequence(leftMatch) > sequence(rightMatch)
}
