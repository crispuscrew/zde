package pass

import (
	"strings"
	"testing"
)

// A password nobody can type into the site it was derived for is not a
// password. Every row is a site policy somebody actually meets: a bank that
// takes digits, a router that caps the length, a form that breaks on a quote.
func TestAPasswordIsWhatTheSiteWillTake(t *testing.T) {
	for _, c := range []struct {
		what   string
		policy Policy
	}{
		{"everything, one of each", DefaultPolicy()},
		{"a bank's twelve digits", Policy{Length: 12, Allow: []string{Digit}, Require: map[string]int{Digit: 1}}},
		{"letters and digits, no symbols", Policy{Length: 16, Allow: []string{Lower, Upper, Digit}, Require: map[string]int{Digit: 2, Upper: 1}}},
		{"a form that breaks on quotes", Policy{Length: 20, Allow: []string{Lower, Symbol}, Require: map[string]int{Symbol: 3}, Exclude: "\"'`\\"}},
		{"the shortest a policy may ask for", Policy{Length: 4, Allow: []string{Lower, Upper, Digit, Symbol}, Require: map[string]int{Lower: 1, Upper: 1, Digit: 1, Symbol: 1}}},
	} {
		t.Run(c.what, func(t *testing.T) {
			alphabet, _, err := c.policy.sets()
			if err != nil {
				t.Fatal(err)
			}
			// Several counters, because a policy that is satisfied by the first
			// draw and by nothing else is a policy that fails on a Tuesday.
			for counter := uint64(1); counter <= 20; counter++ {
				got, err := Derive([]byte(fixedMaster), fixedPepper(), "example.org", counter, cheap(), c.policy)
				if err != nil {
					t.Fatalf("counter %d: %v", counter, err)
				}
				if len(got) != c.policy.Length {
					t.Fatalf("counter %d: %d characters, and the site takes %d", counter, len(got), c.policy.Length)
				}
				for _, ch := range got {
					if strings.IndexByte(alphabet, ch) < 0 {
						t.Fatalf("counter %d: %q is not a character this policy allows", counter, string(ch))
					}
					if strings.IndexByte(c.policy.Exclude, ch) >= 0 {
						t.Fatalf("counter %d: %q is a character the site refuses", counter, string(ch))
					}
				}
				for name, want := range c.policy.Require {
					n := 0
					for _, ch := range got {
						if strings.IndexByte(strip(classSet[name], c.policy.Exclude), ch) >= 0 {
							n++
						}
					}
					if n < want {
						t.Fatalf("counter %d: %q has %d of class %s and the site wants %d", counter, got, n, name, want)
					}
				}
			}
		})
	}
}

// A policy nothing can satisfy is answered with the reason, before Argon2id
// runs and not after a thousand attempts. Somebody wrote these rules down by
// hand and the message is what tells them which one is wrong.
func TestAPolicyThatCannotBeSatisfiedSaysWhy(t *testing.T) {
	for _, c := range []struct {
		what   string
		policy Policy
		says   string
	}{
		{"four required classes in three characters", Policy{Length: 3, Allow: []string{Lower, Upper, Digit, Symbol}, Require: map[string]int{Lower: 1, Upper: 1, Digit: 1, Symbol: 1}}, "between 4 and 128"},
		{"more required than the length", Policy{Length: 8, Allow: []string{Lower, Digit}, Require: map[string]int{Lower: 6, Digit: 4}}, "requires 10 characters"},
		{"a class required and not allowed", Policy{Length: 8, Allow: []string{Lower}, Require: map[string]int{Symbol: 1}}, "does not allow"},
		{"every digit excluded and a digit required", Policy{Length: 8, Allow: []string{Lower, Digit}, Require: map[string]int{Digit: 1}, Exclude: "0123456789"}, "excludes every digit"},
		{"no classes at all", Policy{Length: 8}, "allows no character class"},
		{"a class that does not exist", Policy{Length: 8, Allow: []string{"emoji"}}, "not a character class"},
		{"the whole alphabet excluded", Policy{Length: 8, Allow: []string{Digit}, Exclude: "0123456789"}, "excludes every character"},
		{"a password of a hundred thousand characters", Policy{Length: 100000, Allow: []string{Lower}}, "between 4 and 128"},
	} {
		err := c.policy.Check()
		if err == nil {
			t.Errorf("%s was accepted", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s was refused with %q, which does not say %q", c.what, err, c.says)
		}
		if _, err := Derive([]byte(fixedMaster), fixedPepper(), "example.org", 1, cheap(), c.policy); err == nil {
			t.Errorf("%s derived a password anyway", c.what)
		}
	}
}

// The other kind of impossible: a policy nothing structural refuses, whose
// acceptable strings are a vanishing fraction of its alphabet. Twelve required
// digits out of thirty-six characters is one in four million, so the loop gives
// up and says the policy cannot be satisfied - rather than spinning.
//
// It cannot flake: the stream is a function of the inputs, so this policy fails
// on every machine and on every run, or on none.
func TestAPolicyThatIsOnlyAlmostImpossibleAlsoGivesUp(t *testing.T) {
	pol := Policy{Length: 12, Allow: []string{Lower, Digit}, Require: map[string]int{Digit: 12}}
	if err := pol.Check(); err != nil {
		t.Fatalf("this policy is meant to pass the structural check: %v", err)
	}
	_, err := Derive([]byte(fixedMaster), fixedPepper(), "example.org", 1, cheap(), pol)
	if err == nil {
		t.Fatal("a policy one draw in four million satisfies was satisfied")
	}
	if !strings.Contains(err.Error(), "cannot be satisfied") {
		t.Errorf("it gave up with %q", err)
	}
}

// The alphabet's order is this file's and not the policy file's. It decides
// which character each byte of the stream lands on, so taking it from Allow
// would make a reordered list a changed password - and the person who reordered
// it changed nothing.
func TestTheAlphabetIsBuiltInOneOrder(t *testing.T) {
	a, _, err := Policy{Length: 8, Allow: []string{Digit, Lower}}.sets()
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Policy{Length: 8, Allow: []string{Lower, Digit}}.sets()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("the same classes in two orders built %q and %q", a, b)
	}
	if !strings.HasPrefix(a, "abc") {
		t.Errorf("the alphabet is %q, and classOrder says it starts with the lower case", a)
	}
}

// Describe is what a listing prints. It is the shape of a password and never
// one, and this is the test that says so out loud.
func TestThePolicyPrintsAsItsShape(t *testing.T) {
	got := Policy{Length: 16, Allow: []string{Lower, Digit}, Require: map[string]int{Digit: 2}, Exclude: "0O"}.Describe()
	for _, want := range []string{"16 chars", "lower+digit", "at least 2 digit", `not "0O"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the policy reads %q, which does not say %q", got, want)
		}
	}
}
