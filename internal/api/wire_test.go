package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeRequest(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		code   Code   // 0: accepted
		field  string // the error's field, when it names one
		wantID string // the id echoed back
		method string
		params string
	}{
		{"minimal", `{"jsonrpc":"2.0","id":1,"method":"health"}`, 0, "", "1", "health", ""},
		{"string id", `{"jsonrpc":"2.0","id":"a-1","method":"status.get"}`, 0, "", `"a-1"`, "status.get", ""},
		{"null params", `{"jsonrpc":"2.0","id":2,"method":"health","params":null}`, 0, "", "2", "health", ""},
		{"object params", `{"jsonrpc":"2.0","id":3,"method":"events.list","params":{"limit":5}}`, 0, "", "3", "events.list", `{"limit":5}`},
		{"whitespace", " \n{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"health\"}\n", 0, "", "4", "health", ""},

		{"empty body", ``, CodeParseError, "", "null", "", ""},
		{"not json", `{nope`, CodeParseError, "", "null", "", ""},
		{"trailing data", `{"jsonrpc":"2.0","id":1,"method":"health"} {}`, CodeParseError, "", "null", "", ""},
		{"trailing garbage", `{"jsonrpc":"2.0","id":1,"method":"health"}x`, CodeParseError, "", "null", "", ""},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"health"}]`, CodeInvalidRequest, "", "null", "", ""},
		{"bad batch", `[{`, CodeParseError, "", "null", "", ""},
		{"array not object", `"health"`, CodeInvalidRequest, "", "null", "", ""},
		{"null body", `null`, CodeInvalidRequest, "", "null", "", ""},
		{"unknown member", `{"jsonrpc":"2.0","id":1,"method":"health","extra":1}`, CodeInvalidRequest, "extra", "1", "", ""},
		{"case-folded member", `{"jsonrpc":"2.0","id":1,"Method":"health"}`, CodeInvalidRequest, "Method", "1", "", ""},
		{"duplicate member", `{"jsonrpc":"2.0","id":1,"method":"health","method":"daemon.shutdown"}`, CodeInvalidRequest, "method", "null", "", ""},
		{"case-duplicate member", `{"jsonrpc":"2.0","id":1,"method":"health","METHOD":"x"}`, CodeInvalidRequest, "METHOD", "null", "", ""},
		{"duplicate params key", `{"jsonrpc":"2.0","id":1,"method":"events.list","params":{"limit":1,"limit":2}}`, CodeInvalidRequest, "limit", "null", "", ""},
		{"wrong version", `{"jsonrpc":"1.0","id":1,"method":"health"}`, CodeInvalidRequest, "jsonrpc", "1", "", ""},
		{"missing version", `{"id":1,"method":"health"}`, CodeInvalidRequest, "jsonrpc", "1", "", ""},
		{"notification", `{"jsonrpc":"2.0","method":"health"}`, CodeInvalidRequest, "id", "null", "", ""},
		{"null id", `{"jsonrpc":"2.0","id":null,"method":"health"}`, CodeInvalidRequest, "id", "null", "", ""},
		{"float id", `{"jsonrpc":"2.0","id":1.5,"method":"health"}`, CodeInvalidRequest, "id", "null", "", ""},
		{"exponent id", `{"jsonrpc":"2.0","id":1e3,"method":"health"}`, CodeInvalidRequest, "id", "null", "", ""},
		{"huge id", `{"jsonrpc":"2.0","id":99999999999999999999,"method":"health"}`, CodeInvalidRequest, "id", "null", "", ""},
		{"object id", `{"jsonrpc":"2.0","id":{},"method":"health"}`, CodeInvalidRequest, "id", "null", "", ""},
		{"long id", `{"jsonrpc":"2.0","id":"` + strings.Repeat("x", 129) + `","method":"health"}`, CodeInvalidRequest, "id", "null", "", ""},
		{"missing method", `{"jsonrpc":"2.0","id":1}`, CodeInvalidRequest, "method", "1", "", ""},
		{"numeric method", `{"jsonrpc":"2.0","id":1,"method":7}`, CodeInvalidRequest, "method", "1", "", ""},
		{"upper method", `{"jsonrpc":"2.0","id":1,"method":"Health"}`, CodeInvalidRequest, "method", "1", "", ""},
		{"long method", `{"jsonrpc":"2.0","id":1,"method":"` + strings.Repeat("a", 65) + `"}`, CodeInvalidRequest, "method", "1", "", ""},
		{"positional params", `{"jsonrpc":"2.0","id":1,"method":"health","params":[1]}`, CodeInvalidParams, "params", "1", "", ""},
		{"scalar params", `{"jsonrpc":"2.0","id":1,"method":"health","params":"x"}`, CodeInvalidParams, "params", "1", "", ""},
		{"too deep", `{"jsonrpc":"2.0","id":1,"method":"health","params":{"a":` + strings.Repeat("[", 40) + strings.Repeat("]", 40) + `}}`, CodeInvalidRequest, "", "null", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, req, err := DecodeRequest([]byte(tc.body))
			if string(id) != tc.wantID {
				t.Errorf("id = %s, want %s", id, tc.wantID)
			}
			if tc.code == 0 {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if req.Method != tc.method || string(req.Params) != tc.params {
					t.Errorf("got method %q params %q", req.Method, req.Params)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %q", tc.body)
			}
			checkTypedError(t, err)
			if err.Code != tc.code {
				t.Errorf("code = %d (%s), want %d", err.Code, err.Data.Help, tc.code)
			}
			if tc.field != "" && err.Data.Field != tc.field {
				t.Errorf("field = %q, want %q", err.Data.Field, tc.field)
			}
		})
	}
}

// checkTypedError asserts the invariants of every error the API sends: a known code, the code's
// fixed message and kind.
func checkTypedError(t *testing.T, e *Error) {
	t.Helper()
	if !e.Code.Known() || e.Message != e.Code.Message() || e.Data == nil || e.Data.Kind != e.Code.Kind() {
		t.Errorf("not a typed error: %+v", e)
	}
}

func TestDecodeParams(t *testing.T) {
	type out struct {
		code  Code
		field string
	}
	cases := []struct {
		name   string
		method string
		params string
		want   out
	}{
		{"absent", MethodEvents, ``, out{}},
		{"empty", MethodEvents, `{}`, out{}},
		{"valid", MethodEvents, `{"after_seq":3,"limit":500}`, out{}},
		{"unknown field", MethodEvents, `{"limitt":1}`, out{CodeInvalidParams, "limitt"}},
		{"case-folded field", MethodEvents, `{"Limit":1}`, out{CodeInvalidParams, "Limit"}},
		{"negative", MethodEvents, `{"after_seq":-1}`, out{CodeInvalidParams, "after_seq"}},
		{"over max", MethodEvents, `{"limit":501}`, out{CodeInvalidParams, "limit"}},
		{"wrong type", MethodEvents, `{"limit":"5"}`, out{CodeInvalidParams, "limit"}},
		{"fraction", MethodEvents, `{"limit":1.5}`, out{CodeInvalidParams, "limit"}},
		{"no params allowed", MethodHealth, `{"x":1}`, out{CodeInvalidParams, "x"}},
		{"hello valid", MethodHello, `{"client":"t","api_versions":[1]}`, out{}},
		{"hello missing client", MethodHello, `{"api_versions":[1]}`, out{CodeInvalidParams, "client"}},
		{"hello empty versions", MethodHello, `{"client":"t","api_versions":[]}`, out{CodeInvalidParams, "api_versions"}},
		{"hello too many versions", MethodHello, `{"client":"t","api_versions":[1,1,1,1,1,1,1,1,1]}`, out{CodeInvalidParams, "api_versions"}},
		{"hello zero version", MethodHello, `{"client":"t","api_versions":[0]}`, out{CodeInvalidParams, "api_versions"}},
		{"hello long client", MethodHello, `{"client":"` + strings.Repeat("c", 65) + `","api_versions":[1]}`, out{CodeInvalidParams, "client"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := Lookup(tc.method)
			if !ok {
				t.Fatalf("no method %s", tc.method)
			}
			v := m.NewParams()
			err := DecodeParams(json.RawMessage(tc.params), v)
			if tc.want.code == 0 {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %s", tc.params)
			}
			checkTypedError(t, err)
			if err.Code != tc.want.code || err.Data.Field != tc.want.field {
				t.Errorf("got %d field %q, want %d field %q", err.Code, err.Data.Field, tc.want.code, tc.want.field)
			}
		})
	}
}

func TestErrorIsAndString(t *testing.T) {
	e := FieldError(CodeInvalidParams, "limit", "must be at most 500")
	if !IsCode(e, CodeInvalidParams) || IsCode(e, CodeInternal) {
		t.Error("IsCode mismatch")
	}
	if got := e.Error(); !strings.Contains(got, "invalid params") || !strings.Contains(got, "limit") {
		t.Errorf("Error() = %q", got)
	}
	for _, c := range Codes {
		if !c.Known() || c.Kind() == "" || c.Message() == "" {
			t.Errorf("code %d has no kind or message", c)
		}
	}
}

func TestCallerActor(t *testing.T) {
	if got := (Caller{Transport: "unix", UID: 501}).Actor(); got != "local/uid/501" {
		t.Errorf("Actor = %q", got)
	}
	c := Caller{Scopes: []Scope{ScopeRead}}
	if !c.HasScope(ScopeRead) || c.HasScope(ScopeAdmin) {
		t.Error("HasScope mismatch")
	}
}
