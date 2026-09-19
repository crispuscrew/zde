package zded

import (
	"strings"
	"testing"
)

// An external question must clear prior turns before any branch can send them to a tier.
func TestAQuestionFromOutsideCannotBeAskedInsideSomebodyElsesConversation(t *testing.T) {
	const function = "function askFromOutside("
	source := string(readQML(t, "AskWindow.qml"))
	start := strings.Index(source, function)
	if start < 0 {
		t.Fatalf("AskWindow.qml has no %s", function)
	}
	body := source[start+len(function):]
	lineEnd := strings.Index(body, "\n")
	if lineEnd < 0 {
		t.Fatal("askFromOutside has no body")
	}
	end := strings.Index(body, "\n    }")
	if end < 0 {
		t.Fatal("askFromOutside has no closing brace at its declaration indentation")
	}
	body = body[lineEnd+1 : end]
	first := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		first = line
		break
	}
	if first != "ask.forget();" {
		t.Errorf("askFromOutside starts with %q, not ask.forget(): an external question could include prior turns", first)
	}
}
