package zded

import (
	"encoding/json"
	"reflect"
	"testing"
)

type surfaceRecorder struct {
	server *Server
	ack    bool
	fail   bool
	event  Event
}

func (record *surfaceRecorder) Write(data []byte) (int, error) {
	var envelope struct{ Event Event }
	if err := json.Unmarshal(data, &envelope); err != nil {
		return 0, err
	}
	record.event = envelope.Event
	if record.fail {
		return 0, errClosed
	}
	if record.ack {
		record.server.acknowledge(envelope.Event.Token)
	}
	return len(data), nil
}

func TestSurfaceAcknowledgementsAlwaysReleaseTokens(t *testing.T) {
	for _, test := range []struct {
		name string
		sub  bool
		ack  bool
		fail bool
	}{
		{name: "no listeners"},
		{name: "failed delivery", sub: true, fail: true},
		{name: "timeout", sub: true},
		{name: "acknowledged during broadcast", sub: true, ack: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := New("test", nil, nil, nil)
			defer server.Close()
			record := &surfaceRecorder{server: server, ack: test.ack, fail: test.fail}
			if test.sub {
				server.listen(&sink{w: record})
			}
			prepared := Event{Kind: EventAskPanel, Output: "DP-1", Question: "question"}
			if shown := server.showSurface(prepared); shown != test.ack {
				t.Fatalf("shown = %v, want %v", shown, test.ack)
			}
			server.mu.Lock()
			waiting := len(server.waiting)
			server.mu.Unlock()
			if waiting != 0 {
				t.Fatalf("%d waiting tokens leaked", waiting)
			}
			if test.sub {
				if record.event.Token == "" {
					t.Fatal("no acknowledgement token was delivered")
				}
				server.acknowledge(record.event.Token)
				record.event.Token = ""
				if !reflect.DeepEqual(record.event, prepared) {
					t.Fatalf("prepared payload changed: %+v", record.event)
				}
			}
		})
	}
}
