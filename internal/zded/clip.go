package zded

import (
	"context"
	"time"

	"github.com/crispuscrew/zde/internal/clip"
)

// Clipboard accesses the session selection. Watch must return promptly and put
// waiting on its cancellable channel; Types must not read selection content.
type Clipboard interface {
	Watch(ctx context.Context) (<-chan struct{}, error)
	Types() ([]string, error)
	Read(mime string, limit int) (data []byte, more bool, err error)
	Write(text []byte) error
}

func (s *Server) UseClipboard(c Clipboard) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clipboard = c
}

// clipTool copies under mu; clipboard subprocess calls run outside the lock.
func (s *Server) clipTool() Clipboard {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clipboard
}

func (s *Server) watchingClipboard(why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clipWhy = why
}

func (s *Server) clipboardTrouble() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clipWhy
}

const MethodClip = "clip.history"

// Clips carries preview rows and explains an unavailable watcher separately from emptiness.
type Clips struct {
	Shown   bool       `json:"shown"`
	Entries []clip.Row `json:"entries"`
	Why     string     `json:"why,omitempty"`
}

func (s *Server) clipHistory() Response {
	rows := s.clips.Rows(time.Now())
	output := ""
	if s.niri != nil {
		if _, on, err := s.niri.FocusedPlace(); err == nil {
			output = on
		}
	}
	why := s.clipboardTrouble()
	shown := s.showSurface(Event{
		Kind:   EventClip,
		Clips:  rows,
		Output: output,
	})
	return ok(Clips{Shown: shown, Entries: rows, Why: why})
}
