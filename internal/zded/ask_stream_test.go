package zded

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Use multibyte text longer than the stream buffer to exercise carried UTF-8 bytes.
const (
	cyrillicWord  = "привет "
	cyrillicTimes = 3000
)

// The gap between first text and completion must survive delivery, not just the text itself.
func TestAskStreamsTheAnswerAsItArrives(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("stream")})
	path := askServer(t)

	c, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodAskRun, nil, TierProvider, "say two things"); err != nil {
		t.Fatalf("ask.run: %v", err)
	}

	start := time.Now()
	var first, end time.Duration
	var b strings.Builder
	for {
		ev, err := c.NextEventBefore(time.Now().Add(20 * time.Second))
		if err != nil {
			t.Fatalf("waiting for the answer: %v", err)
		}
		if ev.Kind != EventAskText {
			continue
		}
		if ev.Text != "" && first == 0 {
			first = time.Since(start)
		}
		b.WriteString(ev.Text)
		if ev.Done {
			end = time.Since(start)
			if ev.Error != "" {
				t.Fatalf("the ask failed: %s", ev.Error)
			}
			break
		}
	}
	if b.String() != "onetwo" {
		t.Errorf("the answer is %q, want both pieces of it", b.String())
	}
	if end-first < fakeTierGap/2 {
		t.Errorf("the first piece arrived %v in and the answer ended %v in: nothing was streamed", first, end)
	}
}

// Both the asking socket and a separate keybind socket must remain responsive.
func TestASlowAskDoesNotHoldUpTheNextKey(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("slow")})
	path := askServer(t)

	asking, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer asking.Close()
	if err := asking.Call(MethodAskRun, nil, TierProvider, "how long is a piece of string"); err != nil {
		t.Fatalf("ask.run: %v", err)
	}

	start := time.Now()
	var st Status
	if err := asking.Call("status", &st); err != nil {
		t.Fatalf("the asking connection stopped answering: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a question on the asking connection waited %v for a running ask", took)
	}

	other, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start = time.Now()
	var focused []string
	if err := other.Call("desk.switch", &focused, "vshop"); err != nil {
		t.Fatalf("desk.switch during an ask: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a desk switch waited %v for a running ask", took)
	}
}

// Incomplete UTF-8 waits for another read; invalid trailing bytes are not silently dropped.
func TestPartialRuneHoldsBackTheEndOfACharacter(t *testing.T) {
	two := []byte("да")  // two bytes per character
	three := []byte("日") // three
	four := []byte("🙂")  // four
	for _, c := range []struct {
		name string
		in   []byte
		want int
	}{
		{"nothing at all", nil, 0},
		{"plain ascii", []byte("hello"), 0},
		{"whole characters", two, 0},
		{"one byte of two", two[:len(two)-1], 1},
		{"two bytes of three", three[:2], 2},
		{"one byte of three", three[:1], 1},
		{"three bytes of four", four[:3], 3},
		{"a byte that is not a character", []byte{'a', 0xff}, 1},
	} {
		if got := partialRune(c.in); got != c.want {
			t.Errorf("%s: held %d bytes back, want %d", c.name, got, c.want)
		}
	}
}

func TestAMultibyteAnswerSurvivesTheReadBuffer(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("cyrillic")})
	text, failure := askAll(t, askServer(t), TierProvider, "say it in russian")
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if want := strings.Repeat(cyrillicWord, cyrillicTimes); text != want {
		t.Errorf("the answer came back %d bytes and %d characters, want %d and %d",
			len(text), utf8.RuneCountInString(text), len(want), utf8.RuneCountInString(want))
	}
	if strings.ContainsRune(text, utf8.RuneError) {
		t.Errorf("the answer carries %d replacement marks", strings.Count(text, string(utf8.RuneError)))
	}
}
