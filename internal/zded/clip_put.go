package zded

import (
	"strconv"
	"time"

	"github.com/crispuscrew/zde/internal/clip"
)

// clipPut registers the expected echo before writing and retracts it on failure.
// The temporary text copy is wiped on every return, independently of ring expiry.
func (s *Server) clipPut(id string) Response {
	tool := s.clipTool()
	if tool == nil {
		return Response{Error: "nothing on this session can write to the clipboard: " +
			clip.Copy + " is what zde puts an entry back with"}
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return Response{Error: "clip.history wants the id from the list, not " + strconv.Quote(id)}
	}
	now := time.Now()
	text, why := s.clips.Text(n, now)
	if why != "" {
		return Response{Error: why}
	}
	defer clear(text)
	s.clips.Expect(n, now)
	if err := tool.Write(text); err != nil {
		s.clips.Expect(0, now)
		return Response{Error: err.Error()}
	}
	return ok([]string{})
}

// clipPutOn claims on the read loop before spawning, bounding work and goroutines.
// The result may follow later replies; the request's ID is captured by value.
func (s *Server) clipPutOn(k *sink, id, requestID string) {
	if !k.putting.CompareAndSwap(false, true) {
		k.replyTo(requestID, Response{Error: "this connection is still putting the last entry back on the clipboard: " +
			"wait for it, or ask on another"})
		return
	}
	go func() {
		defer k.putting.Store(false)
		k.replyTo(requestID, s.clipPut(id))
	}()
}

func (s *Server) clipClear() Response {
	return ok(s.clips.Clear())
}
