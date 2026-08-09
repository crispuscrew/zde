// Package plainfile opens a file zde is about to believe.
//
// Everything zde reads at startup is a file somebody could have put something
// else at: the journal, the notification snapshot, the desk manifests, the
// app map the daemon execs out of. "Something else" is not usually another
// file. It is a FIFO, and opening one for reading blocks in the kernel until a
// writer arrives - which is never. That is not a slow start, it is a daemon
// that never reaches its own signal handling, and one `mkfifo` in the state
// directory was enough to leave a session with no zde at all, persistently,
// through every reboot.
//
// So the rule this package enforces is the one every caller already assumed:
// what is at that path is a plain file, belonging to somebody this session has
// no choice about trusting anyway. Three parts, and none of the three is
// enough on its own:
//
//  1. O_NONBLOCK, so the open returns rather than waiting. It is what makes a
//     FIFO answerable at all: without it the check below is never reached,
//     because the open is where the process stops.
//  2. The kind of file, read off the descriptor that was opened rather than
//     off the path. A path can be swapped between a stat and an open; a
//     descriptor cannot be swapped under a fstat.
//  3. Who it belongs to, on the same descriptor and for the same reason.
//
// O_NONBLOCK is dropped from the file's own behaviour by the check, not by
// clearing the flag: a plain file ignores it, and anything that would not have
// is refused before the caller ever reads a byte.
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

// OpenNoFollow is Open, and refuses a symlink at the last component with
// ELOOP.
//
// For the files zde writes rather than reads. A symlink at the journal is a
// redirection of a session's worth of notification summaries into a file
// somebody else named, and the write side has refused one since it was given
// O_NOFOLLOW; the read side following the same link would still have read the
// far end into memory before the write refused, which is half a defence.
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
		// Named by what it is, because the person who has to fix this is
		// looking at a path that appears to exist and a daemon that will not
		// start. "is a named pipe" is the whole diagnosis.
		return fmt.Errorf("%s is %s and not a file: zde will not read it", path, kind(fi.Mode()))
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		// A platform whose Stat cannot say who owns a file. Nothing zde builds
		// for is one, so this is fail-closed rather than a shrug: the check is
		// the point of opening through here.
		return fmt.Errorf("%s: cannot tell who this file belongs to", path)
	}
	if !trusted(int(st.Uid), os.Getuid()) {
		return fmt.Errorf("%s belongs to another account, so it is not zde's to act on", path)
	}
	return nil
}

// trusted is whose files this session will read a decision out of.
//
// Ours, obviously. And root's, because that is where the supported install
// puts them: every file home-manager writes is a symlink into the nix store,
// and every path in the store is owned by root and read-only. A rule of "mine
// only" would be a rule that refuses the ordinary case and admits nothing
// else, and root can rewrite this daemon's binary in any case, so there is
// nothing here for the rule to protect against.
//
// Its own function, taking both uids, for the reason internal/journal's
// chmodRefused is: the decision can be tested and the disk it runs on cannot
// be made to hold a file belonging to somebody else.
func trusted(owner, us int) bool { return owner == us || owner == 0 }

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
