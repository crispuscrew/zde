package zded

import (
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func requestConnection(t *testing.T, server *Server) (net.Conn, *json.Decoder) {
	t.Helper()
	conn, err := net.Dial("unix", serve(t, server))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn, json.NewDecoder(conn)
}

func sendRequest(t *testing.T, conn net.Conn, request Request) {
	t.Helper()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		t.Fatal(err)
	}
}

func readIDReply(t *testing.T, decoder *json.Decoder, requestID string, failed bool) Response {
	t.Helper()
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	_, hasID := fields["id"]
	if response.ID != requestID || hasID != (requestID != "") ||
		(response.Error != "") != failed || (!failed && len(response.Ok) == 0) {
		t.Fatalf("reply = %s; want ID %q, error %v", raw, requestID, failed)
	}
	return response
}

func TestParsedSocketRequestsEchoTheirOwnIDs(t *testing.T) {
	server := New("test", nil, nil, nil)
	conn, decoder := requestConnection(t, server)
	for _, test := range []struct {
		request Request
		failed  bool
	}{
		{Request{Method: MethodEvents, ID: "subscribe"}, false},
		{Request{Method: MethodEvents, ID: "bad-subscribe", Args: []string{"extra"}}, true},
		{Request{Method: MethodShown, ID: "shown", Args: []string{"late"}}, false},
		{Request{Method: MethodShown, ID: "bad-shown"}, true},
		{Request{Method: MethodAskRun, ID: "bad-ask"}, true},
		{Request{Method: MethodClip, ID: "bad-put", Args: []string{"404"}}, true},
		{Request{Method: "unknown", ID: "unknown"}, true},
		{Request{Method: MethodShown, Args: []string{"legacy"}}, false},
		{Request{Method: MethodEvents}, false},
	} {
		sendRequest(t, conn, test.request)
		readIDReply(t, decoder, test.request.ID, test.failed)
	}
	if _, err := io.WriteString(conn, "{\"id\":\"partial\",\"method\":7}\n"); err != nil {
		t.Fatal(err)
	}
	readIDReply(t, decoder, "", true)
}

func TestListenerLimitReplyEchoesID(t *testing.T) {
	server := New("test", nil, nil, nil)
	for range listenersMax {
		if !server.listen(&sink{w: io.Discard}) {
			t.Fatal("could not fill listener slots")
		}
	}
	conn, decoder := requestConnection(t, server)
	sendRequest(t, conn, Request{Method: MethodEvents, ID: "full"})
	readIDReply(t, decoder, "full", true)
}
