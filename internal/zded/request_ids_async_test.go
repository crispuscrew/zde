package zded

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestClipboardAndShownRepliesKeepIDsOutOfOrder(t *testing.T) {
	for _, requestID := range []string{"", "clipboard"} {
		t.Run("id="+requestID, func(t *testing.T) {
			server, clipboard := clipServer(t)
			clipboard.offer("entry", "text/plain")
			server.take()
			entry := itoa(clipRows(t, server)[0].ID)
			blocked := make(chan struct{})
			clipboard.writeBlocks = blocked
			release := sync.OnceFunc(func() { close(blocked) })
			defer release()
			conn, decoder := requestConnection(t, server)
			sendRequest(t, conn, Request{Method: MethodClip, Args: []string{entry}, ID: requestID})
			select {
			case <-clipboard.writeStarts:
			case <-time.After(5 * time.Second):
				t.Fatal("clipboard write never started")
			}
			// A second put is refused on the read loop while the first is still pending.
			sendRequest(t, conn, Request{Method: MethodClip, Args: []string{entry}, ID: "second-put"})
			readIDReply(t, decoder, "second-put", true)
			token := server.nextToken()
			acked := server.await(token)
			defer server.stopAwaiting(token)
			sendRequest(t, conn, Request{Method: MethodShown, Args: []string{token}, ID: "shown"})
			readIDReply(t, decoder, "shown", false)
			select {
			case <-acked:
			default:
				t.Fatal("shown was replied to without resolving its token")
			}
			release()
			readIDReply(t, decoder, requestID, false)
		})
	}
}

func TestAskIDsDoNotChangeStreamEvents(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("stream")})
	for _, requestID := range []string{"", "ask"} {
		t.Run("id="+requestID, func(t *testing.T) {
			server := New("test", nil, nil, nil)
			conn, decoder := requestConnection(t, server)
			sendRequest(t, conn, Request{Method: MethodAskRun, Args: []string{TierProvider, "question"}, ID: requestID})
			response := readIDReply(t, decoder, requestID, false)
			if string(response.Ok) != `"asking"` {
				t.Fatalf("ask acknowledgement = %s", response.Ok)
			}
			sendRequest(t, conn, Request{Method: MethodShown, Args: []string{"late"}, ID: "shown"})
			text, shown, done := "", false, false
			for !shown || !done {
				var fields map[string]json.RawMessage
				if err := decoder.Decode(&fields); err != nil {
					t.Fatal(err)
				}
				if raw, event := fields["event"]; event {
					var chunk Event
					var payload map[string]json.RawMessage
					if err := json.Unmarshal(raw, &chunk); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(raw, &payload); err != nil {
						t.Fatal(err)
					}
					if len(fields) != 1 || payload["id"] != nil || chunk.Kind != EventAskText || chunk.Error != "" {
						t.Fatalf("ask stream changed: %s", raw)
					}
					text += chunk.Text
					done = chunk.Done
				} else {
					if shown || string(fields["id"]) != `"shown"` || string(fields["ok"]) != `"thanks"` {
						t.Fatalf("interleaved reply = %v", fields)
					}
					shown = true
				}
			}
			if text != "onetwo" {
				t.Fatalf("stream = %q", text)
			}
		})
	}
}
