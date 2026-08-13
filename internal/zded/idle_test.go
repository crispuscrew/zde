package zded

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crispuscrew/zde/internal/power"
)

func idleOf(t *testing.T, s *Server) Idle {
	t.Helper()
	resp := s.Dispatch(Request{Method: "system.idle"})
	if resp.Error != "" {
		t.Fatalf("system.idle: %s", resp.Error)
	}
	var got Idle
	if err := json.Unmarshal(resp.Ok, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The distinction the whole widget rests on.
//
// A machine that cannot answer and a machine where nothing is holding the
// screen are the same empty strip, and only one of them is a fact. If these
// collapse into each other the bar goes quiet on a session that has lost its
// system bus and a person walks away from it believing the screen will lock.
func TestALogindThatCannotAnswerIsNotAnEmptyIdleTable(t *testing.T) {
	s, l, _ := powerServer(t)

	quiet := idleOf(t, s)
	if !quiet.Known || len(quiet.Holds) != 0 {
		t.Errorf("a logind holding nothing = %+v, want known with no holders", quiet)
	}

	// The same shape of answer, from a machine that never got asked.
	s2, _, _ := powerServer(t)
	withLogind(s2, nil, errors.New("no system bus"))
	blind := idleOf(t, s2)
	if blind.Known {
		t.Error("a logind that could not be reached reads as known")
	}
	if len(blind.Holds) != 0 {
		t.Errorf("an unreachable logind named holders: %+v", blind.Holds)
	}
	// And it says why, so the surface and the CLI give one reason rather than
	// each inventing their own.
	if !strings.Contains(blind.Why, "no system bus") {
		t.Errorf("why = %q, want logind's own words", blind.Why)
	}

	// A logind that is there and refuses the question is the same unknown, and
	// not an empty table either.
	l.stateErr = errors.New("connection closed")
	if broke := idleOf(t, s); broke.Known {
		t.Error("a logind that failed the call reads as known")
	}
}

// What the bar counts and what doctor prints, off the one reading.
func TestAnIdleInhibitorComesBackNamingWhatIsHoldingIt(t *testing.T) {
	s, l, _ := powerServer(t)
	l.state = power.State{Blocks: []power.Block{
		// The one that is not an idle hold, first, so a filter that returned
		// everything would be caught rather than merely being the right length.
		{What: "sleep", Who: "chromium", Why: "Playing audio"},
		{What: "idle", Who: "steam", Why: "Playing a game"},
	}}

	got := idleOf(t, s)
	if !got.Known {
		t.Fatal("a logind that answered reads as unknown")
	}
	if len(got.Holds) != 1 {
		t.Fatalf("holds = %+v, want only the idle one", got.Holds)
	}
	if got.Holds[0].Who != "steam" || got.Holds[0].Why != "Playing a game" {
		t.Errorf("holds[0] = %+v, want steam and its reason", got.Holds[0])
	}
}

// Who and Why are anybody's: every local account can run `systemd-inhibit
// --who=... --why=...` with whatever it likes in them. This lands in a Qt Text
// on the layer-shell bar and in a terminal for `zde doctor`, so it is the same
// kind of string as a notification's summary and takes the same filter.
//
// The escape is the one that matters: a why of "\033[2J" clears the terminal the
// report was printed in, and a newline splits one holder into two lines that
// look like two holders.
func TestAHoldersOwnWordsCannotDriveTheTerminalTheyArePrintedOn(t *testing.T) {
	s, l, _ := powerServer(t)
	l.state = power.State{Blocks: []power.Block{
		{What: "idle", Who: "steam\x1b[2J", Why: "line one\nline two"},
	}}

	got := idleOf(t, s)
	if len(got.Holds) != 1 {
		t.Fatalf("holds = %+v, want one", got.Holds)
	}
	if strings.ContainsAny(got.Holds[0].Who+got.Holds[0].Why, "\x1b\n\r") {
		t.Errorf("a holder's own words arrived unfiltered: %+v", got.Holds[0])
	}
}

// A holder whose name filters away to nothing is still a holder. The bar draws
// the count, so dropping the row would turn a held screen into a quiet strip -
// which is the one thing a name nobody chose must not be able to do.
func TestAHolderWithNoPrintableNameIsStillCounted(t *testing.T) {
	s, l, _ := powerServer(t)
	l.state = power.State{Blocks: []power.Block{{What: "idle", Who: "\x1b\x1b", Why: ""}}}

	got := idleOf(t, s)
	if len(got.Holds) != 1 {
		t.Fatalf("holds = %+v, want the holder kept", got.Holds)
	}
	if got.Holds[0].Who == "" {
		t.Error("a holder reached the bar with no name to draw")
	}
}

// There is nothing to do about somebody else's inhibitor from here, so the verb
// takes nothing. An argument is a caller that thinks this drops one.
func TestSystemIdleTakesNoArguments(t *testing.T) {
	s, _, _ := powerServer(t)
	if resp := s.Dispatch(Request{Method: "system.idle", Args: []string{"drop"}}); resp.Error == "" {
		t.Error("system.idle accepted an argument")
	}
}
