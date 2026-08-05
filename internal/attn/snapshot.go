package attn

// The part of the history that outlives the daemon.
//
// A file of its own, and deliberately not the journal. The journal fsyncs per
// line and grows until it is compacted, so it is the wrong place for thousands
// of characters of attacker-controlled body (see PerSenderMax); this file is
// bounded, written whole, and renamed over the old one, so what it costs is the
// same whether it is written once or every two minutes.
//
// What it holds is the short answer to "what did I miss", not the long one. The
// rings in memory are the session's whole record; this is the front of the
// newest few of them, kept so that a reboot - or a rebuild that restarts zded,
// which is the common case - does not start the morning with an empty
// notification center.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// snapshotMax is how many records reach the file.
//
// Forty, against the rings' three hundred and ninety, because the two answer
// different questions. Inside a session "what did I miss" reaches back over a
// day of arrivals, and the rings are sized for that. Across a restart the
// question is narrower - what was happening when the session ended - and that
// is the last hour or two on any machine that receives notifications at all.
// Forty is a screenful of the notification center and a third: it draws 31 rows
// on a 1080p panel (shell/NotifCenter.qml - a 26-pixel row and a 2-pixel gap,
// in a panel bounded at the screen height less 80 with a 96-pixel footer). So
// it is more than the surface shows at once and less than a person would ever
// scroll back through after a reboot.
//
// It is also the number the arithmetic below is done with, and the two move
// together: doubling this doubles the file.
const snapshotMax = 40

// snapshotBodyMax is how much of a body reaches the file.
//
// The arithmetic, done the way SendersMax's is, and with one term that count
// does not have. A record at its limit is summaryMax (300 characters) plus this
// (400) plus a sender name, itself bounded at 300 by oneLine: a thousand
// characters. In memory that is 4 KB in the widest alphabet, but this one is
// counted in JSON, where encoding/json writes `<`, `>` and `&` as six-byte
// escapes - and every one of those thousand characters is an app's own text, so
// a thousand of them is the case to count: 6 KB, plus about 150 bytes of field
// names, a timestamp and a desk. No term for the actions, and that is not an
// omission - Snapshot drops them, for its own reasons, so the file is the one
// count in this package that never had to carry them. Forty of those is under
// 250 KB written whole, and a few kilobytes in a session made of real
// notifications. The same forty records with the rings' 4000-character body
// would be over a megabyte, and every ring full at its limit is the 9 MB
// SendersMax counts.
//
// 400 rather than the rings' 4000 because the bodies are what make a history
// large, so the bodies are the part that mostly stays in RAM. What 400
// characters buy is recognition: enough to know which message this was and
// whether it still matters, which is all a record is for once the session that
// received it has ended. Reading the whole of a message is something you do
// while it is still in front of you.
//
// Bounded on the way in as well as on the way out. The file is on a disk and
// disks are editable, so a record that came back longer than this would put the
// daemon's memory at the mercy of a text editor.
const snapshotBodyMax = 400

// snapshotBytesMax is the most of the file that is worth reading at all.
//
// Forty records at their limit is under 250 KB, so a megabyte is four times
// anything this zde could have written. A file past that is not a snapshot -
// it is a mistake, or somebody's idea of one - and reading it into the daemon's
// memory before finding out is exactly the failure the bounds above exist to
// stop.
const snapshotBytesMax = 1 << 20

// snapshotVersion is what a file this zde writes says about itself.
//
// Read strictly: a number this one does not know is a file this one does not
// touch. The alternative is guessing at fields a newer zde meant something
// different by, and being wrong about what a notification said is worse than
// having no history at all. A newer zde reading an older file has the same
// choice and makes it the same way, which is why this is an equality and not a
// floor: what changes the number is a change to what a record means.
const snapshotVersion = 1

// snapshot is the file: the version first, so a reader can refuse before it has
// made sense of anything else.
type snapshot struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

// DefaultSnapshotPath is where it lives: beside the journal, in state rather
// than cache, because losing it costs a person the answer to what happened
// while they were away and not a rebuildable file.
//
// Its own file rather than a section of the journal, for the reason at the top
// of this file. The fallbacks match internal/journal, DefaultPath: a home
// directory that cannot be found is a strange session, not a reason to have no
// daemon.
func DefaultSnapshotPath() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "zde", "history.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "zde", "history.json")
	}
	return filepath.Join(home, ".local", "state", "zde", "history.json")
}

// Snapshot is the part of the history that is written down: the newest
// snapshotMax records that may be written at all, oldest first, with their
// bodies cut to snapshotBodyMax.
//
// The newest across every sender, and not the newest of each. The file answers
// the same question the center does, so it is filled the same way the center
// is read (history.go, newestFirst) - the rings are a bound on what one app
// can hold, and a file that took every ring's newest would be the whole
// history, 390 records against these forty, holding a morning of one app
// rather than the last hour of the session.
//
// Oldest first because that is the order the history is read back in, so
// Restore puts them back without reversing anything and a person reading the
// file sees the morning above the afternoon.
//
// Two things do not come with them. A record that arrived on a private desk is
// left out entirely, which is the invariant this whole file is written around:
// private desks are history only, and a history on disk is a body somebody can
// read afterwards. The flag is the record's own, decided when it arrived
// (internal/zded, Arrived), so nothing here re-reads the manifests - and a desk
// declared private afterwards does not retract what is already in the file,
// which is argued where the flag is set. And the actions go, every time - they are the largest
// attacker-controlled part of a record (nine of them, each key and label
// bounded only by summaryMax), and none of them can be pressed after a restart
// because the connection that offered them is not on the bus any more. A file
// full of buttons that cannot work is the worst of both.
func (h *History) Snapshot() []Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Record, 0, snapshotMax)
	for _, e := range h.newestFirst() {
		if len(out) >= snapshotMax {
			break
		}
		r := e.rec
		if r.Private {
			continue
		}
		body, cut := clip(r.Body)
		r.Body = body
		// Never cleared: a record that came back clipped and is being written
		// again was cut once, whatever this pass had to do to it.
		r.Clipped = r.Clipped || cut
		r.Actions, r.Extra = nil, 0
		out = append(out, r)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// clip is a body as it goes to the file, and whether anything was left behind.
//
// Counted in characters rather than bytes, the way every other bound in this
// package is: a notification written in Cyrillic is not half a notification.
// Run over what comes back off the disk as well as over what goes to it,
// because a file a person can edit is a file that can hold anything.
func clip(body string) (string, bool) {
	cut := utf8.RuneCountInString(body) > snapshotBodyMax
	return clean(body, snapshotBodyMax, true), cut
}

// WriteSnapshot writes the records to path, replacing whatever was there.
//
// Written to a temporary file and renamed over the old one, the way the journal
// compacts: a crash halfway through leaves the previous snapshot whole, never a
// half-written one. That is also what makes reading it a plain unmarshal - a
// reader never meets a torn file, so a file that does not parse is a file
// something else wrote.
//
// The file is 0600 and stays that way: os.CreateTemp makes it so, and the
// rename carries the mode with it. These are notification bodies, which is the
// one thing in zde's state directory that is somebody's mail rather than their
// window layout.
func WriteSnapshot(path string, records []Record) error {
	dir := filepath.Dir(path)
	// 0700 for a directory this creates. It is usually the journal's, already
	// there and 0755, and this does not change it - the file's own mode is what
	// this can be sure of.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot{Version: snapshotVersion, Records: records})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a failure below leaves nothing behind
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Before the rename, or a machine that loses power keeps a name pointing at
	// a file whose contents never reached the disk.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadSnapshot is what the last session wrote, oldest first, ready for Restore.
//
// Every way this can fail answers with no records and an error the caller can
// print. None of them is a reason to stop: this is history, and the daemon is
// the session (docs/vision.md, section 2 - everything else in zde talks to
// zded). A file that is missing, unreadable, corrupt, truncated, absurdly
// large, or written by a zde that numbers its snapshots differently all mean
// the same thing to a person logging in - the center starts empty - and none of
// them means the desks should not come up.
//
// What comes back is checked rather than trusted, because between two sessions
// the file was a file: anything can have edited it. Records are re-bounded,
// ones that cannot be addressed are dropped, and the two marks that say what a
// restored record is are set here rather than read, so that a file cannot claim
// its rows are live.
func ReadSnapshot(path string) ([]Record, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		// A first login, or a session that ended before it wrote one. Not a
		// failure, and not worth a line on anybody's stderr.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, snapshotBytesMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > snapshotBytesMax {
		return nil, fmt.Errorf("%s is larger than %d bytes, which no notification snapshot this zde wrote could be: it is being ignored", path, snapshotBytesMax)
	}
	var file snapshot
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s does not read as a notification snapshot, so this session starts with none: %w", path, err)
	}
	if file.Version != snapshotVersion {
		return nil, fmt.Errorf("%s says it is snapshot version %d and this zde writes %d, so this session starts with none", path, file.Version, snapshotVersion)
	}
	out := make([]Record, 0, len(file.Records))
	for _, r := range file.Records {
		if r.ID == 0 || r.Text == "" {
			// An id addresses nothing and a record with no summary draws an
			// empty row. Both are things only an edited file can contain, and
			// dropping the row beats showing one nothing can act on.
			continue
		}
		r.Text = oneLine(r.Text)
		r.From = oneLine(r.From)
		body, cut := clip(r.Body)
		r.Body, r.Clipped = body, r.Clipped || cut
		// Set here, not read: a file that could say a row was live would be a
		// file that could hide what it had been cut down to.
		r.Restored = true
		// The file never carries these two - Snapshot drops the actions and
		// leaves out the private records altogether - but a hand-written one
		// might, and the meaning of both is decided in this process.
		r.Actions, r.Extra = nil, 0
		r.Private = false
		out = append(out, r)
	}
	if len(out) > snapshotMax {
		// The newest, for the reason the bound exists at all. A longer file is
		// one this zde did not write.
		out = out[len(out)-snapshotMax:]
	}
	return out, nil
}
