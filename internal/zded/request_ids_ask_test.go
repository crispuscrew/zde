package zded

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAskRefusalsKeepTheirRequestID(t *testing.T) {
	writeTiers(t, map[string][]string{TierProvider: fakeTier("echo")})
	for _, test := range []struct {
		name    string
		args    []string
		busy    bool
		full    bool
		stopped bool
	}{
		{name: "arity"},
		{name: "empty question", args: []string{TierProvider, " "}},
		{name: "empty prior turn", args: []string{TierProvider, "question", " ", "answer"}},
		{name: "unknown tier", args: []string{"missing", "question"}},
		{name: "question limit", args: []string{TierProvider, strings.Repeat("x", askContextMax+1)}},
		{name: "context limit", args: []string{TierProvider, "question", strings.Repeat("x", askContextMax), "answer"}},
		{name: "connection busy", args: []string{TierProvider, "question"}, busy: true},
		{name: "daemon full", args: []string{TierProvider, "question"}, full: true},
		{name: "daemon stopping", args: []string{TierProvider, "question"}, stopped: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := New("test", nil, nil, nil)
			defer server.Close()
			record := &recorder{}
			conn := &sink{w: record}
			conn.asking.Store(test.busy)
			if test.full {
				server.asks = asksMax
			}
			if test.stopped {
				server.runStop()
			}
			server.askOn(conn, test.args, test.name)
			var response Response
			if err := json.Unmarshal([]byte(record.String()), &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != test.name || response.Error == "" {
				t.Fatalf("refusal = %+v", response)
			}
			if !test.full && server.asking() != 0 {
				t.Fatal("refusal leaked a run claim")
			}
		})
	}
}
