package zded

import (
	"io"
	"strings"
	"unicode/utf8"
)

func askDone(k *sink, reason string) {
	k.sendWithin(Event{Kind: EventAskText, Done: true, Error: reason}, askSendWait)
}

// pump sends available bytes without waiting for newlines; incomplete UTF-8 waits
// for the next read. Delivery failure or size overflow cancels the subprocess.
func pump(k *sink, r io.Reader, stop func()) int {
	buf := make([]byte, 4096)
	var carry []byte
	said := 0
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			keep := partialRune(chunk)
			carry = append([]byte(nil), chunk[len(chunk)-keep:]...)
			if chunk = chunk[:len(chunk)-keep]; len(chunk) > 0 {
				said += len(chunk)
				if err := k.sendWithin(Event{Kind: EventAskText, Text: string(chunk)}, askSendWait); err != nil {
					stop()
					return said
				}
				if said >= askMax {
					stop()
					return said
				}
			}
		}
		if err != nil {
			if len(carry) > 0 {
				said += len(carry)
				k.sendWithin(Event{Kind: EventAskText, Text: string(carry)}, askSendWait)
			}
			return said
		}
	}
}

func partialRune(b []byte) int {
	for i := len(b) - 1; i >= 0 && len(b)-i < utf8.UTFMax; i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if r, size := utf8.DecodeRune(b[i:]); r == utf8.RuneError && size <= 1 {
			return len(b) - i
		}
		return 0
	}
	return 0
}

// drain consumes all stderr to prevent a full pipe blocking the tier; storage is bounded.
func drain(r io.Reader, max int) []byte {
	var kept []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 && len(kept) < max {
			kept = append(kept, buf[:min(n, max-len(kept))]...)
		}
		if err != nil {
			return kept
		}
	}
}

func complaintOf(complaint []byte, err error) string {
	msg := strings.TrimSpace(string(complaint))
	if msg == "" {
		return err.Error()
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "..."
	}
	return msg
}
