package pass

import (
	"fmt"
	"slices"
	"strings"
)

// The character classes a site policy is written in. Names rather than the sets
// themselves, so the file a person edits says `"allow": ["lower","digit"]` and
// the sets stay one definition here: a site policy that carried its own
// alphabet would be a site policy that can be edited into a different password.
const (
	Lower  = "lower"
	Upper  = "upper"
	Digit  = "digit"
	Symbol = "symbol"
)

// The sets. ASCII only, and symbol is the whole of ASCII punctuation except the
// space: zde has no opinion about which symbols a site can take, and Policy's
// Exclude is where that opinion belongs, because it is the site's.
var classSet = map[string]string{
	Lower:  "abcdefghijklmnopqrstuvwxyz",
	Upper:  "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	Digit:  "0123456789",
	Symbol: "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~",
}

// classOrder is the order the alphabet is assembled in, and it is fixed here
// rather than taken from Policy.Allow. The alphabet's order decides which
// character each byte of the stream picks, so a policy edited from
// ["lower","digit"] to ["digit","lower"] - the same policy, said differently -
// would otherwise derive a different password.
var classOrder = []string{Lower, Upper, Digit, Symbol}

// Length is bounded at both ends. The ceiling is not a cryptographic limit; it
// is a file a person edits, and a length of 100000 is a typo rather than a
// password.
const (
	minLength = 4
	maxLength = 128
)

// Policy is what one site will accept.
type Policy struct {
	Length int `json:"length"`
	// Allow is the classes the alphabet is built from.
	Allow []string `json:"allow"`
	// Require is how many characters of a class must appear - the "at least one
	// digit and one symbol" every site asks for. A class not named here may
	// still appear; it is a floor, not a quota.
	Require map[string]int `json:"require,omitempty"`
	// Exclude is the characters this particular site refuses. Sites that ban a
	// few symbols are common and banning them is not a class, so it is a set of
	// characters rather than another flag.
	Exclude string `json:"exclude,omitempty"`
}

// DefaultPolicy is what a site gets when nobody says otherwise: 20 characters
// of everything, one of each class guaranteed.
func DefaultPolicy() Policy {
	return Policy{
		Length:  20,
		Allow:   []string{Lower, Upper, Digit, Symbol},
		Require: map[string]int{Lower: 1, Upper: 1, Digit: 1, Symbol: 1},
	}
}

// Check says whether this policy can be satisfied at all, before any password
// is derived.
//
// Structural, and separate from the generate-and-reject loop below, so that
// "four required classes in three characters" is answered with the reason
// rather than with a thousand rejected attempts and a timeout.
func (p Policy) Check() error {
	_, _, err := p.sets()
	return err
}

// sets is the alphabet and the required classes' own alphabets, both after
// Exclude, and every reason a policy cannot be satisfied.
func (p Policy) sets() (string, map[string]string, error) {
	if p.Length < minLength || p.Length > maxLength {
		return "", nil, fmt.Errorf("a password of %d characters: the policy has to ask for between %d and %d", p.Length, minLength, maxLength)
	}
	allowed := map[string]bool{}
	for _, name := range p.Allow {
		if _, ok := classSet[name]; !ok {
			return "", nil, fmt.Errorf("%q is not a character class: they are %s", name, strings.Join(classOrder, ", "))
		}
		allowed[name] = true
	}
	if len(allowed) == 0 {
		return "", nil, fmt.Errorf("the policy allows no character class, so there is nothing to build a password out of")
	}
	var alphabet strings.Builder
	req := map[string]string{}
	total := 0
	for _, name := range classOrder {
		if !allowed[name] {
			continue
		}
		set := strip(classSet[name], p.Exclude)
		alphabet.WriteString(set)
		n, wanted := p.Require[name]
		if !wanted {
			continue
		}
		if n < 0 {
			return "", nil, fmt.Errorf("the policy requires %d characters of class %s", n, name)
		}
		if n == 0 {
			continue
		}
		if set == "" {
			return "", nil, fmt.Errorf("the policy requires a %s and excludes every %s character, so no password can satisfy it", name, name)
		}
		req[name] = set
		total += n
	}
	for name, n := range p.Require {
		if n > 0 && !allowed[name] {
			return "", nil, fmt.Errorf("the policy requires a %s and does not allow one, so no password can satisfy it", name)
		}
	}
	if alphabet.Len() == 0 {
		return "", nil, fmt.Errorf("the policy excludes every character it allows, so there is nothing to build a password out of")
	}
	if total > p.Length {
		return "", nil, fmt.Errorf("the policy requires %d characters and asks for a password of %d, so no password can satisfy it", total, p.Length)
	}
	return alphabet.String(), req, nil
}

// canon is the policy as the derivation sees it: one string, in one order, for
// every way of writing the same rules.
//
// It goes into the salt, so what it leaves out is what a person may edit
// without changing a password - the order of Allow, a Require of zero, an
// Exclude naming characters no allowed class contains - and what it keeps is
// everything that changes which strings are acceptable.
func (p Policy) canon() string {
	var allow, req []string
	var excluded []byte
	seen := map[byte]bool{}
	for _, name := range classOrder {
		if !slices.Contains(p.Allow, name) {
			continue
		}
		allow = append(allow, name)
		if n := p.Require[name]; n > 0 {
			req = append(req, fmt.Sprintf("%s%d", name, n))
		}
		for _, c := range []byte(classSet[name]) {
			if strings.IndexByte(p.Exclude, c) >= 0 && !seen[c] {
				seen[c] = true
				excluded = append(excluded, c)
			}
		}
	}
	slices.Sort(excluded)
	return fmt.Sprintf("L%d;A%s;R%s;X%s", p.Length, strings.Join(allow, ","), strings.Join(req, ","), excluded)
}

func strip(set, exclude string) string {
	if exclude == "" {
		return set
	}
	var out strings.Builder
	for _, c := range []byte(set) {
		if strings.IndexByte(exclude, c) < 0 {
			out.WriteByte(c)
		}
	}
	return out.String()
}

// maxAttempts bounds the loop below. A policy this cannot satisfy in a thousand
// uniform draws is one whose satisfying strings are a vanishing fraction of its
// alphabet, and the answer for those is the message rather than a longer wait.
const maxAttempts = 1000

// encode turns the derived key's byte stream into a password this policy
// accepts.
//
// Generate the whole string uniformly and reject it if it does not satisfy the
// policy, rather than placing one character per required class and shuffling
// the result. The two are not equally good: rejection is uniform over exactly
// the set of strings the policy accepts, and place-then-shuffle is not - it
// over-represents strings that hold the bare minimum of each required class,
// which is a bias an attacker who knows the policy can search along. Rejection
// costs attempts and nothing else, and the stream is unbounded, so it costs
// nothing that matters.
//
// Deterministic despite the loop: attempts draw from the stream in order, so
// the same key and policy always land on the same attempt.
func (p Policy) encode(s *stream) ([]byte, error) {
	alphabet, req, err := p.sets()
	if err != nil {
		return nil, err
	}
	out := make([]byte, p.Length)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		for i := range out {
			out[i] = alphabet[s.index(len(alphabet))]
		}
		if satisfies(out, req, p.Require) {
			return out, nil
		}
	}
	Zero(out)
	// The counts are named and the attempts are not: what a person can act on
	// is the policy, and "1000 tries" invites the belief that trying harder
	// would have worked.
	return nil, fmt.Errorf("no password of %d characters from this policy's alphabet had the classes it requires, so the policy cannot be satisfied in practice", p.Length)
}

func satisfies(out []byte, req map[string]string, want map[string]int) bool {
	for name, set := range req {
		n := 0
		for _, c := range out {
			if strings.IndexByte(set, c) >= 0 {
				n++
			}
		}
		if n < want[name] {
			return false
		}
	}
	return true
}

// Describe is the policy in one line, for a listing. No secret is involved: it
// is the shape of a password and not one.
func (p Policy) Describe() string {
	// In classOrder, which is the order the alphabet is built in and the order
	// the requirements below print in. Alphabetical would put the two halves of
	// one line in two different orders.
	var allow []string
	for _, name := range classOrder {
		if slices.Contains(p.Allow, name) {
			allow = append(allow, name)
		}
	}
	line := fmt.Sprintf("%d chars, %s", p.Length, strings.Join(allow, "+"))
	if len(p.Require) > 0 {
		var req []string
		for _, name := range classOrder {
			if n := p.Require[name]; n > 0 {
				req = append(req, fmt.Sprintf("%d %s", n, name))
			}
		}
		line += ", at least " + strings.Join(req, " and ")
	}
	if p.Exclude != "" {
		line += fmt.Sprintf(", not %q", p.Exclude)
	}
	return line
}
