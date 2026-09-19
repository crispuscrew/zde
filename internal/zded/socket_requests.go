package zded

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// requestMax bounds scanner memory, with room for JSON escaping of askContextMax.
const requestMax = 1 << 20

// handle validates and claims work on the read loop so refusals retain back-pressure.
// Only claimed ask and clipboard work leaves this loop; IDs travel with each request.
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	k := &sink{w: conn}
	pid, err := allowPeer(conn)
	if err != nil {
		k.reply(Response{Error: "not permitted"})
		return
	}
	k.pid = pid
	defer s.unlisten(k)
	defer k.end()
	defer s.forget(k)
	if out, held := s.admit(k); out != nil {
		reason := fmt.Sprintf(
			"zded closed this connection to make room: it was holding %d, which is every one it keeps, and of the %d open from this process this was the one that had gone longest without asking anything. zded is running - dial again",
			ConnectionsMax, held)
		if out == k {
			// Finish the explanation before this handler's deferred close.
			out.drop(reason)
			return
		}
		go out.drop(reason)
	}

	r := bufio.NewScanner(conn)
	r.Buffer(make([]byte, 0, 4<<10), requestMax)
	for r.Scan() {
		var req Request
		if err := json.Unmarshal(r.Bytes(), &req); err != nil {
			k.reply(Response{Error: "malformed request"})
			continue
		}
		k.touch()
		if req.Method == MethodEvents {
			if len(req.Args) != 0 {
				k.replyTo(req.ID, Response{Error: MethodEvents + " takes no arguments"})
				continue
			}
			if !s.listen(k) {
				k.replyTo(req.ID, Response{Error: fmt.Sprintf(
					"zded is already listening for %d connections, which is every one it keeps: close one before opening another",
					listenersMax)})
				continue
			}
			k.replyTo(req.ID, ok("listening"))
			continue
		}
		if req.Method == MethodAskRun {
			s.askOn(k, req.Args, req.ID)
			continue
		}
		if req.Method == MethodClip && len(req.Args) == 1 {
			s.clipPutOn(k, req.Args[0], req.ID)
			continue
		}
		k.reply(s.Dispatch(req))
	}
	// An oversized frame ends the connection; its unread tail is not a new request.
	if errors.Is(r.Err(), bufio.ErrTooLong) {
		k.reply(Response{Error: fmt.Sprintf(
			"that request is longer than %d KiB, which is more than any zde request carries: the largest is an ask.run at %d KiB of conversation",
			requestMax>>10, askContextMax>>10)})
	}
}

// allowPeer trusts kernel credentials, never identity supplied in a request.
func allowPeer(conn net.Conn) (int32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	if uint32(os.Getuid()) != cred.Uid {
		return 0, fmt.Errorf("uid %d is not %d", cred.Uid, os.Getuid())
	}
	return cred.Pid, nil
}
