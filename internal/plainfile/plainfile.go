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
// what is at that path is a plain file, and it is either ours or one of the
// read-only store paths zde is installed from. Three parts, and none of the
// three is enough on its own:
//
//  1. O_NONBLOCK, so the open returns rather than waiting. It is what makes a
//     FIFO answerable at all: without it the check below is never reached,
//     because the open is where the process stops.
//  2. The kind of file, read off the descriptor that was opened rather than
//     off the path. A path can be swapped between a stat and an open; a
//     descriptor cannot be swapped under a fstat.
//  3. Who it belongs to and whether anybody can write it, on the same
//     descriptor and for the same reason (see trusted).
//
// O_NONBLOCK is dropped from the file's own behaviour by the check, not by
// clearing the flag: a plain file ignores it, and anything that would not have
// is refused before the caller ever reads a byte.
//
// What this package is not, because the gap between the two is where somebody
// gets hurt:
//
//   - It is not a defence against a process running as this account. Such a
//     process can write these files, so anything it could reach by pointing one
//     of them at something else it could reach by editing it. The uid check is
//     for a third account's file, which is the case that exists wherever the
//     path is not in a directory only this account can write (internal/journal,
//     DefaultPath, and its /tmp fallback).
//   - It is not a defence against root, which owns half of what it accepts and
//     can rewrite this daemon's binary in any case.
//   - It says nothing about the directories above the file. O_NOFOLLOW
//     constrains the last component of the path and nothing else, so a
//     symlinked ~/.local/state, or an XDG_STATE_HOME on another disk, is
//     followed - which is deliberate, because that is the only symlink a real
//     setup puts near these paths, and because putting one there means writing
//     inside this account's own directories, which is the case above.
//
// What it is: the thing that stops a path zde reads from being something that
// is not a file, and the thing that stops it being somebody else's.
//
// One place it must not be used, written down here because it costs a red CI
// run to find out and nothing on a developer's machine shows it. Inside a nix
// build sandbox the ownership check below cannot be satisfied by a store path at
// all. The builder runs in a user namespace with a single uid mapping - the
// build user to sandbox-uid, 1000 by default - and host uid 0 is not in that
// map, so every root-owned file, which is every path in the store, reads inside
// the builder as the overflow uid, 65534. Neither half of trusted can match
// that, and this is not about how narrow the rule is: "ours or root's" refused
// it too. So a build-time tool reading its own input reads it plainly
// (internal/keymap, Load), and this package is for the session.
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

// trusted is whose files this session will read a decision out of, and it is
// narrower than "root's" because "root's" was not what the reason said.
//
// Ours, first, and this half is worth being honest about: against a process
// running as us it is not a defence at all, because a file that belongs to us is
// a file we trust and it is also a file that process can write. It is not there
// for that. It is there for the paths that are not in a directory only this
// account can write - internal/journal falls back to os.TempDir()/zde when
// os.UserHomeDir() fails, and `zded -journal /tmp/live.jsonl` is a supported
// thing to type - where what this refuses is a third account's file, which is
// the only case a uid comparison can decide.
//
// Then root's, and only because of the one thing that needs it: home-manager
// writes every file zde is configured by as a symlink into the nix store, and
// the nix daemon canonicalises what it puts there to root-owned and with no
// write bit set on it at all - 0444, or 0555 for something executable. That is
// the sentence this rule used to cite and did not enforce. `owner == 0` alone
// admits every root-owned file on the machine, and a symlink at apps.json
// pointing at /etc/os-release was read as an app map: root-owned, 0644, and
// nothing to do with the store.
//
// So the mode is asked as well, and it is asked for the property the store
// actually has rather than for the store's location. Not a `/nix/store/` prefix,
// which would be the same claim written down as a path: the store directory is
// a build-time setting, zde runs on installs that relocate it, and a defence
// that refuses somebody's whole config because their store is somewhere else is
// a defence that gets turned off. Not "nobody but root may write it" either -
// 0644 passes that, and 0644 is /etc.
//
// What this does not buy, said plainly rather than left to be assumed. It is not
// a defence against root, and nothing here could be: root can rewrite this
// daemon's binary. It is not a defence against us. What it is, is the difference
// between "a file root happens to own" and "a file that came out of the store",
// which is the difference the sentence above always claimed to be making.
//
// Its own function, taking the uids and the mode, for the reason
// internal/journal's chmodRefused is: the decision can be tested, and the disk a
// test runs on cannot be made to hold a root-owned file.
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
