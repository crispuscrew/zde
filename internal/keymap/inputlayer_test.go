package keymap

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The input layer and the keymap are two files that have to agree, and nothing
// makes them. kanata --check passes on a layer that emits the wrong key,
// because it has no idea what the keys mean; the keymap parses fine against a
// layer that emits nothing of the sort, because it never reads one.
//
// So this reads both. It walks the band layer against defsrc, position by
// position, and holds every chord to the same story from both ends: the
// physical key kanata rewrites has to be the one the bind's via names, and the
// key it emits has to be the one niri is told to bind.
//
// Swap two keys in the layer and next becomes prev with nothing else noticing.
// That is what this is for.
func TestInputLayerAgreesWithTheKeymap(t *testing.T) {
	emits := bandLayer(t) // physical key -> the key niri sees
	km, err := Load("../../common/keymap/keymap.yaml")
	if err != nil {
		t.Fatal(err)
	}

	bound := map[string]string{} // emitted key -> the chord that should produce it
	for _, b := range km.Binds {
		if b.Via == "" {
			continue
		}
		bound[strings.ToLower(b.Key)] = b.Via

		// Tab+j is the band layer's key held, then j. Only the last part is a
		// key the layer maps; the rest is what holds it.
		parts := strings.Split(b.Via, "+")
		physical := strings.ToLower(parts[len(parts)-1])
		got, mapped := emits[physical]
		if !mapped {
			t.Errorf("%s: the keymap says %s produces %s, and the input layer does not map %s at all",
				b.Action, b.Via, b.Key, physical)
			continue
		}
		if got != strings.ToLower(b.Key) {
			t.Errorf("%s: the keymap binds %s to %s, but the input layer sends %s to %s",
				b.Action, b.Via, b.Key, physical, got)
		}
	}

	// And nothing in the layer emits a key nothing binds, which would be a
	// chord that quietly does nothing.
	for physical, emitted := range emits {
		if _, ok := bound[emitted]; !ok {
			t.Errorf("the input layer sends %s to %s, and no bind listens for it", physical, emitted)
		}
	}
}

var (
	blockRe = regexp.MustCompile(`(?s)\(\s*(defsrc|deflayer\s+band)\s(.*?)\)`)
	commRe  = regexp.MustCompile(`;;[^\n]*`)
)

// bandLayer reads input/kanata.kbd and pairs defsrc with the band layer by
// position, which is how kanata reads them too.
func bandLayer(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../input/kanata.kbd")
	if err != nil {
		t.Fatal(err)
	}
	blocks := blockRe.FindAllStringSubmatch(commRe.ReplaceAllString(string(raw), ""), -1)
	var src, band []string
	for _, b := range blocks {
		keys := strings.Fields(b[2])
		if strings.HasPrefix(b[1], "defsrc") {
			src = keys
		} else {
			band = keys
		}
	}
	if len(src) == 0 || len(band) == 0 {
		t.Fatalf("could not read defsrc (%d keys) and the band layer (%d keys)", len(src), len(band))
	}
	if len(src) != len(band) {
		t.Fatalf("defsrc has %d keys and the band layer %d; kanata pairs them by position", len(src), len(band))
	}
	out := map[string]string{}
	for i, key := range src {
		// Transparent: this layer says nothing about that key.
		if band[i] == "_" {
			continue
		}
		out[key] = band[i]
	}
	return out
}
