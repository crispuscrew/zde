package zded

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Oversized context is refused, not silently shortened; diagnostics must name a larger size.
func TestAConversationPastItsBoundIsRefusedRatherThanShortened(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("dump")})
	long := strings.Repeat("x", askContextMax/2)
	text, failure := askAll(t, askServer(t), TierProvider, "and now",
		"the first question", long, "the second question", long)
	if text != "" {
		t.Errorf("the tier ran anyway and answered %q", text)
	}
	if !strings.Contains(failure, "start a fresh one") {
		t.Errorf("the refusal is %q, and does not say what to do about it", failure)
	}
	reached, carries := kibInRefusal(t, failure)
	if carries != askContextMax>>10 {
		t.Errorf("the refusal is %q, and the bound in it is %d rather than %d", failure, carries, askContextMax>>10)
	}
	if reached <= carries {
		t.Errorf("the refusal is %q: it says the conversation reached %d KiB and that one ask carries %d, "+
			"which is not a reason to refuse anything", failure, reached, carries)
	}
}

// Read both sizes: a refusal that prints the same size as its bound explains nothing.
func kibInRefusal(t *testing.T, failure string) (reached, carries int) {
	t.Helper()
	found := regexp.MustCompile(`\d+`).FindAllString(failure, -1)
	if len(found) != 2 {
		t.Fatalf("the refusal is %q, and does not name exactly two sizes: %v", failure, found)
	}
	var err error
	if reached, err = strconv.Atoi(found[0]); err != nil {
		t.Fatal(err)
	}
	if carries, err = strconv.Atoi(found[1]); err != nil {
		t.Fatal(err)
	}
	return reached, carries
}

// The first byte over the limit must round up in the diagnostic.
func TestAConversationOneByteOverIsNotReportedAsTheBoundItself(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("dump")})
	prior := []string{"what is the capital of peru", "Lima."}
	question := strings.Repeat("x", askContextMax+1-len(askDoc(prior, "")))
	if got := len(askDoc(prior, question)); got != askContextMax+1 {
		t.Fatalf("this conversation is %d bytes and the test means it to be %d, "+
			"so it is not measuring the case it is named after", got, askContextMax+1)
	}

	text, failure := askAll(t, askServer(t), TierProvider, question, prior...)
	if text != "" {
		t.Errorf("the tier ran anyway and answered %d bytes", len(text))
	}
	reached, carries := kibInRefusal(t, failure)
	if reached <= carries {
		t.Errorf("a conversation of %d bytes was refused with %q, which says it reached %d KiB "+
			"against a bound of %d", askContextMax+1, failure, reached, carries)
	}
}

func TestAQuestionTooBigForOneRunSaysItIsTheQuestion(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("dump")})
	text, failure := askAll(t, askServer(t), TierProvider, strings.Repeat("x", askContextMax+1))
	if text != "" {
		t.Errorf("the tier ran anyway and answered %q", text)
	}
	if !strings.Contains(failure, "fewer words") {
		t.Errorf("the refusal is %q, and does not say that the question is what is too big", failure)
	}
}

// Pairing determines roles; an odd tail would relabel the tier's answer as a question.
func TestTurnsThatDoNotPairUpAreRefused(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("dump")})
	text, failure := askAll(t, askServer(t), TierProvider, "and of chile", "what is the capital of peru")
	if text != "" {
		t.Errorf("the tier ran anyway and answered %q", text)
	}
	if !strings.Contains(failure, "pairs") {
		t.Errorf("the refusal is %q, and does not say what the turns should look like", failure)
	}
}

func TestATurnWithNothingInItIsRefused(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("dump")})
	text, failure := askAll(t, askServer(t), TierProvider, "and of chile", "what is the capital of peru", "  ")
	if text != "" {
		t.Errorf("the tier ran anyway and answered %q", text)
	}
	if !strings.Contains(failure, "empty") {
		t.Errorf("the refusal is %q, and does not say what is wrong with it", failure)
	}
}
