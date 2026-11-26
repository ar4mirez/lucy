package api

import (
	_ "embed"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Document is the committed OpenRPC document, generated from the method table by
// `mise run api-schema` and checked current by TestOpenRPCDocument. rpc.discover returns it.
//
//go:embed openrpc.json
var Document []byte

// OpenRPC is the API's schema as an OpenRPC 1.3 document (https://spec.open-rpc.org): methods with
// by-name params, results, the error codes and lucy's extensions (x-lucy-scope, x-lucy-mutating,
// x-lucy-group). Params schemas forbid additional properties, as the server does; result schemas
// allow them, because a v1 result may grow new optional fields.
func OpenRPC() ([]byte, error) {
	type descriptor struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Required    bool           `json:"required"`
		Schema      map[string]any `json:"schema"`
	}
	type method struct {
		Name           string       `json:"name"`
		Summary        string       `json:"summary"`
		ParamStructure string       `json:"paramStructure"`
		Params         []descriptor `json:"params"`
		Result         descriptor   `json:"result"`
		Scope          Scope        `json:"x-lucy-scope"`
		Mutating       bool         `json:"x-lucy-mutating"`
		Group          string       `json:"x-lucy-group"`
	}
	type errorObject struct {
		Code    Code   `json:"code"`
		Message string `json:"message"`
	}
	doc := struct {
		OpenRPC string `json:"openrpc"`
		Info    struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Version     string `json:"version"`
		} `json:"info"`
		Servers []map[string]string `json:"servers"`
		Methods []method            `json:"methods"`
		Comps   struct {
			Errors  map[string]errorObject    `json:"errors"`
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}{OpenRPC: "1.3.2"}
	doc.Info.Title = "lucy API"
	doc.Info.Description = "One typed API behind every lucy surface: JSON-RPC 2.0, one request per POST to " + Path +
		", over the daemon's unix socket (and HTTPS for remote surfaces, from m1). See ADR D10."
	doc.Info.Version = strconv.Itoa(Version) + ".0.0"
	doc.Servers = []map[string]string{{"name": "local", "url": "unix:$XDG_RUNTIME_DIR/lucy/lucy.sock" + Path}}

	for _, m := range methods {
		out := method{Name: m.Name, Summary: m.Summary, ParamStructure: "by-name", Params: []descriptor{},
			Scope: m.Scope, Mutating: m.Mutating, Group: m.Group}
		for i := range m.Params.NumField() {
			f := m.Params.Field(i)
			name, ok := jsonName(f)
			if !ok {
				continue
			}
			b := parseBounds(f)
			out.Params = append(out.Params, descriptor{Name: name, Description: f.Tag.Get("doc"),
				Required: b.required || !omitEmpty(f), Schema: schemaOf(f.Type, b, true)})
		}
		out.Result = descriptor{Name: "result", Required: true, Schema: schemaOf(m.Result, bounds{}, false)}
		if m.Result == reflect.TypeFor[json.RawMessage]() {
			out.Result.Schema = map[string]any{"type": "object", "description": "An OpenRPC document (this one)."}
		}
		doc.Methods = append(doc.Methods, out)
	}
	doc.Comps.Errors = map[string]errorObject{}
	for _, c := range Codes {
		doc.Comps.Errors[c.Kind()] = errorObject{c, c.Message()}
	}
	doc.Comps.Schemas = map[string]map[string]any{"ErrorData": schemaOf(reflect.TypeFor[ErrorData](), bounds{}, false)}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// schemaOf returns the JSON Schema of a Go type. strict forbids additional properties on objects.
func schemaOf(t reflect.Type, b bounds, strict bool) map[string]any {
	s := map[string]any{}
	switch {
	case t == reflect.TypeFor[time.Time]():
		s["type"], s["format"] = "string", "date-time"
	case t == reflect.TypeFor[json.RawMessage]():
		s["description"] = "Any JSON value."
	case t.Kind() == reflect.Struct:
		s["type"] = "object"
		props := map[string]any{}
		required := []string{}
		for i := range t.NumField() {
			f := t.Field(i)
			name, ok := jsonName(f)
			if !ok {
				continue
			}
			fb := parseBounds(f)
			p := schemaOf(f.Type, fb, strict)
			if d := f.Tag.Get("doc"); d != "" {
				p["description"] = d
			}
			props[name] = p
			if fb.required || !omitEmpty(f) {
				required = append(required, name)
			}
		}
		s["properties"] = props
		s["required"] = required
		if strict {
			s["additionalProperties"] = false
		}
	case t.Kind() == reflect.Slice:
		s["type"] = "array"
		item := bounds{min: b.itemMin, max: b.itemMax}
		s["items"] = schemaOf(t.Elem(), item, strict)
		setInt(s, "maxItems", b.maxItems)
		if b.required {
			s["minItems"] = 1
		}
	case t.Kind() == reflect.String:
		s["type"] = "string"
		setInt(s, "maxLength", b.maxLen)
		if b.required {
			s["minLength"] = 1
		}
	case t.Kind() == reflect.Bool:
		s["type"] = "boolean"
	case strings.HasPrefix(t.Kind().String(), "int"):
		s["type"] = "integer"
		setInt(s, "minimum", b.min)
		setInt(s, "maximum", b.max)
	default:
		panic("api: no schema for " + t.String()) // a programming error, caught by tests
	}
	return s
}

func setInt(s map[string]any, key string, v *int64) {
	if v != nil {
		s[key] = *v
	}
}
