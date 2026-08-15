// Package journal keeps the little that workspace names cannot: where you were
// on each desk, and which desk you were on (docs/model.md, section 3). The desk
// map itself is never in here - that is rebuilt from names, and a second copy
// of it would be a second thing to disagree with niri.
//
// It is append-only and replayed at open. That shape is chosen for how it
// fails: a write torn by a crash costs the last line and nothing else, where a
// rewritten state file can lose everything at once. Entries are keyed by desk
// and monitor rather than by niri's workspace id, because ids do not survive a
// compositor restart and this state has to.
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"github.com/crispuscrew/zde/internal/attn"
	"github.com/crispuscrew/zde/internal/desk"
	"github.com/crispuscrew/zde/internal/plainfile"
)

// The modes this file keeps for itself: readable by the person it belongs to
// and by nobody else, and the same for the directory zde makes to hold it.
//
// It was 0644 until this was written, and the reason first written down here
// was that some distributions leave a home directory open. Do not put that
// back. It is false on the ones it named, and it is the wrong shape of
// argument besides: a mode that is only right when the directory above it
// happens to be right is not a promise this file can make. Three things make
// 0600 the one to write down, and all three hold on every machine:
//
//  1. The directory is not always a private one. DefaultPath honours
//     XDG_STATE_HOME wherever it points, and falls back to
//     os.TempDir()/zde/journal.jsonl when os.UserHomeDir() fails. Neither of
//     those is constrained to a directory belonging to one person, and on /tmp
//     a 0644 journal is readable by every account on the machine.
//  2. What is in it is correspondence. The queue carries the summary, the
//     sender and the urgency of every notification that reached it (see Item):
//     who wrote to you and what about, kept until you finish the item.
//  3. A mode travels with the file and a directory's mode does not. 0600
//     survives a tarball, an `rsync -a` and a restored backup; "the directory
//     this came out of happened to be 0700" survives none of them.
//
// So this is refusing to depend on the directory above being right, rather
// than closing a hole somebody's distribution left open.
const (
	journalMode  = 0o600
	stateDirMode = 0o700
)

// compactAt is when a replay is long enough to be worth rewriting.
//
// It was chosen when the journal held desk switches, which are human-paced, and
// on that traffic it is days of use. It is not any more: every notification
// writes a line here too, a queued one to record the item and a silenced one to
// spend its id, so a machine that receives a hundred notifications a minute
// passes a thousand entries in ten.
//
// What that costs is a longer replay at the next login, and a file that grows
// for as long as the session runs - compaction happens at Open and nowhere
// else, so nothing shortens it in between. Not fixed here: compacting under a
// running daemon means rewriting the file every zded is appending to, and that
// is a change to make on purpose rather than as a footnote to the modes.
const compactAt = 1000

// QueueMax is how many things may be waiting at once.
//
// The queue was the one thing in zde with no bound on it. Everything else that
// grows with what arrives already has one and says why: the notification
// history is a ring of PerSenderMax records for each of SendersMax senders, the
// summary is cut at 300 characters, the body at 4000 (internal/attn). This was
// not, and the shape of what that costs is worth writing down because it has
// already happened once - a flood filled a 16 GB tmpfs and took every shell on
// the machine with it.
//
// The arithmetic. One queued line is the summary and the sender at their
// ceiling, which is 300 characters each and up to four bytes a character in the
// alphabet somebody writes in, plus the id, the desk and the JSON around them:
// about 2.5 KB at the very worst, and around 130 bytes for the ordinary line an
// ordinary program sends. Measured on a live daemon at 1065 arrivals a second,
// which is 2.6 MB/s of fsynced appends at the worst case. Nothing shortened it:
// compaction runs at Open and keeps everything still waiting, so a 49 MB
// journal of queued entries compacted to 49 MB.
//
// A thousand is what that becomes: 2.5 MB of live queue at the ceiling, which
// is what a compaction leaves behind and what `queue.list` marshals for the bar
// every two seconds. It is also far more than a queue can be and still be one -
// the point of the thing is what you still owe, and nobody owes a thousand.
// Under the cap the file still grows while the session runs, because an arrival
// past it spends an id and that is a line too (see ClaimID) - but at 27 bytes
// rather than 2500, and those lines are exactly what compaction collapses to
// one. So the bound this really buys is the one that was missing: after it, a
// restart shortens the journal to something with a ceiling.
//
// It is the ceiling on the file and on the memory, and it is not on its own a
// bound on anything else. Who may spend it is PerSenderMax and Reserved, below.
//
// Deliberately not enforced on replay, and neither are the two below. A journal
// written before these caps can hold more, and refusing to load it would delete
// what somebody already owes to enforce a number invented afterwards. It loads
// whole, and nothing new is taken until it drains - and `zde queue clear` is
// what drains it in one call rather than a thousand (see Clear).
const QueueMax = 1000

// PerSenderMax is how many things one sender may have waiting at once.
//
// The bound above was a bound on the queue and not on anybody in it, which
// means it was a bound the first caller to reach it spent on everybody else.
// One app on the session bus filled all thousand places through Notify, with
// nothing authenticating it, and after that a second app was refused, an urgent
// arrival was refused, and a person typing `zde queue add` was refused. The
// number was right and the shape of it was the whole failure: a queue with a
// ceiling and no shares is a queue whose ceiling belongs to whoever gets there
// first.
//
// internal/attn had already answered this for the half of attn that is the
// history - a ring of records for each of a bounded number of senders - against
// exactly this threat, and this file had taken the number without the argument.
//
// What is deliberately not taken from there is eviction, and that is the
// difference between the two halves. The history is what already happened, so
// dropping its oldest record costs a row nobody was going to read. The queue is
// what you still owe, and its promise is that what interrupted you is worth as
// much tomorrow morning as it was last night (see State.Queue). Dropping an
// item is breaking that promise quietly, and it is also the flood winning: a
// chatty program would erase everything real you owed and then hold the list
// itself. So this bounds by refusing, as the ceiling always did. What changes is
// whose room is being refused - one sender's own, and nobody else's.
//
// Thirty, which is the number the history keeps per sender and for a related
// reason: an arrival makes both a queue item and a history record, and a queue
// holding more of one sender's items than that sender's ring could show would be
// owing things this session can no longer look up. Thirty is a day or two of one
// ordinary app, and it is far past what anybody acts on - and a refusal here is
// not a lost notification, because an arrival the queue has no room for is still
// recorded, still draws its popup and still takes an id (see ErrSenderFull).
const PerSenderMax = 30

// Reserved is how much of QueueMax nothing off the bus can spend.
//
// A per-sender bound alone is a bound on a name, and a name is free: From is the
// sender's own claim and nothing verifies it (internal/attn, Notification.From).
// An app that varies what it calls itself mints a share per variation, which is
// the same leak one level up - and thirty-four names would have had the whole
// thousand and refused a person again.
//
// So a part of the queue is kept for the entries the bus cannot produce, and the
// reason it is a real reservation rather than a wish is that the emptiness of
// From is not a claim anybody can make. A notification that gives no app name,
// or gives "-", or gives "zde", is recorded under the bus's own name for its
// connection instead, and the bus hands that out rather than letting the peer
// choose it (internal/attn, claim). So an item with no sender is a person who
// typed `zde queue add`, and an item from attn.SelfFrom is the desktop's own
// message about a desk that could not start its apps - and nothing on the bus
// can be either.
//
// The same two names internal/attn refuses to evict, for the same argument
// (history.go, unevictable): a name no app can mint is not part of the leak the
// bound exists to stop, and leaving zde's own in the count is the bound working
// for the attacker - "this desk could not start three of its apps" is one item,
// the cheapest thing on the machine, so a flood would refuse the desktop's own
// words first.
//
// A hundred, and it is a number that has to be reachable rather than one that
// gets used: nobody types a hundred reminders. What it costs is a tenth of a
// ceiling that is already ten times what a queue can be and still be one.
const Reserved = 100

// ErrQueueFull is the whole queue at its ceiling, told apart from a journal that
// could not be written.
//
// Its own error because the two want opposite answers. A write that failed
// means the daemon cannot record anything and the arrival should fail; a full
// queue means this session already has more waiting than it can act on, and the
// notification still has to land - it is recorded, it draws its popup, and it
// gets its id. Only its place in the queue is refused.
//
// The text is the whole message rather than a label, because one of the two
// callers hands it straight to a person: `zde queue add` on a full queue prints
// this and nothing else, and "the queue is full" with no number and no way out
// of it is the kind of refusal somebody has to go and read the source about.
// This is also the only refusal a person can ever be given, because the other
// two are about senders and a person is not one - so it is the only one whose
// words have to work in a terminal.
var ErrQueueFull = fmt.Errorf("%d things are already waiting, which is as many as the queue holds: "+
	"`zde queue` is the list, `zde queue done <id>` is how it gets shorter, "+
	"and `zde queue clear` empties it", QueueMax)

// ErrSenderFull is a sender being refused its own share, or everything with a
// sender being refused theirs.
//
// Told apart from the one above because it means something different to whoever
// reads the log: this queue is not full, this sender's part of it is, and the
// rest of the session is unaffected. Nobody types their way to this one - a
// person's entries have no sender - so unlike ErrQueueFull it is written for a
// daemon's log rather than for a terminal, and what it is wrapped in says which
// name and which number.
var ErrSenderFull = errors.New("what it sent is shown and recorded, and does not go on the queue")

// entry is one line of the journal.
type entry struct {
	Kind    string `json:"kind"`
	Desk    string `json:"desk,omitempty"`
	Monitor string `json:"monitor,omitempty"`
	Slot    string `json:"slot,omitempty"`
	To      string `json:"to,omitempty"`
	ID      uint64 `json:"id,omitempty"`
	Text    string `json:"text,omitempty"`
	// Body was the whole of a notification, and nothing writes it any more (see
	// Item). It is still read, because a journal an earlier zde wrote is full of
	// them: noticing one is what makes Open rewrite the file without it.
	Body   string `json:"body,omitempty"`
	From   string `json:"from,omitempty"`
	Urgent bool   `json:"urgent,omitempty"`
	// Mode is the attn mode a "mode" entry sets. Its own field rather than
	// borrowed from To: a mode is not a workspace name, and a reader looking at
	// the file should not have to know which kinds put what where.
	Mode string `json:"mode,omitempty"`
}

const (
	kindActive   = "active"   // on this desk and monitor, this slot was focused
	kindLastDesk = "lastdesk" // the desk to go back to
	kindOnDesk   = "ondesk"   // the desk you are on now
	kindRenamed  = "renamed"  // a workspace was renamed, so entries move with it
	kindQueued   = "queued"   // something is waiting, and which desk it waits on
	kindDone     = "done"     // it is not waiting any more
	kindCleared  = "cleared"  // nothing is waiting any more: the whole queue at once
	kindLastID   = "lastid"   // the highest queue id handed out, so none repeats
	kindMode     = "mode"     // what arrivals are allowed to do (internal/attn)
	kindBorrowed = "borrowed" // the desk whose declared mode is in force, and the mode it displaced
)

// State is what the journal remembers. It is a value: callers get a copy and
// cannot reach back into the journal through it.
type State struct {
	// LastActive is desk -> monitor -> slot: what to focus on each monitor
	// when this desk comes back (invariant 5).
	LastActive map[string]map[string]string
	// LastDesk is the desk to return to, for desk.last.
	LastDesk string
	// OnDesk is the desk you are on. Focus alone cannot answer that: a window
	// opened onto a fresh workspace has no name yet, and that is exactly when
	// something has to know which desk it belongs to.
	OnDesk string
	// Queue is what is waiting, oldest first. It survives a logout because
	// what you were interrupted by is worth as much tomorrow morning as it
	// was last night (docs/model.md, section 3).
	Queue []Item
	// Mode is the attn mode: what an arrival is allowed to do (internal/attn).
	// Here rather than in the daemon's memory because a mode that resets to the
	// default when zded restarts is a mode that lies about why nothing is
	// arriving - and zded restarts on every rebuild that touches it.
	//
	// Kept as the string it was given. The journal has no opinion about which
	// modes exist; whoever reads it parses, and an entry from a newer zde
	// replays into a name this one does not know rather than into a refusal.
	Mode string
	// Borrowed is the desk whose manifest is deciding the mode above, and what
	// the mode was before it did (docs/model.md, section 5: policies.attn).
	Borrowed Borrowed
}

// Borrowed is a desk's attn policy in force. The zero value is nobody having
// borrowed anything, which is a session whose mode is its own.
//
// It is written down beside the mode rather than kept in the daemon for the
// reason the mode is: zded restarts on every rebuild that touches it, and a
// restart that forgot which desk had lent a mode would leave that mode behind
// on the next desk you walked to, with nothing anywhere saying why. The pair
// has to survive together or it disagrees with itself.
type Borrowed struct {
	// Desk is the borrower. It is the desk being left that has to be
	// recognised, so the name is kept rather than derived from OnDesk: OnDesk
	// is where you are now, and by the time the mode is given back that is the
	// desk you arrived on.
	Desk string
	// Mode is what was in force before the desk took it, and what goes back
	// when you leave. Kept as a string for the reason Mode is.
	Mode string
}

// Item is one thing waiting. The desk is where it belongs, which is what
// separates a queue from a list: attn can show a desk only its own, and
// queue-jump has somewhere to go.
//
// What it does not carry is the body of the notification it came from. An item
// used to keep the whole message, and the message is the part of a notification
// that is thousands of attacker-controlled characters (docs/vision.md,
// principle 3): the journal fsyncs a line per arrival and is compacted only at
// Open, so every message anybody sent went to the disk and stayed there for the
// session and past it. Nothing was reading it back - the queue is one line an
// item in `zde queue`, on the bar and in queue-jump, and the message under a
// row is the notification center's, which reads the history record and not this
// (internal/attn, Record).
//
// A desk declared private is what makes that decisive rather than tidy
// (docs/vision.md, section 3: private desks are history only). A body from one
// of those desks sitting in a file is the exact thing that flag exists to
// prevent, and a rule that dropped it only for those desks would still be a
// rule that has to ask a manifest, at arrival time, what a desk was. Keeping no
// body at all needs nothing asked, and it is checkable by reading the file.
//
// What stays is the least a queue can be and still be one: the id, the desk,
// the sender, the urgency, and the summary that is the row. The queue is the
// half of attn that survives a restart because it is what you still owe, and a
// private desk is where the things you owe are personal, so dropping the item
// instead would be the flag deciding what is kept - which principle 3 forbids.
type Item struct {
	ID   uint64 `json:"id"`
	Text string `json:"text"`
	Desk string `json:"desk,omitempty"`
	// From is what sent it, as it described itself. Empty when a person typed
	// it. Nothing verifies it - see internal/attn.
	From string `json:"from,omitempty"`
	// Urgent is the sender's claim that this should interrupt rather than
	// wait. It is a claim too, and attn's modes are what will act on it.
	Urgent bool `json:"urgent,omitempty"`
}

func newState() State {
	return State{LastActive: map[string]map[string]string{}}
}

// Journal is an open journal file. It is safe for concurrent use: zded will
// have niri events and IPC calls arriving on different goroutines.
type Journal struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	state   State
	lastID  uint64
	entries int
	skipped int
	// bodies is how many replayed lines carried a notification body. Only a
	// journal written by an earlier zde can have any, and one is enough to make
	// Open rewrite the file (see Item).
	bodies int
}

// DefaultPath is where the journal lives: state, not config and not cache -
// losing it costs you your place, not your setup.
func DefaultPath() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "zde", "journal.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "zde", "journal.jsonl")
	}
	return filepath.Join(home, ".local", "state", "zde", "journal.jsonl")
}

// Open replays the journal at path, creating it if it is not there.
//
// A line that does not parse is skipped rather than fatal, and counted in
// Skipped. Refusing to start because the tail of a log is torn would trade a
// forgotten desk position for no session at all.
func Open(path string) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), stateDirMode); err != nil {
		return nil, err
	}
	// A directory that was already there keeps whatever mode it had, which on a
	// machine that has run an earlier zde is 0755. Tightened here - but only
	// when it is the directory zde picked for itself, because `zded -journal
	// /tmp/live.jsonl` puts the journal somewhere belonging to the whole
	// machine, and taking /tmp private would do far more harm than the listing
	// it stops. Best effort either way: the file's own mode is what keeps the
	// lines unreadable, and this only decides whether another account can see
	// that zde keeps a journal at all.
	if dir := filepath.Dir(path); dir == filepath.Dir(DefaultPath()) {
		_ = os.Chmod(dir, stateDirMode)
	}
	j := &Journal{path: path, state: newState()}
	if err := j.replay(); err != nil {
		return nil, err
	}
	// O_NOFOLLOW refuses a symlink sitting at journal.jsonl itself, with ELOOP,
	// rather than opening whatever it points at. It constrains the last
	// component and nothing above it, so a symlinked ~/.local/state, or an
	// XDG_STATE_HOME on another disk, still works - which is the only symlink a
	// real setup puts anywhere near this path.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, journalMode)
	if err != nil {
		return nil, err
	}
	// And the mode of a journal that was already there, which O_CREATE does not
	// touch. An earlier zde made this file 0644 and every notification since has
	// gone into it, so a fix that reached only new files would leave every
	// machine that has been running zde as exposed as it was and call it done.
	// Safe to do to a file somebody already has: this is zde's own journal, zded
	// runs as the person who owns it, and no mode narrower than 0600 could ever
	// have worked - so there is no setup this takes anything away from.
	//
	// Two mechanisms against two different swaps, and neither covers the
	// other's. The chmod is on the descriptor rather than on the path, so what
	// is tightened is the file that was just opened and not whatever the name
	// has come to point at since. O_NOFOLLOW above refuses a symlink that was
	// already at the name before the open, which a descriptor chmod would
	// cheerfully tighten the far end of.
	if err := tighten(f, path); err != nil {
		f.Close()
		return nil, err
	}
	j.file = f
	// Compaction, now for either of two reasons. A long replay is the old one.
	// The other is a journal an earlier zde wrote, which put the whole of every
	// notification in here: rewriting it is what takes those bodies off the
	// disk, rather than leaving them until the entry count happens to pass
	// compactAt on some later day. It is this or nothing - compaction runs at
	// Open and nowhere else.
	if j.entries > compactAt || j.bodies > 0 {
		if err := j.compactLocked(); err != nil {
			j.file.Close()
			return nil, err
		}
	}
	return j, nil
}

// tighten makes an open journal 0600 and decides what a refusal means.
//
// Whose file it is, asked rather than guessed from the errno. An EPERM says
// the kernel refused and not why, and the two ways it happens want opposite
// answers: a journal belonging to somebody else, and a filesystem with no
// permission bits to set. The old code read every failure as the first, which
// made the second cost a person their session.
func tighten(f *os.File, path string) error {
	err := f.Chmod(journalMode)
	if err == nil {
		return nil
	}
	return chmodRefused(path, err, ownerOf(f), os.Getuid())
}

// ownerOf is the uid an open file belongs to, or -1 when the descriptor cannot
// say. Off the descriptor, which has the same immunity the chmod has: it names
// the file that was opened, not whatever the path points at now.
//
// -1 never equals a uid, so a file whose owner cannot be read is treated as
// somebody else's, which is the fail-closed way round.
func ownerOf(f *os.File) int {
	fi, err := f.Stat()
	if err != nil {
		return -1
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(st.Uid)
}

// chmodRefused is the choice between the two failures.
//
// Somebody else's file: fatal, which is principle 9. A journal that is not
// yours is not one to be appending your notification summaries to, whether or
// not its mode could have been fixed.
//
// Yours, and the chmod failed anyway: said once and carried on. vfat, exFAT
// and some 9p and SMB mounts cannot represent a mode at all, and refusing to
// start there costs a person their whole session to enforce a property the
// filesystem was never able to have. Once by construction on the path that
// matters: Open runs once per journal, and the only other caller is a
// compaction, which a session does not repeat.
func chmodRefused(path string, err error, owner, us int) error {
	if owner != us {
		return fmt.Errorf("%s belongs to another account, and it is where your notification summaries are written down: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "zde: %s is yours but cannot be made %04o (%v); the filesystem under it has no permission bits, so what is written there is as private as the directory holding it and no more\n", path, journalMode, err)
	return nil
}

func (j *Journal) replay() error {
	// The same refusal the write open below makes, and made here as well
	// because this one happens first. O_NOFOLLOW on the write open was the
	// whole defence, and it was reached only after this had already opened
	// whatever was at the name and read it into the daemon's memory - so a
	// symlink was half-followed and a FIFO was not refused at all: a plain
	// os.Open of one waits in the kernel for a writer that never comes, with
	// the whole of zded still ahead of it and no signal handling yet reached.
	// One mkfifo and the session had no daemon, through every reboot
	// (internal/plainfile).
	f, err := plainfile.OpenNoFollow(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			j.skipped++
			continue
		}
		// Counted here rather than in apply, which also runs on what this
		// process writes: nothing this zde writes has a body, so a body is
		// always a line off the disk (see Item).
		if e.Body != "" {
			j.bodies++
		}
		j.entries++
		j.apply(e)
	}
	// A torn final line reads as an unterminated token, not an error, so the
	// only errors here are real ones: a bad disk, or a line past the cap.
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (j *Journal) apply(e entry) {
	switch e.Kind {
	case kindActive:
		if e.Desk == "" || e.Monitor == "" {
			j.skipped++
			return
		}
		if j.state.LastActive[e.Desk] == nil {
			j.state.LastActive[e.Desk] = map[string]string{}
		}
		j.state.LastActive[e.Desk][e.Monitor] = e.Slot
	case kindLastDesk:
		j.state.LastDesk = e.Desk
	case kindOnDesk:
		j.state.OnDesk = e.Desk
	case kindQueued:
		if e.ID == 0 || e.Text == "" {
			// An item with no id cannot be finished and one with no text says
			// nothing. Counted rather than shown: a blank row with id 0 looks
			// like the queue's fault, and doctor reports the count.
			j.skipped++
			return
		}
		// e.Body is dropped rather than carried into the item: a queue built out
		// of an older journal is the same queue, and the body it used to hold is
		// on its way off the disk (see Open).
		j.state.Queue = append(j.state.Queue, Item{ID: e.ID, Text: e.Text, Desk: e.Desk, From: e.From, Urgent: e.Urgent})
		if e.ID > j.lastID {
			j.lastID = e.ID
		}
	case kindLastID:
		if e.ID > j.lastID {
			j.lastID = e.ID
		}
	case kindMode:
		j.state.Mode = e.Mode
	case kindBorrowed:
		// An empty desk is the real state "nobody has it" rather than a torn
		// line: it is how a mode chosen by hand ends the loan (internal/zded,
		// setMode), and skipping it would leave the file claiming a desk still
		// holds a mode it gave up.
		j.state.Borrowed = Borrowed{Desk: e.Desk, Mode: e.Mode}
	case kindDone:
		for i, it := range j.state.Queue {
			if it.ID == e.ID {
				j.state.Queue = append(j.state.Queue[:i], j.state.Queue[i+1:]...)
				break
			}
		}
	case kindCleared:
		// Everything queued before this line, and nothing after it: the replay
		// is in order, so one entry is the whole of what a thousand `done` lines
		// used to be. The ids are not given back - lastID is untouched - because
		// an id is spent when it is handed out and a number reused would let an
		// app close something somebody typed (see ClaimID).
		j.state.Queue = nil
	case kindRenamed:
		j.applyRename(e)
	default:
		// An entry from a newer zde. Counted, not discarded: it stays in the
		// file, and compaction is what would drop it.
		j.skipped++
	}
}

// applyRename moves a remembered position when the workspace holding it is
// renamed. The name is the record, so when it changes the journal follows it
// or starts pointing at a workspace that is not there any more.
func (j *Journal) applyRename(e entry) {
	from, err := desk.ParseName(e.Desk)
	if err != nil {
		j.skipped++
		return
	}
	to, err := desk.ParseName(e.To)
	if err != nil {
		j.skipped++
		return
	}
	byMonitor := j.state.LastActive[from.Desk]
	if byMonitor == nil || byMonitor[from.Monitor] != from.Slot {
		return // the renamed workspace was not the remembered one
	}
	delete(byMonitor, from.Monitor)
	byMonitor[to.Monitor] = to.Slot
}

// State returns a copy of what the journal remembers.
func (j *Journal) State() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := State{
		LastActive: make(map[string]map[string]string, len(j.state.LastActive)),
		LastDesk:   j.state.LastDesk,
		OnDesk:     j.state.OnDesk,
		Mode:       j.state.Mode,
		Borrowed:   j.state.Borrowed,
		Queue:      append([]Item(nil), j.state.Queue...),
	}
	for d, byMonitor := range j.state.LastActive {
		m := make(map[string]string, len(byMonitor))
		for mon, slot := range byMonitor {
			m[mon] = slot
		}
		out.LastActive[d] = m
	}
	return out
}

// Waiting is what is on the queue, oldest first, as a copy.
//
// Its own method rather than State().Queue because of who asks and how often. A
// person reads the queue when they wonder what they owe; the bar reads it on a
// clock, and so does the mode below it, which between them is sixty questions a
// minute for as long as the session runs. State copies everything the journal
// remembers to answer any of them - the queue, and a map of desks with a map of
// monitors inside each - so reading one field cost a copy of all of them.
//
// Still a copy of the field: a caller that could reach back in here through the
// slice it was handed would be able to edit the queue without writing a line.
func (j *Journal) Waiting() []Item {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Item(nil), j.state.Queue...)
}

// Mode is the attn mode as it was written down, and its own method for the
// reason Waiting is: the bar asks for it on a clock, and one string is not a
// reason to copy a session's worth of desk positions.
func (j *Journal) Mode() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.Mode
}

// Borrowed is which desk's declared mode is in force, and the mode it
// displaced. Its own method for the reason Mode is: it is read on every desk
// switch, beside the mode, and one pair of strings is not a reason to copy a
// session's worth of desk positions.
func (j *Journal) Borrowed() Borrowed {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.Borrowed
}

// SetBorrowed records that a desk's declared mode is in force, or with the zero
// value that none is. Both directions are written: "nobody has it" is a state
// somebody arrived at, and leaving it to be inferred from silence would make a
// mode chosen by hand indistinguishable from one a desk is still holding.
func (j *Journal) SetBorrowed(b Borrowed) error {
	return j.record(entry{Kind: kindBorrowed, Desk: b.Desk, Mode: b.Mode})
}

// Skipped is how many lines the replay could not use: a torn tail, or entries
// from a version that knows kinds this one does not. doctor reports it.
func (j *Journal) Skipped() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.skipped
}

// SetActive records the workspace focused on one monitor of a desk, which is
// what a desk switch restores (invariant 5).
func (j *Journal) SetActive(n desk.Name) error {
	return j.record(entry{Kind: kindActive, Desk: n.Desk, Monitor: n.Monitor, Slot: n.Slot})
}

// SetLastDesk records the desk that was active, for desk.last.
func (j *Journal) SetLastDesk(name string) error {
	return j.record(entry{Kind: kindLastDesk, Desk: name})
}

// SetOnDesk records the desk you are on, which is what says who owns a
// workspace that has no name yet.
func (j *Journal) SetOnDesk(name string) error {
	return j.record(entry{Kind: kindOnDesk, Desk: name})
}

// Queue records something waiting and answers with it as recorded. The caller
// fills in everything but the id, which is the journal's to give: it needs the
// id back to be able to finish the thing later.
//
// Ids come from the journal and never repeat within its life, including across
// a restart: the replay carries the highest one it saw. A queue whose ids came
// from the length of itself would hand the same number to two things as soon as
// one was finished.
func (j *Journal) Queue(it Item) (Item, error) {
	// One lock over reading the last id and writing the entry that claims the
	// next one. Two calls that read it before either wrote would both take the
	// same number, and the second thing to wait would finish the first.
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.roomFor(it.From); err != nil {
		return Item{}, err
	}
	it.ID = j.lastID + 1
	if err := j.recordLocked(entry{
		Kind: kindQueued, ID: it.ID, Text: it.Text, Desk: it.Desk, From: it.From, Urgent: it.Urgent,
	}); err != nil {
		return Item{}, err
	}
	return it, nil
}

// roomFor is the three bounds, asked in the order of what they mean. Called
// with the lock held.
//
// The newest is what gives way at every one of them, and never the oldest.
// Something has to give at a cap and the two directions are not equally honest.
// The queue's promise is that what interrupted you is worth as much tomorrow
// morning as it was last night (see State.Queue), so dropping the oldest to make
// room would mean one chatty program could quietly erase every real thing you
// owed - which is the shape of failure principle 3 is about. Refusing the newest
// keeps every promise about what is already there, and it makes the flood the
// thing that is refused rather than the thing that wins.
//
// There is no sweep and no age at which an item goes, and that is decided rather
// than missing. A queue that deleted what you owed because it got old would be a
// list you cannot trust, which is a list nobody reads; and it would be a second
// way for a flood to cost somebody a real item, since the thing quietly removed
// is whatever has been waiting longest and that is the thing that mattered most.
// What empties a queue is a person deciding to (see Clear).
//
// A walk over the queue per call, up to QueueMax comparisons of a string. It is
// on the arrival path and it is not worth avoiding: the same critical section
// then fsyncs a line to the disk, which is four or five orders of magnitude more
// than this, and a counter kept beside the slice would be a second copy of the
// queue's shape to disagree with it across replay, compaction and Done.
func (j *Journal) roomFor(from string) error {
	if len(j.state.Queue) >= QueueMax {
		return ErrQueueFull
	}
	// Nothing on the bus can be either of these names, so neither is part of
	// what the shares below are protecting anybody from (see Reserved).
	if ours(from) {
		return nil
	}
	held, mine := 0, 0
	for _, it := range j.state.Queue {
		if ours(it.From) {
			continue
		}
		held++
		if it.From == from {
			mine++
		}
	}
	if mine >= PerSenderMax {
		return fmt.Errorf("%s already has %d things waiting, which is one sender's whole share of the queue: %w",
			from, PerSenderMax, ErrSenderFull)
	}
	if held >= QueueMax-Reserved {
		return fmt.Errorf("%d of the queue's %d places are taken by things with a sender, "+
			"which is as many as they hold between them: %w", held, QueueMax, ErrSenderFull)
	}
	return nil
}

// ours is an entry nothing off the bus could have made: one a person typed, and
// the desktop's own.
//
// attn's constant rather than the same word spelled twice here, which is the
// argument internal/zded/launch.go makes about the same string: the name is
// reserved on the way in from the bus, and a reservation guarded under one
// spelling while the sender used another is a defence with nothing behind it.
func ours(from string) bool { return from == "" || from == attn.SelfFrom }

// ClaimID hands out an id without queueing anything, and records that it is
// spent.
//
// A notification a mode kept out of the queue still needs a number: the app
// that sent it addresses it by one on the bus, and the notification center
// dismisses it by one. Taking that number from the same counter the queue uses
// is what keeps the two from colliding - a reused id would let an app close a
// reminder somebody typed, which is the exact thing internal/attn scopes its
// replaces to sender to prevent.
func (j *Journal) ClaimID() (uint64, error) {
	// One lock over reading and claiming, for the reason Queue has: two
	// arrivals at once would otherwise take the same number.
	j.mu.Lock()
	defer j.mu.Unlock()
	id := j.lastID + 1
	if err := j.recordLocked(entry{Kind: kindLastID, ID: id}); err != nil {
		return 0, err
	}
	return id, nil
}

// SetMode records what arrivals are allowed to do. Written down rather than
// held in the daemon, so a zded restart comes back in the mode you left it in.
func (j *Journal) SetMode(mode string) error {
	return j.record(entry{Kind: kindMode, Mode: mode})
}

// Done takes something off the queue. Unknown ids are not an error: the thing
// is not waiting any more either way, which is what was asked for.
func (j *Journal) Done(id uint64) error {
	return j.record(entry{Kind: kindDone, ID: id})
}

// Clear takes everything off the queue and answers with how many went.
//
// It exists because the way out of a full queue was a thousand calls. The bounds
// above make a flood cost one sender's share instead of the whole list, but they
// are refusals and refusals do not shorten anything: a queue that filled up
// before this zde, or one a person let grow, still has to be emptied, and
// `zde queue done <id>` a thousand times is not a way out - it is the reason
// nobody would take it.
//
// One entry and one fsync, not one per item. That is the point of it and it is
// also what makes it honest across a restart: a thousand `done` lines can be
// torn in the middle and replay to half a cleared queue, where one line either
// landed or did not.
//
// Everything, including what a person typed themselves. A clear that kept some
// of it would be zde deciding which of somebody's own obligations were the real
// ones, and there is nothing here that could decide that. This is a verb a
// person types, and the count comes back so that what it cost is said rather
// than assumed.
func (j *Journal) Clear() (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := len(j.state.Queue)
	if n == 0 {
		// Nothing written for nothing done: this is reachable from a key, and a
		// line and an fsync per press of it is a file growing for no reason.
		return 0, nil
	}
	if err := j.recordLocked(entry{Kind: kindCleared}); err != nil {
		return 0, err
	}
	return n, nil
}

// Renamed tells the journal a workspace changed name, so any position
// remembered for it moves too.
func (j *Journal) Renamed(r desk.Rename) error {
	return j.record(entry{Kind: kindRenamed, Desk: r.From.String(), To: r.To.String()})
}

func (j *Journal) record(e entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.recordLocked(e)
}

func (j *Journal) recordLocked(e entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		return err
	}
	// Fsync per entry: these arrive at the speed a person switches desks, so
	// the cost is nothing and the alternative is losing your place to a crash
	// that the rest of the system survives by design.
	if err := j.file.Sync(); err != nil {
		return err
	}
	j.entries++
	j.apply(e)
	return nil
}

// Compact rewrites the journal as the shortest sequence of entries that
// replays to the current state.
func (j *Journal) Compact() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.compactLocked()
}

func (j *Journal) compactLocked() error {
	tmp, err := os.CreateTemp(filepath.Dir(j.path), ".journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	w := bufio.NewWriter(tmp)
	n := 0
	write := func(e entry) error {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return err
		}
		n++
		return nil
	}
	for _, d := range sortedKeys(j.state.LastActive) {
		for _, mon := range sortedKeys(j.state.LastActive[d]) {
			if err := write(entry{Kind: kindActive, Desk: d, Monitor: mon, Slot: j.state.LastActive[d][mon]}); err != nil {
				return err
			}
		}
	}
	if j.state.LastDesk != "" {
		if err := write(entry{Kind: kindLastDesk, Desk: j.state.LastDesk}); err != nil {
			return err
		}
	}
	if j.state.OnDesk != "" {
		if err := write(entry{Kind: kindOnDesk, Desk: j.state.OnDesk}); err != nil {
			return err
		}
	}
	// The mode, or a compaction would silently put the session back in the
	// default - which is the one failure a persisted mode exists to prevent,
	// arriving at whatever moment the journal happened to get long enough.
	if j.state.Mode != "" {
		if err := write(entry{Kind: kindMode, Mode: j.state.Mode}); err != nil {
			return err
		}
	}
	// And who lent it, or the mode above would come back with nothing to give
	// it back to: the session would keep a desk's mode after walking off that
	// desk, because a compaction happened to fall between the two.
	if j.state.Borrowed.Desk != "" {
		if err := write(entry{
			Kind: kindBorrowed, Desk: j.state.Borrowed.Desk, Mode: j.state.Borrowed.Mode,
		}); err != nil {
			return err
		}
	}
	// In order, because the order is the queue: what has waited longest is
	// what queue-jump goes to.
	for _, it := range j.state.Queue {
		if err := write(entry{
			Kind: kindQueued, ID: it.ID, Text: it.Text, Desk: it.Desk, From: it.From, Urgent: it.Urgent,
		}); err != nil {
			return err
		}
	}
	// And the counter, because compaction drops the entries the ids were
	// learned from. Without this the highest id in the file is whatever is
	// still waiting - or nothing at all - and the next reminder takes a number
	// somebody already wrote down next to a different one.
	if j.lastID != 0 {
		if err := write(entry{Kind: kindLastID, ID: j.lastID}); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	// os.CreateTemp already makes it 0600, and the rename carries the mode over
	// with it. Said out loud anyway, because the mode is a promise this file
	// makes (see journalMode) and a reader checking it should not have to know
	// what os.CreateTemp defaults to.
	//
	// Through tighten, and before the close, for the reasons Open has: on the
	// descriptor rather than the name, and a filesystem that cannot hold a mode
	// says so once instead of failing a compaction. This temp file is zde's own
	// by construction, so tighten's other branch cannot be reached from here.
	if err := tighten(tmp, j.path); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Rename over the live file, then reopen: a crash mid-compaction leaves
	// the old journal whole, never a half-written one.
	if err := os.Rename(tmp.Name(), j.path); err != nil {
		return err
	}
	if j.file != nil {
		j.file.Close()
	}
	// O_NOFOLLOW here too, for the reason Open has it. Nothing legitimate can
	// have put a symlink at the name in the moment since the rename, but a
	// second way to open the journal that follows one is a second way in, and
	// an asymmetry a reader would have to work out is not worth the word it
	// saves.
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, journalMode)
	if err != nil {
		return err
	}
	j.file = f
	j.entries = n
	// The bodies an older journal held are gone with the file they were in, so
	// a second compaction is not owed for them.
	j.bodies = 0
	return nil
}

// Close flushes and closes the journal.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}

// sortedKeys keeps a compacted journal in a fixed order, so that two runs that
// remember the same thing produce the same file.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
