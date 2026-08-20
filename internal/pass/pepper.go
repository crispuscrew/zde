package pass

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// PepperLen is the pepper's size in bytes. 32, because it is an HMAC-SHA-256
// key and because it is the "have" half of the threat model: an attacker with a
// leaked site password and no pepper is guessing 256 bits, not a password.
const PepperLen = 32

// The modes. The pepper is the one secret zde keeps at rest, so it is 0600 in a
// 0700 directory, and a file that is not is refused rather than fixed - a
// pepper that has been group-readable has been read, and quietly narrowing it
// would hide that.
const (
	pepperMode = 0o600
	dirMode    = 0o700
)

// Dir is where pass keeps its two files.
//
// State and not config, for one reason that decides it: home-manager writes
// everything under ~/.config/zde as a symlink into the nix store, and the store
// is world-readable and root-owned. A pepper somebody put in their nix config
// would be a pepper every account on the machine can read, and it would look
// like it had worked. State is not a place anybody is tempted to generate from
// a config.
//
// The store file sits beside it rather than in config, because losing either
// one loses every password: they are one backup, and a backup split across two
// directories is a backup where half of it is missing.
func Dir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "zde", "pass")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "zde", "pass")
	}
	return filepath.Join(home, ".local", "state", "zde", "pass")
}

// PepperPath is the pepper's file.
func PepperPath() string { return filepath.Join(Dir(), "pepper") }

// ReadPepper is the pepper, or the reason zde will not use what is at path.
//
// Not internal/plainfile, and the difference is the point. That package trusts
// "ours, or a read-only root-owned file", because the second half is how a nix
// store path reads - which is right for a config file and wrong for a key: a
// pepper in the store is a pepper every account on this machine can read.
// Nothing but a file this account owns will do here, and nothing that anybody
// else can read or write.
//
// The rest is plainfile's rule and its reasons: O_NONBLOCK so a FIFO at the
// path answers instead of blocking forever, the kind and the owner read off the
// descriptor rather than off the path so neither can be swapped underneath,
// and O_NOFOLLOW because this is a file zde writes.
//
// The bytes are the caller's to Zero.
func ReadPepper(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("there is no pepper at %s: `zde pass init` writes one, and it is half of every password derived here - back it up", path)
		}
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a symlink, and zde will not follow one to a secret", path)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("%s: cannot tell who this file belongs to", path)
	}
	if err := refuse(path, fi.Mode(), int(st.Uid), os.Getuid(), fi.Size()); err != nil {
		return nil, err
	}
	b := make([]byte, PepperLen)
	if _, err := io.ReadFull(f, b); err != nil {
		Zero(b)
		return nil, err
	}
	return b, nil
}

// refuse is every reason zde will not derive from what is at this path.
//
// Its own function, taking the mode, the two uids and the size, for the reason
// internal/plainfile's trusted() is one: the decision can be tested, and no
// test can make a disk hold a file belonging to another account.
func refuse(path string, mode os.FileMode, owner, us int, size int64) error {
	if !mode.IsRegular() {
		return fmt.Errorf("%s is not a plain file, so it is not a pepper", path)
	}
	if owner != us {
		return fmt.Errorf("%s belongs to another account, and a secret zde does not own is not one it will derive from", path)
	}
	if perm := mode.Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s is mode %04o and the pepper has to be %04o: anything else has been readable by another account on this machine", path, perm, pepperMode)
	}
	if size != PepperLen {
		// The length is checked before the read, because a truncated pepper is
		// a weaker one and it would derive a different password for every site
		// without saying a word.
		return fmt.Errorf("%s is %d bytes and a pepper is %d: this is not the file zde wrote", path, size, PepperLen)
	}
	return nil
}

// CreatePepper writes a new pepper at path.
//
// It refuses to write over one. A second pepper changes every password this
// machine can derive, so the only safe way to replace one is deliberately, by
// hand, having understood that every account has to be changed - and the shape
// of that refusal is O_EXCL rather than a prompt, because a prompt is a thing
// somebody answers yes to.
func CreatePepper(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	// A directory that was there already keeps whatever mode it had, and this
	// one holds a key. Best effort: the file's own mode is what the read side
	// insists on.
	_ = os.Chmod(dir, dirMode)
	b := make([]byte, PepperLen)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	defer Zero(b)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, pepperMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s is already there, and a second pepper would change every password on this machine: move that file away yourself if replacing it is what you mean", path)
		}
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	// Before anything derives from it: a name pointing at a file whose contents
	// never reached the disk is every password on this machine.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
