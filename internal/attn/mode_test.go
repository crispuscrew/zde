package attn

import "testing"

// Work is the mode nobody has to choose, and the one a journal that has never
// been told replays into. A default that came out as focus or quiet would be a
// desktop that silently swallows notifications on a fresh install.
func TestWorkQueuesEverything(t *testing.T) {
	m, err := ParseMode("")
	if err != nil {
		t.Fatalf("the empty mode is not the default: %v", err)
	}
	if m != Work {
		t.Errorf("the empty mode is %q, want work", m)
	}
	for _, urgent := range []bool{false, true} {
		if !m.Queues(urgent) {
			t.Errorf("work kept an arrival (urgent %v) off the queue", urgent)
		}
	}
}

// Focus is the mode that has to let the emergency through: it exists so that
// somebody can stop being interrupted without having to watch for the one thing
// that should interrupt. A focus that queued everything would be work under
// another name, and one that queued nothing would be quiet.
func TestFocusQueuesOnlyTheUrgent(t *testing.T) {
	if !Focus.Queues(true) {
		t.Error("focus kept an urgent arrival off the queue")
	}
	if Focus.Queues(false) {
		t.Error("focus let an ordinary arrival onto the queue")
	}
}

// Quiet is the urgent ones too. It is the mode for a screencast and a night's
// sleep, and a quiet that let critical notifications through would be a quiet
// nobody can trust with a screen share.
func TestQuietQueuesNothing(t *testing.T) {
	for _, urgent := range []bool{false, true} {
		if Quiet.Queues(urgent) {
			t.Errorf("quiet let an arrival (urgent %v) onto the queue", urgent)
		}
	}
}

// A name that is not a mode is refused where it is set, so that a typo cannot
// be written into the journal and read back for the rest of the session as
// something that queues nothing.
func TestUnknownModeIsRefused(t *testing.T) {
	for _, name := range []string{"silent", "dnd", "WORK", "focus "} {
		if _, err := ParseMode(name); err == nil {
			t.Errorf("%q was accepted as a mode", name)
		}
	}
	for _, name := range []string{"work", "focus", "quiet"} {
		if m, err := ParseMode(name); err != nil || string(m) != name {
			t.Errorf("ParseMode(%q) = %q, %v", name, m, err)
		}
	}
}

// Anything that is not a mode this package knows still queues. The way to get
// here is a journal from a newer zde, and being interrupted by something you
// would rather not have seen beats never hearing about the thing you were
// waiting for.
func TestAnUnknownModeStillQueues(t *testing.T) {
	if !Mode("holiday").Queues(false) {
		t.Error("a mode nothing has heard of swallowed an arrival")
	}
}
