package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/crispuscrew/zde/internal/bt"
	"github.com/crispuscrew/zde/internal/zded"
)

// printQuestion distinguishes passkey comparison, entry on the other device,
// service permission and pairing with nothing to compare.
func printQuestion(req bt.Request) {
	who := req.Device
	if req.Name != "" {
		who += "  " + req.Name
	}
	switch req.Kind {
	case bt.KindDisplay:
		fmt.Printf("asking     %s\n", who)
		typed := ""
		if req.Entered > 0 {
			typed = fmt.Sprintf("  (%d typed so far)", req.Entered)
		}
		fmt.Printf("           type %s on it%s\n", req.Passkey, typed)
	case bt.KindService:
		fmt.Printf("asking     %s\n", who)
		fmt.Printf("           wants %s\n", serviceWords(req))
		fmt.Printf("           allow it: zde system bluetooth confirm %s yes\n", req.ID)
	case bt.KindAuthorize:
		fmt.Printf("asking     %s wants to pair\n", who)
		fmt.Println("           there is nothing to compare: say yes only if you started this")
		fmt.Printf("           zde system bluetooth confirm %s yes\n", req.ID)
	default:
		fmt.Printf("asking     %s\n", who)
		fmt.Printf("           it should be showing %s\n", req.Passkey)
		fmt.Printf("           if it is: zde system bluetooth confirm %s yes\n", req.ID)
	}
}

func serviceWords(req bt.Request) string {
	if req.Service == "" {
		return "a service this does not recognise: " + req.UUID
	}
	return req.Service + "  (" + req.UUID + ")"
}

// answerHere prompts only at a terminal, with a deadline and the question's id
// so a late answer cannot authorize a different pending request.
func answerHere(c *zded.Client, req bt.Request) (bool, error) {
	if req.Kind == bt.KindDisplay {
		return false, nil
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false, nil
	}
	fmt.Print("           " + prompt(req) + " [y/N] ")

	// Stdin has no portable deadline. A timed-out read leaves one blocked
	// goroutine in this short-lived process rather than holding the prompt open.
	typed := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		typed <- line
	}()
	var line string
	select {
	case line = <-typed:
	case <-time.After(bt.AnswerWait):
		fmt.Println()
		return true, errors.New("nobody answered here in time, so it was refused")
	}
	// Anything other than an explicit yes refuses the request.
	answer := "no"
	if s := strings.ToLower(strings.TrimSpace(line)); s == "y" || s == "yes" {
		answer = "yes"
	}
	return true, c.Call("bluetooth.confirm", nil, req.ID, answer)
}

func prompt(req bt.Request) string {
	switch req.Kind {
	case bt.KindService:
		return "allow it?"
	case bt.KindAuthorize:
		return "did you start this?"
	default:
		return "is it showing " + req.Passkey + "?"
	}
}
