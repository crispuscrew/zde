package zded

import "encoding/json"

// Request is one input line. ID is optional and opaque; empty preserves legacy JSON.
type Request struct {
	Method string   `json:"method"`
	Args   []string `json:"args,omitempty"`
	ID     string   `json:"id,omitempty"`
}

// Response is one reply: exactly one of Ok and Error is set. Note describes
// a partial limitation without changing the payload. ID echoes the request.
type Response struct {
	Ok    json.RawMessage `json:"ok,omitempty"`
	Error string          `json:"error,omitempty"`
	Note  string          `json:"note,omitempty"`
	ID    string          `json:"id,omitempty"`
}

func ok(v any) Response {
	raw, err := json.Marshal(v)
	if err != nil {
		return Response{Error: err.Error()}
	}
	return Response{Ok: raw}
}

// responseLine also preserves correlation when the payload cannot be encoded.
func responseLine(resp Response) []byte {
	line, err := json.Marshal(resp)
	if err != nil {
		line, _ = json.Marshal(Response{ID: resp.ID, Error: "zded could not encode its own reply"})
	}
	return append(line, '\n')
}
