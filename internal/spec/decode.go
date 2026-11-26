package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Limits on one config file. Config is untrusted input: these bound the memory and time a hostile
// or broken file can cost.
const (
	MaxFileBytes    = 1 << 20 // one file
	MaxDocuments    = 64      // YAML documents in one file
	MaxNodes        = 100_000 // YAML nodes in one file
	MaxDecodeDepth  = 64      // nesting of mappings and sequences
	MaxScalarBytes  = 64 << 10
	maxKeyBytes     = 1024
	maxDecodeErrLen = 200
)

// ErrDecode wraps every decoding error.
var ErrDecode = errors.New("spec: cannot decode")

// Decode reads the YAML (or JSON) documents in data into JSON-shaped values: map[string]any,
// []any, string, json.Number, bool and nil. Empty documents are skipped.
//
// It is deliberately narrower than YAML: anchors, aliases and merge keys are refused (so no
// document can expand into more than it says), as are custom and binary tags, duplicate keys,
// non-scalar keys, NaN and infinities. Timestamps stay strings, as JSON Schema expects.
func Decode(data []byte) (docs []any, err error) {
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%w: %d bytes; the limit is %d", ErrDecode, len(data), MaxFileBytes)
	}
	defer func() {
		if r := recover(); r != nil {
			docs, err = nil, fmt.Errorf("%w: the YAML parser failed: %v", ErrDecode, r)
		}
	}()
	d := &decoder{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				return docs, nil
			}
			return nil, fmt.Errorf("%w: %s", ErrDecode, clip(err.Error()))
		}
		d.docs++
		if d.docs > MaxDocuments {
			return nil, fmt.Errorf("%w: more than %d documents", ErrDecode, MaxDocuments)
		}
		v, err := d.node(&n, 0)
		if err != nil {
			return nil, fmt.Errorf("%w: document %d: %w", ErrDecode, d.docs, err)
		}
		if v == nil {
			continue
		}
		docs = append(docs, v)
	}
}

type decoder struct {
	docs  int
	nodes int
}

func (d *decoder) node(n *yaml.Node, depth int) (any, error) {
	d.nodes++
	if d.nodes > MaxNodes {
		return nil, fmt.Errorf("more than %d YAML nodes", MaxNodes)
	}
	if depth > MaxDecodeDepth {
		return nil, fmt.Errorf("line %d: nested deeper than %d", n.Line, MaxDecodeDepth)
	}
	if n.Anchor != "" {
		return nil, fmt.Errorf("line %d: anchors (&%s) are not allowed in lucy config", n.Line, n.Anchor)
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return d.node(n.Content[0], depth)
	case yaml.AliasNode:
		return nil, fmt.Errorf("line %d: aliases (*%s) are not allowed in lucy config", n.Line, n.Value)
	case yaml.MappingNode:
		if t := n.ShortTag(); t != "!!map" {
			return nil, fmt.Errorf("line %d: tag %s is not allowed", n.Line, t)
		}
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, vn := n.Content[i], n.Content[i+1]
			key, err := d.key(k)
			if err != nil {
				return nil, err
			}
			if _, dup := m[key]; dup {
				return nil, fmt.Errorf("line %d: key %q is defined twice", k.Line, key)
			}
			v, err := d.node(vn, depth+1)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		return m, nil
	case yaml.SequenceNode:
		if t := n.ShortTag(); t != "!!seq" {
			return nil, fmt.Errorf("line %d: tag %s is not allowed", n.Line, t)
		}
		a := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := d.node(c, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		return a, nil
	case yaml.ScalarNode:
		return scalar(n)
	default:
		return nil, fmt.Errorf("line %d: unsupported YAML node", n.Line)
	}
}

func (d *decoder) key(k *yaml.Node) (string, error) {
	d.nodes++
	if k.Kind != yaml.ScalarNode {
		if k.Kind == yaml.AliasNode {
			return "", fmt.Errorf("line %d: aliases (*%s) are not allowed in lucy config", k.Line, k.Value)
		}
		return "", fmt.Errorf("line %d: a mapping key must be a plain value", k.Line)
	}
	if k.Anchor != "" {
		return "", fmt.Errorf("line %d: anchors (&%s) are not allowed in lucy config", k.Line, k.Anchor)
	}
	switch t := k.ShortTag(); t {
	case "!!merge":
		return "", fmt.Errorf("line %d: merge keys (<<) are not allowed in lucy config", k.Line)
	case "!!str", "!!int", "!!float", "!!bool", "!!timestamp":
	default:
		return "", fmt.Errorf("line %d: a %s mapping key is not allowed", k.Line, t)
	}
	if len(k.Value) > maxKeyBytes {
		return "", fmt.Errorf("line %d: a key is longer than %d bytes", k.Line, maxKeyBytes)
	}
	return k.Value, nil
}

func scalar(n *yaml.Node) (any, error) {
	if len(n.Value) > MaxScalarBytes {
		return nil, fmt.Errorf("line %d: a value is longer than %d bytes", n.Line, MaxScalarBytes)
	}
	switch t := n.ShortTag(); t {
	case "!!str", "!!timestamp":
		return n.Value, nil
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return nil, fmt.Errorf("line %d: %s", n.Line, clip(err.Error()))
		}
		return b, nil
	case "!!int", "!!float":
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, fmt.Errorf("line %d: %s", n.Line, clip(err.Error()))
		}
		return number(n.Line, v)
	default:
		return nil, fmt.Errorf("line %d: tag %s is not allowed", n.Line, t)
	}
}

func number(line int, v any) (any, error) {
	switch x := v.(type) {
	case int:
		return json.Number(strconv.Itoa(x)), nil
	case int64:
		return json.Number(strconv.FormatInt(x, 10)), nil
	case uint64:
		return json.Number(strconv.FormatUint(x, 10)), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("line %d: NaN and infinity are not JSON numbers", line)
		}
		return json.Number(strconv.FormatFloat(x, 'g', -1, 64)), nil
	default:
		return nil, fmt.Errorf("line %d: unsupported number %v", line, v)
	}
}

func clip(s string) string {
	s = strings.TrimPrefix(s, "yaml: ")
	if len(s) > maxDecodeErrLen {
		s = s[:maxDecodeErrLen] + "..."
	}
	return s
}
