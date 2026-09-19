package zded

import (
	"encoding/json"
	"testing"
)

func TestOptionalIDsPreserveLegacyJSON(t *testing.T) {
	for _, test := range []struct {
		value any
		want  string
	}{
		{Request{Method: "status"}, `{"method":"status"}`},
		{Request{Method: MethodShown, Args: []string{"1"}}, `{"method":"shown","args":["1"]}`},
		{ok("thanks"), `{"ok":"thanks"}`},
		{Response{Error: "refused"}, `{"error":"refused"}`},
		{Response{Ok: json.RawMessage(`[]`), Note: "missing screen"}, `{"ok":[],"note":"missing screen"}`},
		{Event{Kind: EventAskText, Text: "answer"}, `{"kind":"ask.text","text":"answer"}`},
	} {
		got, err := json.Marshal(test.value)
		if err != nil || string(got) != test.want {
			t.Errorf("marshal %#v = %s, %v; want %s", test.value, got, err, test.want)
		}
	}
}

func TestDispatchEchoesOptionalIDOnSuccessAndErrors(t *testing.T) {
	server := New("test", nil, &fakeCompositor{m: twoDesks()}, nil)
	defer server.Close()
	for _, request := range []Request{
		{Method: "status"}, {Method: MethodShown, Args: []string{"late"}},
		{Method: "desk.switch"}, {Method: "window.jump-to", Args: []string{"bad"}},
		{Method: "net.connect"}, {Method: "system.power", Args: []string{"bad"}},
		{Method: "palette.run"}, {Method: "queue.add"}, {Method: "ask.panel", Args: []string{" "}},
		{Method: "clip.clear"}, {Method: MethodAskRun}, {Method: MethodEvents}, {Method: "unknown"},
	} {
		legacy := server.Dispatch(request)
		request.ID = "opaque\n\"123"
		got := server.Dispatch(request)
		if got.ID != request.ID {
			t.Errorf("%s ID = %q, want %q", request.Method, got.ID, request.ID)
		}
		got.ID = ""
		if string(responseLine(got)) != string(responseLine(legacy)) {
			t.Errorf("%s payload changed: %s vs %s", request.Method, responseLine(got), responseLine(legacy))
		}
	}
}

func TestEncodingFailureKeepsRequestID(t *testing.T) {
	for _, requestID := range []string{"", "bad-payload"} {
		var response Response
		data := responseLine(Response{ID: requestID, Ok: json.RawMessage(`{`)})
		if err := json.Unmarshal(data, &response); err != nil {
			t.Fatal(err)
		}
		if response.ID != requestID || response.Error != "zded could not encode its own reply" {
			t.Fatalf("encoding fallback = %s", data)
		}
		if requestID == "" && string(data) != "{\"error\":\"zded could not encode its own reply\"}\n" {
			t.Fatalf("legacy fallback changed: %s", data)
		}
	}
}
