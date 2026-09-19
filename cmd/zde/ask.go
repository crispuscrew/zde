package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/zded"
)

// ask keeps panel questions on the surface; oneshot with text answers here.
// Local and escalate are terminal-only tiers.
func ask(kind, question string) error {
	if question == "" {
		typed, err := questionOnStdin()
		if err != nil {
			return err
		}
		question = typed
	}
	c, err := zded.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	switch kind {
	case "oneshot":
		if question == "" {
			return askSurface(c, "ask.oneshot", "")
		}
		return askRun(c, zded.TierProvider, question)
	case "panel":
		return askSurface(c, "ask.panel", question)
	case zded.TierLocal, zded.TierEscalate:
		if question == "" {
			return fmt.Errorf("zde ask %s takes the question, in an argument or on stdin: say what to ask", kind)
		}
		return askRun(c, kind, question)
	}
	return fmt.Errorf("zde ask takes oneshot, panel, local or escalate, not %q", kind)
}

// questionOnStdin keeps private questions out of argv. Character devices mean
// a terminal or /dev/null: neither should block a keybind waiting for input.
func questionOnStdin() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, questionMax+1))
	if err != nil {
		return "", err
	}
	if len(raw) > questionMax {
		return "", fmt.Errorf("that is more than %d KiB, which is a document rather than a question", questionMax>>10)
	}
	return strings.TrimSpace(string(raw)), nil
}

// Bound the question before encoding it as one daemon request.
const questionMax = 64 << 10

// askSurface refuses when no shell answers; silently running a tier would change
// where the answer goes. An absent question is sent as no argument, not an empty one.
func askSurface(c *zded.Client, method, question string) error {
	var args []string
	if question != "" {
		args = []string{question}
	}
	var shown bool
	if err := c.Call(method, &shown, args...); err != nil {
		return err
	}
	if shown {
		return nil
	}
	if question != "" {
		return errors.New("no shell to draw the ask panel, so that question was not asked: " +
			"`zde ask oneshot` takes the same question and answers here")
	}
	return errors.New("no shell to draw the ask window: ask from a terminal instead, " +
		"as `zde ask oneshot what is the capital of peru`")
}

// askRun streams one answer without keeping history.
func askRun(c *zded.Client, tier, question string) error {
	if err := c.Call(zded.MethodAskRun, nil, tier, question); err != nil {
		return err
	}
	ended := true
	for {
		ev, err := c.NextEvent()
		if err != nil {
			return fmt.Errorf("the answer stopped coming: %w", err)
		}
		if ev.Kind != zded.EventAskText {
			continue
		}
		// Filter before tracking newlines. Text preserves streamed formatting;
		// Block would indent fragments as though each were a whole message.
		if said := attn.Text(ev.Text); said != "" {
			fmt.Print(said)
			ended = strings.HasSuffix(said, "\n")
		}
		if !ev.Done {
			continue
		}
		if !ended {
			fmt.Println()
		}
		if ev.Error != "" {
			return errors.New(ev.Error)
		}
		return nil
	}
}
