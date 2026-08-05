package zded

import (
	"fmt"
	"log"
	"strings"

	"github.com/crispuscrew/zde/internal/attn"
)

// What a desk switch does about an app that did not start.
//
// Entering a desk starts what its manifest declares (docs/model.md, section 5),
// behind the switch rather than in front of it - so the person is already
// looking at the desk while the launches happen, and until now a launch that
// failed was a line in the daemon's log. Nobody reads a daemon's log while they
// are working, so somebody who switched to a desk and got three of its four
// windows had no way to find out which one was missing or why, short of
// suspecting it in the first place.
//
// zded is the session's notification server (internal/attn), so the way to
// reach that person is the one every other app in the session already uses.
// Posting to its own sink rather than over the bus, because dialling out to the
// name it holds itself would be a round trip to reach a struct field.

// launchFailure is one app a desk declares that did not start, and the runner's
// own words about it. An app that was already running is not one of these: zinc
// refuses a second launch, and the desk switch that met that refusal got what
// it wanted (server.go, startApps).
type launchFailure struct {
	Address string
	Err     error
}

// launchesNamed is how many failures the notification spells out one by one.
//
// The bound is the whole point of this being one notification: a desk that
// declares eight apps and can run none of them - which is every desk on a
// machine with no layer 2 (docs/delivery.md) - must not cost eight arrivals, and
// a body listing all eight is a wall nobody reads either. Four is what fits the
// glance the summary is for; the rest are counted, and the log has them all.
const launchesNamed = 4

// launchFrom is what the notification says sent it. The sender column is
// otherwise an app's own claim about itself (internal/attn, Notification.From),
// and this is the one arrival where it is the desktop talking.
const launchFrom = "zde"

// reasonMax is as much of one failure as belongs on one line of the body.
//
// zcr hands back everything the runner printed (internal/zinc, Run), and an
// image that would not build is a screenful of it. Bounded per failure and not
// only in total, because the total bound alone would let the first complaint eat
// the space the other three needed to be named at all.
const reasonMax = 200

// launchesFailed says, once for the whole switch, what the desk could not start.
//
// Once and not once per app: the count is the thing a person needs first, and
// the names are underneath it.
//
// Never urgent, and the mode is allowed to keep it out of the queue. Urgency is
// the claim focus mode reads (internal/attn, Mode), and a desk that came up
// missing a window is not worth crossing a focus mode for - nor would urgency
// buy anything in a quiet session, which queues nothing, the urgent included.
// The reason it does not queue itself regardless of the mode is the machine
// this is for: a desk can put somebody in quiet without their pressing anything
// (docs/roadmap.md, desk attn policies), and on a machine with no layer 2 every
// desk fails to start every app it declares (docs/delivery.md) - so a launch
// failure that ignored the mode would be the one notification quiet cannot
// silence, exactly where it fires most. What no mode does is lose it: the
// record lands in the history either way, marked as one the mode kept off the
// queue, which is principle 3 - display policy, never data policy.
//
// Nothing here can fail the switch: it runs on the goroutine behind it, holds no
// lock the switch waits on, and an arrival that cannot be kept is logged like
// the launch failures themselves.
func (s *Server) launchesFailed(target string, failed []launchFailure) {
	if len(failed) == 0 {
		return
	}
	if _, err := s.Arrived(attn.Local(launchFrom, launchSummary(target, failed), launchBody(failed))); err != nil {
		log.Printf("zded: %d of %s's apps did not start, and nor did the notification about it: %v",
			len(failed), target, err)
	}
}

// launchSummary is the line the queue and the centre's row draw. One failure
// names itself there, because with only one the count says nothing the address
// does not say better.
func launchSummary(target string, failed []launchFailure) string {
	if len(failed) == 1 {
		return fmt.Sprintf("desk %s: %s did not start", target, failed[0].Address)
	}
	return fmt.Sprintf("desk %s: %d apps did not start", target, len(failed))
}

// launchBody is what the notification centre shows under the row: which app,
// and what the thing that would have run it said about it.
func launchBody(failed []launchFailure) string {
	var b strings.Builder
	for i, f := range failed {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i == launchesNamed {
			fmt.Fprintf(&b, "and %d more, in journalctl --user -u zded", len(failed)-i)
			break
		}
		b.WriteString(f.Address + ": " + reason(f.Err))
	}
	return b.String()
}

// reason is one failure, cut to a line: the first line of it, since that is
// where a program puts what went wrong, and bounded in runes because a message
// in an alphabet that costs four bytes a character is not four times as long.
func reason(err error) string {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = strings.TrimSpace(msg[:i]) + " ..."
	}
	if r := []rune(msg); len(r) > reasonMax {
		msg = strings.TrimSpace(string(r[:reasonMax])) + " ..."
	}
	return msg
}
