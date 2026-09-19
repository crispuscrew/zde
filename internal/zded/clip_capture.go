package zded

import (
	"fmt"
	"time"

	"github.com/crispuscrew/zde/internal/clip"
)

// take checks sensitive MIME hints before reading bytes into the daemon. wl-paste
// may already have received the selection into its own pipe. Oversized data is
// wiped rather than retained as a truncated entry that would lose data on paste.
func (s *Server) take() {
	tool := s.clipTool()
	if tool == nil {
		return
	}
	types, err := tool.Types()
	if err != nil || len(types) == 0 {
		return
	}
	if clip.Sensitive(types) {
		return
	}
	now := time.Now()
	mime, ok := clip.TextType(types)
	if !ok {
		s.clips.Note(clip.KindOther, clip.Offered(types)+" is not text, and the history keeps text only", 0, now)
		return
	}
	data, more, err := tool.Read(mime, clip.TextMax)
	if err != nil {
		return
	}
	if more {
		clear(data)
		s.clips.Note(clip.KindBig,
			fmt.Sprintf("more than %d KiB of %s, and an entry is kept whole or not at all",
				clip.TextMax>>10, clip.Offered([]string{mime})),
			clip.TextMax, now)
		return
	}
	s.clips.Add(data, now)
}
