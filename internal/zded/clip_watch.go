package zded

import (
	"context"
	"log"
	"time"
)

// sweepEvery bounds how long an unviewed entry can outlive its TTL.
const sweepEvery = 30 * time.Second

// WatchClipboard restarts failed watches; expiry runs independently and is joined
// on exit so a failed watch cannot retain clipboard history indefinitely.
func (s *Server) WatchClipboard(ctx context.Context) {
	tool := s.clipTool()
	if tool == nil {
		s.watchingClipboard("this zded was started without a clipboard to watch")
		return
	}
	swept := make(chan struct{})
	go func() {
		defer close(swept)
		s.sweepClips(ctx)
	}()
	defer func() { <-swept }()

	said := false
	for ctx.Err() == nil {
		changes, err := tool.Watch(ctx)
		if err != nil {
			s.watchingClipboard(err.Error())
			if !said {
				log.Printf("zded: clipboard history: %v", err)
				said = true
			}
			if !sleep(ctx, retry) {
				return
			}
			continue
		}
		said = false
		s.watchingClipboard("")
		s.drainClipboard(ctx, changes)
		if !sleep(ctx, retry) {
			return
		}
	}
}

// drainClipboard coalesces MIME-type rings before spawning selection reads.
func (s *Server) drainClipboard(ctx context.Context, changes <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-changes:
			if !open {
				return
			}
		}
		for settling := true; settling; {
			select {
			case <-ctx.Done():
				return
			case _, open := <-changes:
				if !open {
					return
				}
			case <-time.After(settle):
				settling = false
			}
		}
		s.take()
	}
}

func (s *Server) sweepClips(ctx context.Context) {
	for {
		if !sleep(ctx, sweepEvery) {
			return
		}
		s.clips.Expire(time.Now())
	}
}
