package pass

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/crispuscrew/zde/internal/plainfile"
)

// storeVersion is the file's shape, not the derivation's. Params.Version is the
// one that decides what a password is; this one decides how to read the file
// that holds it.
const storeVersion = 1

// storeMax is the ceiling on the file. A thousand sites is around 300 KiB of
// this JSON, so a megabyte is a file somebody made a mistake with.
const storeMax = 1 << 20

// Store is everything pass keeps that is not secret: which services exist, what
// each site will accept, where its counter is, and which parameters its
// password was derived under.
type Store struct {
	Version int     `json:"version"`
	Sites   []*Site `json:"sites"`
}

// Site is one service.
type Site struct {
	// Service is the derivation's input and it never changes. Aliases and
	// listings are lookup; this string is what the password is a function of,
	// so renaming a site would be changing its password.
	Service string `json:"service"`
	// Aliases are the other spellings that find this site. They exist so that
	// two names for one account cannot become two different passwords.
	Aliases []string `json:"aliases,omitempty"`
	// Counter is the rotation. It starts at 1 and only ever goes up by hand.
	Counter uint64 `json:"counter"`
	Params  Params `json:"params"`
	Policy  Policy `json:"policy"`
}

// StorePath is the file.
func StorePath() string { return filepath.Join(Dir(), "sites.json") }

// Canonical is the one spelling a name is looked up under.
//
// A URL pasted out of a browser, a capital letter, a trailing dot and the www
// that half of the world's links carry all mean the same site, and a password
// manager that derives from the string as typed turns each of them into a
// different account. What this does not do is guess: it removes what is
// certainly noise and leaves the rest, so `mail.google.com` and `google.com`
// stay two names - and Aliases is how somebody says they are one.
func Canonical(name string) string {
	s := strings.TrimSpace(strings.ToLower(name))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	return strings.TrimSuffix(s, ".")
}

// checkName is what may be written down as a service or an alias.
func checkName(name string) error {
	c := Canonical(name)
	if c == "" {
		return fmt.Errorf("%q is not a name for a service", name)
	}
	if len(c) > 128 {
		return fmt.Errorf("a service name of %d characters is a mistake rather than a name", len(c))
	}
	for _, r := range c {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%q has a space or a control character in it, which a service name cannot", name)
		}
	}
	return nil
}

// LoadStore reads the file. A file that is not there is an empty store, because
// that is what a machine that has never recorded a site looks like.
func LoadStore(path string) (*Store, error) {
	data, err := plainfile.Read(path, storeMax)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{Version: storeVersion}, nil
		}
		return nil, err
	}
	var s Store
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Version != storeVersion {
		return nil, fmt.Errorf("%s says it is version %d and this zde reads version %d", path, s.Version, storeVersion)
	}
	// Every site checked at load, and the whole file refused if one is wrong.
	// A store half of which loaded would be a machine that derives for some
	// accounts and cannot say why not for the others.
	seen := map[string]string{}
	for _, site := range s.Sites {
		if err := checkName(site.Service); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := site.Params.Check(); err != nil {
			return nil, fmt.Errorf("%s, service %s: %w", path, site.Service, err)
		}
		if err := site.Policy.Check(); err != nil {
			return nil, fmt.Errorf("%s, service %s: %w", path, site.Service, err)
		}
		if site.Counter == 0 {
			return nil, fmt.Errorf("%s, service %s: the counter starts at 1", path, site.Service)
		}
		for _, name := range site.names() {
			if other, ok := seen[name]; ok {
				return nil, fmt.Errorf("%s: %q names both %s and %s, so it names neither", path, name, other, site.Service)
			}
			seen[name] = site.Service
		}
	}
	return &s, nil
}

// Save writes the file: 0600, and over a temporary rather than in place, so a
// crash halfway through leaves the counters that were there rather than half of
// them.
func Save(path string, s *Store) error {
	s.Version = storeVersion
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sites-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a failure below leaves nothing behind
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Site) names() []string {
	out := []string{Canonical(s.Service)}
	for _, a := range s.Aliases {
		out = append(out, Canonical(a))
	}
	return out
}

// Restamp puts today's parameters on a site and says whether anything moved.
//
// The password changes when it does, which is the whole reason this is a verb
// somebody types rather than something a new zde does on its own: raising the
// cost of Argon2id is a decision to go and change one account's password, taken
// one account at a time.
func (s *Site) Restamp(p Params) bool {
	if s.Params == p {
		return false
	}
	s.Params = p
	return true
}

// Find is the site a name means.
//
// An unknown name is an error and never a derivation. A password manager that
// derived from whatever it was handed would answer a typo with a password that
// is not wrong in any way it can detect - it just is not the one that opens the
// account, and the person retypes their master three times looking for the
// mistake.
func (s *Store) Find(name string) (*Site, error) {
	key := Canonical(name)
	for _, site := range s.Sites {
		for _, n := range site.names() {
			if n == key {
				return site, nil
			}
		}
	}
	return nil, fmt.Errorf("no service here is called %q: `zde pass list` says what there is, and `zde pass add` records a new one", name)
}

// Add records a service.
func (s *Store) Add(site *Site) error {
	if err := checkName(site.Service); err != nil {
		return err
	}
	if err := site.Params.Check(); err != nil {
		return err
	}
	if err := site.Policy.Check(); err != nil {
		return err
	}
	for _, name := range site.names() {
		if other, err := s.Find(name); err == nil {
			return fmt.Errorf("%q is already %s here", name, other.Service)
		}
	}
	s.Sites = append(s.Sites, site)
	s.sort()
	return nil
}

// Alias gives a site a second spelling.
func (s *Store) Alias(site *Site, name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if other, err := s.Find(name); err == nil {
		return fmt.Errorf("%q is already %s here", name, other.Service)
	}
	site.Aliases = append(site.Aliases, Canonical(name))
	sort.Strings(site.Aliases)
	return nil
}

// sort keeps the file in a stable order, so that a diff of it is the change
// somebody made.
func (s *Store) sort() {
	sort.Slice(s.Sites, func(i, j int) bool {
		return Canonical(s.Sites[i].Service) < Canonical(s.Sites[j].Service)
	})
}
