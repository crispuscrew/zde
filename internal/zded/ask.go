package zded

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crispuscrew/zde/internal/apps"
)

// Tier names are configured commands; no provider, credentials or cloud is defaulted.
const (
	TierProvider = "provider"
	TierLocal    = "local"
	TierEscalate = "escalate"
)

const askFile = "ask.json"

const MethodAskRun = "ask.run"

// askTimeout bounds the whole run, including a tier that goes silent.
const askTimeout = 2 * time.Minute

// asksMax bounds subprocesses across all connections, not only per socket.
const asksMax = 4

// askGrace bounds Wait after the process group has been killed.
const askGrace = 5 * time.Second

// askDrain allows buffered output after exit without waiting for descendants holding pipes.
const askDrain = 200 * time.Millisecond

// askSendWait allows client layout while streaming; broadcast latency is a different bound.
const askSendWait = 5 * time.Second

const complaintMax = 64 << 10

// askMax bounds the answer retained by the shell; askContextMax bounds the tier's input.
const askMax = 256 << 10

const askContextMax = 64 << 10

// askSurface broadcasts an optional opening question; answers go only to their requester.
func (s *Server) askSurface(kind, question string) Response {
	_, output, err := s.niri.FocusedPlace()
	if err != nil {
		output = ""
	}
	return ok(s.showSurface(Event{Kind: kind, Output: output, Question: question}))
}

func askTier(tier string) ([]string, error) {
	path := apps.Path(askFile)
	all, err := apps.Load(path)
	if err != nil {
		return nil, err
	}
	if argv := all[tier]; len(argv) > 0 {
		return argv, nil
	}
	msg := fmt.Sprintf("no %s tier to ask: set zde.ask.tiers.%s in your home-manager config, which is what writes %s",
		tier, tier, path)
	if names := all.Names(); len(names) > 0 {
		msg += " (this machine has " + strings.Join(names, ", ") + ")"
	}
	return nil, errors.New(msg)
}
