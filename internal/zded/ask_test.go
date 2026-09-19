package zded

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAskRunsTheTierWithTheQuestionOnItsStdin(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("echo")})
	text, failure := askAll(t, askServer(t), TierProvider, "what is the capital of peru")
	if failure != "" {
		t.Fatalf("the ask failed: %s", failure)
	}
	if text != "answered: what is the capital of peru" {
		t.Errorf("the tier answered %q", text)
	}
}

func TestAskWithNoTierSaysWhichOptionToSet(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("echo")})
	_, failure := askAll(t, askServer(t), TierLocal, "something private")
	if !strings.Contains(failure, "zde.ask.tiers.local") {
		t.Errorf("the refusal is %q, and does not say which option to set", failure)
	}
	if !strings.Contains(failure, TierProvider) {
		t.Errorf("the refusal is %q, and does not say what this machine has", failure)
	}
}

func TestATierThatSaysNothingIsAFailure(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("silent")})
	text, failure := askAll(t, askServer(t), TierProvider, "anything")
	if text != "" {
		t.Errorf("the silent tier answered %q", text)
	}
	if !strings.Contains(failure, "said nothing") {
		t.Errorf("a tier that said nothing ended with %q", failure)
	}
}

func TestATierThatCannotStartNamesTheOption(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-tier")
	writeTiers(t, map[string][]string{TierProvider: {missing}})
	_, failure := askAll(t, askServer(t), TierProvider, "anything")
	for _, want := range []string{missing, "zde.ask.tiers.provider", "no such file"} {
		if !strings.Contains(failure, want) {
			t.Errorf("a tier that cannot start reported %q, which does not mention %q", failure, want)
		}
	}
}

func TestAFailedTierReportsWhatItComplainedAbout(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("angry")})
	_, failure := askAll(t, askServer(t), TierProvider, "anything")
	if !strings.Contains(failure, "no credentials in this container") {
		t.Errorf("the tier failed with %q, and its own words are not in it", failure)
	}
}
