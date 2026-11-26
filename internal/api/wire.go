package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Request is one decoded JSON-RPC request.
type Request struct {
	ID     json.RawMessage // a string or an integer, normalized; echoed in the response
	Method string
	Params json.RawMessage // an object, or nil when absent or null
}

// Response is one JSON-RPC response: exactly one of Result and Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

var (
	methodName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	intID      = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,18})$`)
	nullID     = json.RawMessage("null")
)

// envelopeMembers are the only members a request may have, matched exactly (case-sensitive).
var envelopeMembers = map[string]bool{"jsonrpc": true, "id": true, "method": true, "params": true}

// DecodeRequest decodes and validates one request body. On failure it returns the request id when
// it could be read (so the error response can echo it) and a typed error.
func DecodeRequest(body []byte) (json.RawMessage, *Request, *Error) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if json.Valid(trimmed) {
			return nullID, nil, NewError(CodeInvalidRequest, "batch requests are not supported; send one request per POST")
		}
		return nullID, nil, NewError(CodeParseError, "the body is not valid JSON")
	}
	if err := checkStructure(body); err != nil {
		return nullID, nil, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil || members == nil {
		return nullID, nil, NewError(CodeInvalidRequest, "the request must be a JSON object")
	}

	id := nullID
	rawID, hasID := members["id"]
	if hasID {
		norm, err := decodeID(rawID)
		if err != nil {
			return nullID, nil, err
		}
		id = norm
	}
	for k := range members {
		if !envelopeMembers[k] {
			return id, nil, FieldError(CodeInvalidRequest, k, "unknown request member; allowed: jsonrpc, id, method, params")
		}
	}
	var version string
	if err := json.Unmarshal(members["jsonrpc"], &version); err != nil || version != "2.0" {
		return id, nil, FieldError(CodeInvalidRequest, "jsonrpc", `must be "2.0"`)
	}
	if !hasID {
		return id, nil, FieldError(CodeInvalidRequest, "id", "an id is required; notifications are not supported")
	}
	var method string
	if err := json.Unmarshal(members["method"], &method); err != nil || method == "" {
		return id, nil, FieldError(CodeInvalidRequest, "method", "must be a non-empty string")
	}
	if len(method) > MaxMethodBytes || !methodName.MatchString(method) {
		return id, nil, FieldError(CodeInvalidRequest, "method", "must be dotted lower-case words, at most 64 bytes")
	}
	params := members["params"]
	switch t := bytes.TrimSpace(params); {
	case len(t) == 0 || bytes.Equal(t, nullID):
		params = nil
	case t[0] != '{':
		return id, nil, FieldError(CodeInvalidParams, "params", "params must be an object (by name)")
	}
	return id, &Request{ID: id, Method: method, Params: params}, nil
}

// decodeID accepts a string of at most MaxIDBytes or an integer, and returns it re-encoded.
func decodeID(raw json.RawMessage) (json.RawMessage, *Error) {
	t := bytes.TrimSpace(raw)
	if len(t) > 0 && t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err != nil || len(s) > MaxIDBytes {
			return nil, FieldError(CodeInvalidRequest, "id", "a string id must be at most 128 bytes")
		}
		out, _ := json.Marshal(s)
		return out, nil
	}
	if intID.Match(t) {
		if _, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			return json.RawMessage(t), nil
		}
	}
	return nil, FieldError(CodeInvalidRequest, "id", "must be a string or an integer")
}

// checkStructure walks the JSON tokens once and refuses invalid JSON, trailing data, nesting deeper
// than MaxJSONDepth and duplicate object keys. Keys are compared case-insensitively, because Go's
// decoder matches struct fields that way: {"limit":1,"LIMIT":2} must not pick one silently.
func checkStructure(body []byte) *Error {
	type frame struct {
		obj     bool
		wantKey bool
		keys    map[string]bool
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var stack []*frame
	values := 0
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].obj {
			stack[n-1].wantKey = true
		} else if n == 0 {
			values++
		}
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return NewError(CodeParseError, "the body is not valid JSON")
		}
		if values > 0 {
			return NewError(CodeParseError, "trailing data after the request object")
		}
		if n := len(stack); n > 0 && stack[n-1].obj && stack[n-1].wantKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:n-1]
				valueDone()
				continue
			}
			key, _ := tok.(string)
			folded := strings.ToLower(key)
			if stack[n-1].keys[folded] {
				return FieldError(CodeInvalidRequest, key, "duplicate key")
			}
			stack[n-1].keys[folded] = true
			stack[n-1].wantKey = false
			continue
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			if len(stack) >= MaxJSONDepth {
				return NewError(CodeInvalidRequest, "the request is nested too deeply")
			}
			obj := tok == json.Delim('{')
			stack = append(stack, &frame{obj: obj, wantKey: obj, keys: map[string]bool{}})
		case json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
	if values == 0 {
		return NewError(CodeParseError, "the body is empty")
	}
	return nil
}

// DecodeParams decodes params into v, a pointer to the method's params struct, strictly: every key
// must be a declared field (exact case), and the values must meet the field's declared bounds.
// Absent or null params decode as an empty object.
func DecodeParams(raw json.RawMessage, v any) *Error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil || keys == nil {
		return FieldError(CodeInvalidParams, "params", "params must be an object (by name)")
	}
	if e := checkKeys(keys, reflect.TypeOf(v).Elem(), ""); e != nil {
		return e
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return FieldError(CodeInvalidParams, te.Field, "want "+jsonKind(te.Type))
		}
		return FieldError(CodeInvalidParams, "params", "params do not match the method's schema")
	}
	return validate(v)
}

// checkKeys refuses any member that is not a declared field of t, by exact name, in the object
// and in every nested object, including objects inside arrays. Go's decoder would otherwise match
// a case-folded member ("Source" for "source") inside nested objects.
func checkKeys(keys map[string]json.RawMessage, t reflect.Type, path string) *Error {
	fields := jsonFields(t)
	for k, raw := range keys {
		name := k
		if path != "" {
			name = path + "." + k
		}
		f, ok := fields[k]
		if !ok {
			return FieldError(CodeInvalidParams, name, "unknown field")
		}
		if e := checkNested(raw, f.Type, name); e != nil {
			return e
		}
	}
	return nil
}

func checkNested(raw json.RawMessage, t reflect.Type, path string) *Error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t.Kind() == reflect.Struct && t != reflect.TypeFor[time.Time]():
		var keys map[string]json.RawMessage
		if json.Unmarshal(raw, &keys) != nil || keys == nil {
			return nil // not an object: the decoder reports the type error
		}
		return checkKeys(keys, t, path)
	case t.Kind() == reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		for i, item := range items {
			if e := checkNested(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); e != nil {
				return e
			}
		}
	}
	return nil
}

// NewResult builds a success response.
func NewResult(id json.RawMessage, result any) (*Response, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{JSONRPC: "2.0", ID: id, Result: data}, nil
}

// NewErrorResponse builds an error response.
func NewErrorResponse(id json.RawMessage, e *Error) *Response {
	if len(id) == 0 {
		id = nullID
	}
	return &Response{JSONRPC: "2.0", ID: id, Error: e}
}

// jsonFields maps a struct's JSON member names to their fields.
func jsonFields(t reflect.Type) map[string]reflect.StructField {
	out := map[string]reflect.StructField{}
	for i := range t.NumField() {
		f := t.Field(i)
		if name, ok := jsonName(f); ok {
			out[name] = f
		}
	}
	return out
}

// jsonName is a field's JSON member name; false for unexported or `json:"-"` fields.
func jsonName(f reflect.StructField) (string, bool) {
	if !f.IsExported() {
		return "", false
	}
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}
	return name, true
}

func omitEmpty(f reflect.StructField) bool {
	_, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
	return strings.Contains(","+opts+",", ",omitempty,")
}

// jsonKind names a Go type as the JSON value a client should send.
func jsonKind(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	}
	return "a different type"
}
