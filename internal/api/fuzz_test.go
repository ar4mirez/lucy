package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzDecodeRequest feeds arbitrary bodies to the request decoder and params decoders. It must
// never panic; every refusal must be a typed error; every accepted request must have a valid
// method name, an id that is a string or an integer, and params that are absent or an object.
func FuzzDecodeRequest(f *testing.F) {
	seeds := []string{
		`{"jsonrpc":"2.0","id":1,"method":"health"}`,
		`{"jsonrpc":"2.0","id":"x","method":"events.list","params":{"after_seq":1,"limit":10}}`,
		`{"jsonrpc":"2.0","id":2,"method":"hello","params":{"client":"c","api_versions":[1]}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"health"}]`,
		`{"jsonrpc":"2.0","id":1,"method":"health","method":"x"}`,
		`{"jsonrpc":"2.0","id":1e400,"method":"health"}`,
		`{"a":[[[[[[[[[[]]]]]]]]]]}`,
		"{\"jsonrpc\":\"2.0\",\"id\":\"\xff\",\"method\":\"health\"}",
		``, `null`, `{}`, `[`, `"`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		id, req, err := DecodeRequest(body)
		if !json.Valid(id) {
			t.Fatalf("echoed id %q is not JSON", id)
		}
		if err != nil {
			checkTypedError(t, err)
			if req != nil {
				t.Fatal("both a request and an error")
			}
			return
		}
		if !methodName.MatchString(req.Method) || len(req.Method) > MaxMethodBytes {
			t.Fatalf("accepted method %q", req.Method)
		}
		if _, derr := decodeID(req.ID); derr != nil {
			t.Fatalf("accepted id %s", req.ID)
		}
		if req.Params != nil && bytes.TrimSpace(req.Params)[0] != '{' {
			t.Fatalf("accepted params %s", req.Params)
		}
		for _, m := range Methods() {
			if perr := DecodeParams(req.Params, m.NewParams()); perr != nil {
				checkTypedError(t, perr)
			}
		}
	})
}
