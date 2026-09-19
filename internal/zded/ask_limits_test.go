package zded

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Reaching the output cap must stop the tier and tell the reader why the stream ended.
func TestAnEndlessAnswerIsCappedAndSaysSo(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("loop")})
	text, failure := askAll(t, askServer(t), TierProvider, "go on for ever")
	if len(text) > askMax+64<<10 {
		t.Errorf("the answer ran to %d bytes with a cap of %d", len(text), askMax)
	}
	if len(text) < askMax {
		t.Errorf("the answer stopped at %d bytes, short of the cap of %d", len(text), askMax)
	}
	if !strings.Contains(failure, "was stopped") {
		t.Errorf("the answer was capped and the client was told %q", failure)
	}
}

// ask.text has no request ID, so overlapping streams on one socket are ambiguous.
func TestASecondAskOnOneConnectionIsRefused(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("slow")})
	c, err := DialPath(askServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(MethodAskRun, nil, TierProvider, "the first question"); err != nil {
		t.Fatalf("the first ask: %v", err)
	}
	err = c.Call(MethodAskRun, nil, TierProvider, "the second question")
	if err == nil {
		t.Fatal("a second answer was started on a connection that was already carrying one")
	}
	if !strings.Contains(err.Error(), "still answering") {
		t.Errorf("the refusal is %q, and does not say why", err)
	}
}

// Separate sockets cannot bypass the daemon-wide subprocess cap.
func TestOnlySoManyTiersRunAtOnce(t *testing.T) {
	dir := t.TempDir()
	writeTiers(t, map[string][]string{TierLocal: fakeTier("linger", filepath.Join(dir, "tier"))})
	s := New("test", nil, &fakeCompositor{m: twoDesks(), focused: "vshop.DP-1.code"}, nil)
	path := serve(t, s)

	const tries = asksMax * 5
	asking, refused := 0, 0
	for i := 0; i < tries; i++ {
		c, err := DialPath(path)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if err := c.Call(MethodAskRun, nil, TierLocal, "hold on to a gpu"); err != nil {
			if !strings.Contains(err.Error(), "already running") {
				t.Fatalf("ask %d was refused with %v", i, err)
			}
			refused++
			continue
		}
		asking++
	}
	if asking != asksMax || refused != tries-asksMax {
		t.Errorf("%d tiers started and %d were refused, want %d and %d", asking, refused, asksMax, tries-asksMax)
	}
	if n := s.asking(); n != asksMax {
		t.Errorf("the daemon is running %d tiers at once, want the cap of %d", n, asksMax)
	}
}

// Pin an absolute client-side ceiling as well as the configurable askMax bound.
func TestALoopingTierCannotPushMoreThanAWindowHoldsIntoTheShell(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("loop")})
	text, failure := askAll(t, askServer(t), TierProvider, "go on for ever")
	if len(text) > 1<<20 {
		t.Errorf("a tier that never stopped got %d KiB of text into the client, past the 1 MiB "+
			"a window can hold in one text item on the thread that draws the bar",
			len(text)>>10)
	}
	if !strings.Contains(failure, "was stopped") {
		t.Errorf("the tier was not stopped, so %d KiB is what it chose to say and not what it "+
			"was allowed to: %q", len(text)>>10, failure)
	}
}

// Both write deadlines must match Client.Call's five-second patience.
func TestOnePieceOfAnAnswerWaitsAsLongAsAReplyDoes(t *testing.T) {
	if askSendWait != replyWait {
		t.Errorf("one piece of an answer waits %v and a reply waits %v: they are one number, "+
			"which is as long as anybody could still want the line", askSendWait, replyWait)
	}
	if askSendWait != 5*time.Second {
		t.Errorf("askSendWait is %v against the five seconds Call gives a whole round trip (client.go)", askSendWait)
	}
}
