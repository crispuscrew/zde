package pass

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"
	"testing"
)

// The fixed inputs every known-answer test below derives from. A pepper of
// 0x00..0x1f rather than a random one, so the vectors in this file can be
// recomputed by hand from the source.
func fixedPepper() []byte {
	p := make([]byte, PepperLen)
	for i := range p {
		p[i] = byte(i)
	}
	return p
}

const fixedMaster = "correct horse battery staple"

// cheap is the parameter set the tests that are not about cost use: Argon2id is
// designed to spend memory, and a suite that ran the shipped 64 MiB a hundred
// times would be a suite nobody runs.
func cheap() Params { return Params{Version: Version, Time: 1, MemoryKiB: 64, Lanes: 1} }

// The vectors. This is the test this package exists for: a derived password
// manager has no vault, so a refactor that changes the derivation does not
// break a stored secret, it silently locks a person out of every account they
// have. These strings are the contract, and a change to any of them is a
// migration rather than a diff.
//
// Two parameter sets over the same service, counter and policy, which is what
// pins Params as an input rather than a note: with the cost hard-coded anywhere
// in Derive, the two rows below come back equal.
func TestTheDerivationIsPinnedToTheseAnswers(t *testing.T) {
	for _, c := range []struct {
		name    string
		service string
		counter uint64
		params  Params
		policy  Policy
		want    string
	}{
		{
			name:    "the shipped parameters",
			service: "github.com",
			counter: 1,
			params:  Default(),
			policy:  DefaultPolicy(),
			want:    "CBkcS6zYIM/|)4\\ss>Z^",
		},
		{
			name:    "the same site under cheaper parameters",
			service: "github.com",
			counter: 1,
			params:  cheap(),
			policy:  DefaultPolicy(),
			want:    "o]%x~;UQdwU\\n?a9^6yr",
		},
		{
			name:    "the next rotation of it",
			service: "github.com",
			counter: 2,
			params:  cheap(),
			policy:  DefaultPolicy(),
			want:    "vav7zCPRX]Mp0}Qwny4\"",
		},
		{
			name:    "another service",
			service: "example.org",
			counter: 1,
			params:  cheap(),
			policy:  DefaultPolicy(),
			want:    "Wymh/K1!xh\"F8-=vU'j_",
		},
		{
			name:    "a bank that takes twelve digits and nothing else",
			service: "example.org",
			counter: 1,
			params:  cheap(),
			policy:  Policy{Length: 12, Allow: []string{Digit}, Require: map[string]int{Digit: 1}},
			want:    "991135978253",
		},
		{
			name:    "a site that refuses four symbols",
			service: "example.org",
			counter: 1,
			params:  cheap(),
			policy: Policy{
				Length:  16,
				Allow:   []string{Lower, Upper, Digit, Symbol},
				Require: map[string]int{Lower: 1, Upper: 1, Digit: 1, Symbol: 1},
				Exclude: "\"'`\\",
			},
			want: "{/^26Nx4dkVkq5rm",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Derive([]byte(fixedMaster), fixedPepper(), c.service, c.counter, c.params, c.policy)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("derived %q, and the vector is %q: every password on a machine running this build just changed", got, c.want)
			}
		})
	}
}

// Each of the five inputs is an input. A version of this that folded, say, the
// counter into the salt and then ignored it would pass every other test here:
// the answer would still be a password of the right shape.
func TestEveryInputChangesTheAnswer(t *testing.T) {
	base := func() ([]byte, error) {
		return Derive([]byte(fixedMaster), fixedPepper(), "github.com", 1, cheap(), DefaultPolicy())
	}
	want, err := base()
	if err != nil {
		t.Fatal(err)
	}
	other := fixedPepper()
	other[0] ^= 0xff
	harder := cheap()
	harder.Time = 2
	for _, c := range []struct {
		what string
		get  func() ([]byte, error)
	}{
		{"the master", func() ([]byte, error) {
			return Derive([]byte(fixedMaster+"!"), fixedPepper(), "github.com", 1, cheap(), DefaultPolicy())
		}},
		{"the pepper", func() ([]byte, error) {
			return Derive([]byte(fixedMaster), other, "github.com", 1, cheap(), DefaultPolicy())
		}},
		{"the service", func() ([]byte, error) {
			return Derive([]byte(fixedMaster), fixedPepper(), "github.co", 1, cheap(), DefaultPolicy())
		}},
		{"the counter", func() ([]byte, error) {
			return Derive([]byte(fixedMaster), fixedPepper(), "github.com", 2, cheap(), DefaultPolicy())
		}},
		{"the parameters", func() ([]byte, error) {
			return Derive([]byte(fixedMaster), fixedPepper(), "github.com", 1, harder, DefaultPolicy())
		}},
	} {
		got, err := c.get()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) == string(want) {
			t.Errorf("changing %s derived the same password, so it is not part of the derivation", c.what)
		}
	}
}

// The fields going into the salt are length-prefixed, so no two different
// sequences of them can build the same message. Today's four fields would not
// collide without it - the counter is fixed width and the policy starts with a
// letter - so what this is really holding is the fifth field somebody adds: two
// variable-length fields next to each other is where "gi"+"thub" and
// "git"+"hub" become one site.
func TestTwoFieldsCannotRunTogetherInTheSalt(t *testing.T) {
	digest := func(parts ...string) string {
		m := hmac.New(sha256.New, fixedPepper())
		for _, p := range parts {
			field(m, []byte(p))
		}
		return string(m.Sum(nil))
	}
	if digest("git", "hub") == digest("gi", "thub") {
		t.Error("two different field sequences salt the same, so two sites can share a password")
	}
}

// The policy is part of the derivation, so that one site's shortened password
// tells nothing about the longer one it replaced: the encoder's byte stream is
// a function of the Argon2id output alone, and without this the two would be
// drawn from the same bytes and share characters position for position.
//
// The other half of the same rule: only the effective policy counts. A person
// editing the file writes the classes in whatever order, and an order is not a
// reason to change a password.
func TestThePolicyIsPartOfTheDerivationAndItsSpellingIsNot(t *testing.T) {
	derive := func(pol Policy) string {
		got, err := Derive([]byte(fixedMaster), fixedPepper(), "example.org", 1, cheap(), pol)
		if err != nil {
			t.Fatal(err)
		}
		return string(got)
	}
	long := Policy{Length: 20, Allow: []string{Lower, Upper, Digit, Symbol}, Require: map[string]int{Digit: 1}}
	short := long
	short.Length = 16
	if strings.HasPrefix(derive(long), derive(short)[:8]) {
		t.Error("two policies for one site draw the same bytes, so the shorter password gives away the longer one")
	}
	said := long
	said.Allow = []string{Symbol, Digit, Upper, Lower}
	said.Require = map[string]int{Digit: 1, Lower: 0}
	said.Exclude = " " // no class holds a space, so this excludes nothing
	if derive(said) != derive(long) {
		t.Error("the same policy written differently derived a different password")
	}
}

// A parameter set out of a file is a parameter set somebody can edit, and
// argon2 allocates what it is told to. `"memory_kib": 68719476736` is not a
// strange password, it is this machine going down - and there is no way to
// discover that except by having it happen.
func TestParametersOutOfAFileCannotAskForTheMachine(t *testing.T) {
	for _, c := range []struct {
		what string
		p    Params
		says string
	}{
		{"a terabyte of memory", Params{Version: Version, Time: 3, MemoryKiB: 1 << 30, Lanes: 4}, "memory"},
		{"no memory at all", Params{Version: Version, Time: 3, MemoryKiB: 1, Lanes: 4}, "memory"},
		{"a thousand passes", Params{Version: Version, Time: 1000, MemoryKiB: 1024, Lanes: 1}, "time"},
		{"no passes", Params{Version: Version, Time: 0, MemoryKiB: 1024, Lanes: 1}, "time"},
		{"no lanes", Params{Version: Version, Time: 1, MemoryKiB: 1024, Lanes: 0}, "parallelism"},
		{"a version this build cannot reproduce", Params{Version: 99, Time: 1, MemoryKiB: 1024, Lanes: 1}, "version"},
	} {
		err := c.p.Check()
		if err == nil {
			t.Errorf("%s was accepted", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s was refused with %q, which does not say which parameter", c.what, err)
		}
		if _, err := Derive([]byte(fixedMaster), fixedPepper(), "github.com", 1, c.p, DefaultPolicy()); err == nil {
			t.Errorf("%s reached argon2 anyway", c.what)
		}
	}
}

// A password is one thing and its ingredients are another: nothing in this
// package may put the master or the pepper into an error, because an error is
// the one value that is printed on every failure path by design.
func TestNoRefusalCarriesTheMasterOrThePepper(t *testing.T) {
	pepper := fixedPepper()
	impossible := Policy{Length: 8, Allow: []string{Digit}, Require: map[string]int{Symbol: 1}}
	for _, c := range []struct {
		what   string
		params Params
		policy Policy
	}{
		{"an impossible policy", cheap(), impossible},
		{"parameters this build will not run", Params{Version: 99}, DefaultPolicy()},
	} {
		_, err := Derive([]byte(fixedMaster), pepper, "github.com", 1, c.params, c.policy)
		if err == nil {
			t.Fatalf("%s was accepted", c.what)
		}
		if strings.Contains(err.Error(), fixedMaster) {
			t.Errorf("%s answered with the master in the error: %q", c.what, err)
		}
		if strings.Contains(err.Error(), string(pepper)) {
			t.Errorf("%s answered with the pepper in the error: %q", c.what, err)
		}
	}
}

// index draws uniformly, and the way it does that is rejection. A plain
// `next() % n` is the one-character simplification this invites, and it is a
// bias in every position of every password derived: 256 is not a multiple of
// 94, so the first 68 characters of the alphabet would come up half again as
// often as the other 26.
//
// Deterministic, so it cannot flake: the stream is HMAC over a fixed key, and
// the counts below are the counts for that key.
func TestTheAlphabetIsDrawnFromWithoutBias(t *testing.T) {
	const n = 94
	const draws = 20000
	s := newStream([]byte("a key that is not a secret"), "test")
	counts := make([]int, n)
	for i := 0; i < draws; i++ {
		counts[s.index(n)]++
	}
	// The two halves a modulo would split the alphabet into: 256%94 is 68, so
	// indices below 68 would take three bytes out of 256 and the rest two.
	low, high := 0, 0
	for i, c := range counts {
		if i < 256%n {
			low += c
		} else {
			high += c
		}
	}
	lowMean := float64(low) / float64(256%n)
	highMean := float64(high) / float64(n-256%n)
	ratio := lowMean / highMean
	if ratio < 0.94 || ratio > 1.06 {
		t.Errorf("the first %d indices come up %.3f times as often as the rest, which is a modulo and not a draw", 256%n, ratio)
	}
}
