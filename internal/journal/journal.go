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
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/crispuscrew/zde/internal/desk"
)

// compactAt is when a replay is long enough to be worth rewriting. Desk
// switches are human-paced, so this is days of use, not minutes.
const compactAt = 1000

// entry is one line of the journal.
type entry struct {
	Kind    string `json:"kind"`
	Desk    string `json:"desk,omitempty"`
	Monitor string `json:"monitor,omitempty"`
	Slot    string `json:"slot,omitempty"`
	To      string `json:"to,omitempty"`
	ID      uint64 `json:"id,omitempty"`
	Text    string `json:"text,omitempty"`
	Body    string `json:"body,omitempty"`
	From    string `json:"from,omitempty"`
	Urgent  bool   `json:"urgent,omitempty"`
}

const (
	kindActive   = "active"   // on this desk and monitor, this slot was focused
	kindLastDesk = "lastdesk" // the desk to go back to
	kindOnDesk   = "ondesk"   // the desk you are on now
	kindRenamed  = "renamed"  // a workspace was renamed, so entries move with it
	kindQueued   = "queued"   // something is waiting, and which desk it waits on
	kindDone     = "done"     // it is not waiting any more
	kindLastID   = "lastid"   // the highest queue id handed out, so none repeats
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
}

// Item is one thing waiting. The desk is where it belongs, which is what
// separates a queue from a list: attn can show a desk only its own, and
// queue-jump has somewhere to go.
type Item struct {
	ID   uint64 `json:"id"`
	Text string `json:"text"`
	Desk string `json:"desk,omitempty"`
	// From is what sent it, as it described itself. Empty when a person typed
	// it. Nothing verifies it - see internal/attn.
	From string `json:"from,omitempty"`
	// Body is the rest of what was sent, kept but not shown: a notification is
	// meant to land in history with its full text (docs/vision.md, principle
	// 3), and the notification center that will show it does not exist yet.
	Body string `json:"body,omitempty"`
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	j := &Journal{path: path, state: newState()}
	if err := j.replay(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	j.file = f
	if j.entries > compactAt {
		if err := j.compactLocked(); err != nil {
			j.file.Close()
			return nil, err
		}
	}
	return j, nil
}

func (j *Journal) replay() error {
	f, err := os.Open(j.path)
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
		j.state.Queue = append(j.state.Queue, Item{ID: e.ID, Text: e.Text, Body: e.Body, Desk: e.Desk, From: e.From, Urgent: e.Urgent})
		if e.ID > j.lastID {
			j.lastID = e.ID
		}
	case kindLastID:
		if e.ID > j.lastID {
			j.lastID = e.ID
		}
	case kindDone:
		for i, it := range j.state.Queue {
			if it.ID == e.ID {
				j.state.Queue = append(j.state.Queue[:i], j.state.Queue[i+1:]...)
				break
			}
		}
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
	it.ID = j.lastID + 1
	if err := j.recordLocked(entry{
		Kind: kindQueued, ID: it.ID, Text: it.Text, Body: it.Body, Desk: it.Desk, From: it.From, Urgent: it.Urgent,
	}); err != nil {
		return Item{}, err
	}
	return it, nil
}

// Done takes something off the queue. Unknown ids are not an error: the thing
// is not waiting any more either way, which is what was asked for.
func (j *Journal) Done(id uint64) error {
	return j.record(entry{Kind: kindDone, ID: id})
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
	// In order, because the order is the queue: what has waited longest is
	// what queue-jump goes to.
	for _, it := range j.state.Queue {
		if err := write(entry{
			Kind: kindQueued, ID: it.ID, Text: it.Text, Body: it.Body, Desk: it.Desk, From: it.From, Urgent: it.Urgent,
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
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
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
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	j.file = f
	j.entries = n
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
