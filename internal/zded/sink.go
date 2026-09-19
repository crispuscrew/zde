package zded

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// sink serializes whole lines with a deadline-aware gate, initialized on first use.
// asking is set on the read loop only after a daemon claim: streams have no IDs
// and admission exempts only claimed runs. putting similarly bounds clipboard work.
// pid is immutable after admission; asked records parsed requests, never raw bytes.
type sink struct {
	made    sync.Once
	gate    chan struct{}
	gone    chan struct{}
	ends    sync.Once
	w       io.Writer
	asking  atomic.Bool
	putting atomic.Bool
	pid     int32
	asked   atomic.Int64
}

func (k *sink) touch() { k.asked.Store(time.Now().UnixNano()) }

func (k *sink) gateOf() chan struct{} {
	k.channels()
	return k.gate
}

func (k *sink) endedOf() chan struct{} {
	k.channels()
	return k.gone
}

func (k *sink) channels() {
	k.made.Do(func() {
		k.gate = make(chan struct{}, 1)
		k.gone = make(chan struct{})
	})
}

func (k *sink) end() {
	gone := k.endedOf()
	k.ends.Do(func() { close(gone) })
}

func (k *sink) lockBefore(deadline time.Time) bool {
	gate := k.gateOf()
	select {
	case gate <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case gate <- struct{}{}:
		return true
	case <-t.C:
		return false
	}
}

func (k *sink) unlock() { <-k.gateOf() }
