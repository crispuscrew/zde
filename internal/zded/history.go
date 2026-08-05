package zded

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/crispuscrew/zde/internal/attn"
)

// The daemon's half of the notification snapshot (internal/attn, snapshot.go).
//
// attn owns what is written and how much of it; what is here is when. That
// split is the same one the rest of this file's package keeps: attn is the
// policy and the record, zded is the session - which desk you were on when
// something arrived, and whether that desk was one you told zde to keep off the
// disk.

// snapshotEvery is how often the history is written down while the session
// runs.
//
// It is a compromise about what a crash costs. Written only on the way out, a
// zded killed by an OOM or a compositor taking the session with it loses
// everything since login; written on every arrival, a machine receiving a
// hundred notifications a minute rewrites the whole file a hundred times a
// minute for records nobody will read until tomorrow. Two minutes is at most
// two minutes of arrivals lost, and thirty writes an hour of a file that is a
// few kilobytes long - next to a journal line with an fsync per notification,
// which the same arrivals already cost, it is nothing.
//
// And it does not write when nothing has changed (see SaveHistory), so an idle
// afternoon costs one write and not thirty an hour.
const snapshotEvery = 2 * time.Minute

// written is what the writer remembers between writes: the revision of the
// history that is in the file already.
//
// Its own lock rather than the server's. Writing the snapshot is disk work, and
// s.mu is held by everything that answers the socket - a status call queued
// behind a file write would be the daemon getting slower the more it had to
// remember.
type written struct {
	mu   sync.Mutex
	rev  uint64
	once bool // whether this session has written the file at all
}

// LoadHistory fills the history from the snapshot the last session left.
//
// Called before anything can arrive (cmd/zded), so what it puts back is the
// oldest end of the ring. The error is for saying so on stderr and nothing
// else: a snapshot that will not load is a session that starts with an empty
// notification center, which is where every first login starts anyway.
func (s *Server) LoadHistory(path string) error {
	records, err := attn.ReadSnapshot(path)
	// Restored first: a file that was partly readable is not a file to throw
	// away, and ReadSnapshot answers with both.
	s.history.Restore(records)
	return err
}

// SaveHistory writes the snapshot now, unless the file already holds this
// history.
//
// The check is what makes the clock above cheap. A laptop where nothing has
// arrived since the last write should not touch the disk every two minutes for
// the rest of the afternoon - it keeps a disk awake and wears a stick of flash
// for a file that would come out byte for byte the same.
//
// The first write of a session happens whatever the revision says. Nothing has
// changed at that point either, but the file on disk may be one this zde could
// not read, and rewriting it is how that stops being true.
func (s *Server) SaveHistory(path string) error {
	rev := s.history.Rev()
	s.written.mu.Lock()
	skip := s.written.once && s.written.rev == rev
	s.written.mu.Unlock()
	if skip {
		return nil
	}
	// The revision read before the records, deliberately. An arrival between
	// the two puts more in the file than this revision names, and the next pass
	// writes it again; the other order would call a record written that never
	// reached the file.
	if err := attn.WriteSnapshot(path, s.history.Snapshot()); err != nil {
		return err
	}
	s.written.mu.Lock()
	s.written.rev, s.written.once = rev, true
	s.written.mu.Unlock()
	return nil
}

// KeepHistory writes the snapshot on a clock until ctx ends, and once more on
// the way out.
//
// The write on the way out is the one that matters most and the one a caller
// has to wait for: it runs in this goroutine, so a daemon that returns from
// this call has written what it had (cmd/zded, run). A logout that raced the
// last write would be the ordinary way to end a session, and the ordinary way
// to lose the last two minutes of it.
func (s *Server) KeepHistory(ctx context.Context, path string) {
	tick := time.NewTicker(snapshotEvery)
	defer tick.Stop()
	save := func() {
		if err := s.SaveHistory(path); err != nil {
			// The log, not a refusal. A history that cannot be written is worth
			// finding out about from `journalctl --user -u zded`; it is not
			// worth any part of the session.
			log.Printf("zded: writing the notification history: %v", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			save()
			return
		case <-tick.C:
			save()
		}
	}
}

// privateArrival says whether something arriving on this desk must stay in
// memory (docs/vision.md, section 3: private desks are popups off, history
// only, capture-blocked).
//
// A notification body on disk is a different thing from one in a daemon's
// memory: it outlives the session, it is in a file with a name, and it is
// readable by anything that can read the user's state directory. A desk marked
// private is somebody saying that what arrives there is not to be left lying
// around, and this is the one place that promise is kept.
//
// Fail closed, which is principle 9, and it decides the three cases that are
// not a plain yes or no:
//
//   - the manifests cannot be read at all: we cannot tell which desk is
//     private, so nothing is written down.
//   - the desk is not declared, or nothing could say which desk it was (niri
//     unreadable, or a session where nothing is named yet): then it could have
//     been the private one, so it is refused whenever this machine declares a
//     private desk at all. On the ordinary machine, which declares none, there
//     is nothing to protect and the record is kept.
//   - one of the manifests would not parse: the broken file could be the
//     private desk's, and a typo must not be how a private desk stops being
//     one. `zde status` names the file (see rememberProblems).
func (s *Server) privateArrival(deskName string) bool {
	all, problems, err := s.desks.All()
	if err != nil {
		return true
	}
	s.rememberProblems(problems)
	if d, declared := all[deskName]; declared && deskName != "" {
		return d.Private
	}
	for _, d := range all {
		if d.Private {
			return true
		}
	}
	return len(problems) > 0
}
