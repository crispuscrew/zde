package zded

import (
	"encoding/json"
	"strings"
)

const askFrame = "zde-ask 1"

const (
	askWhoPerson = "person"
	askWhoTier   = "tier"
)

type askTurn struct {
	Who  string `json:"who"`
	Text string `json:"text"`
}

// askDoc preserves bare-question stdin unless its first line would forge the frame.
// Conversation roles come from position, with JSON escaping preventing forged turns.
func askDoc(prior []string, question string) string {
	if len(prior) == 0 && !framed(question) {
		return question
	}
	var b strings.Builder
	b.WriteString(askFrame)
	b.WriteByte('\n')
	for i, text := range prior {
		who := askWhoPerson
		if i%2 == 1 {
			who = askWhoTier
		}
		writeTurn(&b, who, text)
	}
	writeTurn(&b, askWhoPerson, question)
	return b.String()
}

// overKiB rounds up so one byte over the limit does not report the limit itself.
func overKiB(n int) int {
	return (n + 1<<10 - 1) >> 10
}

func framed(question string) bool {
	line, _, _ := strings.Cut(question, "\n")
	return line == askFrame
}

// writeTurn disables HTML escaping so pasted markup uses its real context budget.
// Encoding strings to a strings.Builder cannot fail or silently skip a turn.
func writeTurn(b *strings.Builder, who, text string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(askTurn{Who: who, Text: text})
}
