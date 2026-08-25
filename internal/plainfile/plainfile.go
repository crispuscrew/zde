// Package plainfile opens a file zde is about to believe.
//
// It uses O_NONBLOCK so a FIFO cannot hang startup, then validates kind,
// ownership and mode from the opened descriptor so a path swap cannot evade
// the check. Open accepts this user's files and immutable root-owned store
// files; OpenNoFollow also refuses a final symlink for state zde writes.
//
// It does not defend against this user or root, and it deliberately permits
// symlinked parent directories. Build-time tools must not use it: root-owned
// store paths appear as overflow-owned inside a Nix build user namespace.
package plainfile

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// Open opens path for reading.
//
// A symlink at the last component is followed. That is deliberate and it is
// what separates this from OpenNoFollow: zde's config files are installed by
// home-manager, which writes them as symlinks into the nix store (nix/home.nix
// says so out loud where it explains why dynamic.kdl had to be an exception).
// Refusing a symlink here would refuse to read apps.json on every machine that
// installs zde the supported way, so the defence has to be about what the link
// lands on rather than about the link.
func Open(path string) (*os.File, error) { return open(path, 0) }

// OpenNoFollow is Open but refuses a final symlink, for state zde writes.
func OpenNoFollow(path string) (*os.File, error) { return open(path, syscall.O_NOFOLLOW) }

func open(path string, extra int) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|extra, 0)
	if err != nil {
		return nil, err
	}
	if err := check(f, path); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Read is Open with a ceiling on how much comes back.
//
// The ceiling is the caller's, because the answer differs: a desk manifest that
// is a megabyte is a mistake, and a journal that is a megabyte is a Tuesday.
// max+1 is read so that a file exactly at the limit is accepted and the first
// byte past it is what refuses, rather than a file at the limit being
// indistinguishable from one twice as long that was silently truncated.
func Read(path string, max int64) ([]byte, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return read(f, path, max)
}

// ReadPrivateNoFollow reads owner-only 0600 state without following its final
// path component.
func ReadPrivateNoFollow(path string, max int64) ([]byte, error) {
	f, err := OpenNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%s must be owned by uid %d with mode 0600", path, os.Getuid())
	}
	return read(f, path, max)
}

func read(f *os.File, path string, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes, which nothing zde reads here is", path, max)
	}
	return data, nil
}

// check is the descriptor asked what it actually is.
func check(f *os.File, path string) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is %s and not a file: zde will not read it", path, kind(fi.Mode()))
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: cannot tell who this file belongs to", path)
	}
	if !trusted(int(st.Uid), os.Getuid(), fi.Mode()) {
		if int(st.Uid) == 0 {
			// Told apart from another account's, because it is a different thing
			// to fix. This is a root-owned file that root can still write, which
			// is every ordinary file in /etc and none of the ones zde is
			// installed from.
			return fmt.Errorf("%s belongs to root and is not read-only, so it is not one of the store paths "+
				"zde is installed from and not zde's to act on", path)
		}
		return fmt.Errorf("%s belongs to another account, so it is not zde's to act on", path)
	}
	return nil
}

// trusted accepts this user's files and root-owned immutable files. The latter
// is the property Nix store files have; accepting every root-owned file would
// also accept ordinary writable configuration under /etc.
func trusted(owner, us int, mode os.FileMode) bool {
	if owner == us {
		return true
	}
	// 0222 and not 0022: what makes a store path a store path is that nobody
	// writes it, root included.
	return owner == 0 && mode.Perm()&0o222 == 0
}

func kind(m os.FileMode) string {
	switch {
	case m&os.ModeNamedPipe != 0:
		return "a named pipe"
	case m&os.ModeSocket != 0:
		return "a socket"
	case m&os.ModeDevice != 0:
		return "a device"
	case m.IsDir():
		return "a directory"
	}
	return "not a plain file"
}
