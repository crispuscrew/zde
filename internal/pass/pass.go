// Package pass derives a site's password instead of keeping one.
//
// The whole derivation is here: Argon2id over the master, salted with a keyed
// hash of the service name and its counter, then encoded to what the site will
// accept (policy.go). Nothing derived is ever written down. What is on disk is
// the counters, the site policies and the parameters (store.go), and one
// secret - the pepper (pepper.go).
//
// The threat model is docs/vision.md's, and this package is built to keep it:
// one leaked site password is an offline oracle on the master, Argon2id makes
// each guess of the master expensive, and the pepper makes the attack
// know-plus-have, because the salt cannot be computed without the file. So the
// pepper is the HMAC key and not another string mixed into the message: a
// message an attacker can guess plus a key they do not have is exactly the
// property wanted, and appending a secret to a hashed message is not.
// Unrotatable legacy passwords are out of scope, as they are in the vision.
//
// Argon2id itself is golang.org/x/crypto/argon2, pinned at v0.29.0 - the last
// release that requires golang.org/x/sys v0.27.0, which is the version this
// repo already vendors. Nothing here implements a primitive: the KDF is
// x/crypto's, the keyed hash is crypto/hmac over crypto/sha256, and the byte
// stream the encoder draws from is those two in counter mode (NIST SP 800-108).
//
// What the trusted window (docs/vision.md, section 3) will plug into: Derive,
// with the master it took under a keyboard grab, and Zero when it is done with
// it. Nothing in this package reaches a terminal, a clipboard or the daemon.
package pass

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"runtime"

	"golang.org/x/crypto/argon2"
)

// Version is the shape of the derivation: what goes into the salt, which KDF
// runs, and how its output becomes characters. It is recorded per site
// (Params), because a change here changes every password on the machine, and a
// site that was created under version 1 has to keep deriving what it derived
// then.
//
// It is also mixed into the labels below, so two versions cannot collide even
// if they take the same parameters.
const Version = 1

// keyLen is what Argon2id is asked for, in bytes. Not a knob: 32 bytes is more
// entropy than any site policy can carry, so raising it would change every
// password to buy nothing, and lowering it would be a real loss.
const keyLen = 32

// Params is the cost of one derivation, recorded beside the site it belongs to.
//
// Stored rather than assumed. The defaults below will be raised as machines get
// faster, and a raise that silently re-derived every existing site would lock a
// person out of every account they have.
type Params struct {
	Version   int    `json:"version"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Lanes     uint8  `json:"lanes"`
}

// Default is what a site recorded today gets.
//
// RFC 9106's second recommended option (t=3, m=64 MiB, p=4), taken as it stands
// rather than tuned against this machine. A parameter set is chosen against a
// time budget on the hardware that will run it, and the honest way to pick one
// is to measure an idle machine - so the standard's number is what ships until
// somebody does that. Raising it later is a decision with a migration
// (Site.Restamp), which is why Params exists at all.
func Default() Params {
	return Params{Version: Version, Time: 3, MemoryKiB: 64 * 1024, Lanes: 4}
}

// The ceilings. Params comes out of a file a person can edit, and argon2 will
// allocate whatever it is told to: `"memory_kib": 68719476736` in a JSON file
// is not a strange password, it is this machine going down. So the file is
// bounded before the KDF sees it.
const (
	maxMemoryKiB = 1 << 20 // 1 GiB
	maxTime      = 64
	maxLanes     = 64
)

// Check says whether these parameters are ones this build will run.
func (p Params) Check() error {
	if p.Version != Version {
		return fmt.Errorf("derivation version %d: this zde derives version %d and cannot reproduce another one", p.Version, Version)
	}
	if p.Time < 1 || p.Time > maxTime {
		return fmt.Errorf("argon2id time %d: it has to be between 1 and %d", p.Time, maxTime)
	}
	if p.Lanes < 1 || p.Lanes > maxLanes {
		return fmt.Errorf("argon2id parallelism %d: it has to be between 1 and %d", p.Lanes, maxLanes)
	}
	// argon2's own floor, and it is a panic in x/crypto rather than an error.
	if p.MemoryKiB < 8*uint32(p.Lanes) {
		return fmt.Errorf("argon2id memory %d KiB is below the 8 KiB per lane argon2 needs for %d lanes", p.MemoryKiB, p.Lanes)
	}
	if p.MemoryKiB > maxMemoryKiB {
		return fmt.Errorf("argon2id memory %d KiB: zde will not ask for more than %d KiB, which is a file asking this machine to run out of memory", p.MemoryKiB, maxMemoryKiB)
	}
	return nil
}

// Derive is the password for one service at one counter.
//
// Pure: the same six inputs give the same bytes on any machine, which is the
// whole point - there is no vault to restore, only these. The answer is the
// caller's to Zero.
//
// master and pepper are read and not kept. Neither is ever part of an error
// returned from here, and no path in this package prints or logs either.
func Derive(master, pepper []byte, service string, counter uint64, p Params, pol Policy) ([]byte, error) {
	if err := p.Check(); err != nil {
		return nil, err
	}
	if err := pol.Check(); err != nil {
		return nil, err
	}
	if len(master) == 0 {
		return nil, fmt.Errorf("no master password was given")
	}
	if len(pepper) != PepperLen {
		return nil, fmt.Errorf("the pepper is %d bytes and this derivation needs %d", len(pepper), PepperLen)
	}
	if service == "" {
		return nil, fmt.Errorf("no service to derive for")
	}
	s := salt(pepper, service, counter, pol, p.Version)
	defer Zero(s)
	key := argon2.IDKey(master, s, p.Time, p.MemoryKiB, p.Lanes, keyLen)
	defer Zero(key)
	return pol.encode(newStream(key, label("encode", p.Version)))
}

// salt binds the pepper, the service, the counter and the policy into
// Argon2id's salt.
//
// HMAC and not a plain hash of the parts concatenated: the pepper is a key
// here, so without the file the salt is not computable and the offline attack
// on the master cannot be run at all. Every field is length-prefixed, so no two
// different messages can be built from the same bytes.
//
// The policy is in here and not only in the encoding, which is worth the line
// it costs. The stream the encoder draws from is a function of the Argon2id
// output alone, so one site whose policy changed - same service, same counter,
// sixteen characters instead of twenty - would draw the same bytes and land on
// a visibly related password. Salting with the policy makes the old one tell
// nothing about the new. Only the effective policy counts: Policy.canon puts
// the classes in one order and drops the exclusion of a character the alphabet
// never had, so a cosmetic edit to the file changes nobody's password.
func salt(pepper []byte, service string, counter uint64, pol Policy, version int) []byte {
	m := hmac.New(sha256.New, pepper)
	field(m, []byte(label("salt", version)))
	field(m, []byte(service))
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], counter)
	field(m, c[:])
	field(m, []byte(pol.canon()))
	return m.Sum(nil)
}

// label is the domain separator, carrying the version so that a version 2
// deriving with the same parameters cannot collide with a version 1.
func label(what string, version int) string {
	return fmt.Sprintf("zde-pass %s v%d", what, version)
}

// field writes b length-prefixed. Unambiguous encoding: without it the pair
// ("git", "hub") and ("gi", "thub") hash the same.
func field(w io.Writer, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	w.Write(n[:]) //nolint:errcheck // hash.Hash never fails a write
	w.Write(b)    //nolint:errcheck // as above
}

// stream is the encoder's supply of bytes: HMAC-SHA-256 in counter mode over
// the Argon2id output (NIST SP 800-108). Deterministic and unbounded, which is
// what generate-and-reject needs (policy.go).
type stream struct {
	key   []byte
	label string
	ctr   uint64
	buf   []byte
	pos   int
}

func newStream(key []byte, label string) *stream {
	return &stream{key: key, label: label}
}

func (s *stream) next() byte {
	if s.pos == len(s.buf) {
		m := hmac.New(sha256.New, s.key)
		field(m, []byte(s.label))
		var c [8]byte
		binary.BigEndian.PutUint64(c[:], s.ctr)
		m.Write(c[:]) //nolint:errcheck // hash.Hash never fails a write
		s.ctr++
		s.buf = m.Sum(s.buf[:0])
		s.pos = 0
	}
	b := s.buf[s.pos]
	s.pos++
	return b
}

// index is a uniform number below n, for n up to 256.
//
// Rejection and not a modulo: 256 does not divide 94, so `next() % 94` would
// make the first 68 characters of a 94-character alphabet likelier than the
// rest - a bias in every position of every password this derives.
func (s *stream) index(n int) int {
	limit := 256 - 256%n
	for {
		b := int(s.next())
		if b < limit {
			return b % n
		}
	}
}

// Zero overwrites b.
//
// Go guarantees nothing here and this does not pretend otherwise: the garbage
// collector may already have copied the value somewhere this cannot reach, the
// page may be in swap, and a compiler is free to elide a write nothing reads.
// It is what can be done rather than a guarantee, and it is worth doing because
// the copy it does reach is the long-lived one.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}
