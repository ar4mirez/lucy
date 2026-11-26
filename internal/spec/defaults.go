package spec

import (
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// applyDefaults fills omitted optional members with their schema defaults, in place (spec section
// 7, rule 4). It follows properties, $ref, allOf, items and additionalProperties schemas; branches
// of anyOf, oneOf and if/then/else are not followed, since which branch applies is a matter of the
// instance, not of the schema. A required member never takes a default. Each default is deep-copied
// so documents never share state with the compiled schema.
func applyDefaults(s *jsonschema.Schema, v any) {
	applyDefaultsDepth(s, v, 0)
}

const maxDefaultsDepth = 64

func applyDefaultsDepth(s *jsonschema.Schema, v any, depth int) {
	if s == nil || depth > maxDefaultsDepth {
		return
	}
	if s.Ref != nil {
		applyDefaultsDepth(s.Ref, v, depth+1)
	}
	for _, sub := range s.AllOf {
		applyDefaultsDepth(sub, v, depth+1)
	}
	switch x := v.(type) {
	case map[string]any:
		required := map[string]bool{}
		for _, r := range s.Required {
			required[r] = true
		}
		for name, ps := range s.Properties {
			if _, ok := x[name]; !ok && !required[name] {
				if d, ok := defaultOf(ps, 0); ok {
					x[name] = deepCopy(d)
				}
			}
			if child, ok := x[name]; ok {
				applyDefaultsDepth(ps, child, depth+1)
			}
		}
		if ap, ok := s.AdditionalProperties.(*jsonschema.Schema); ok {
			for name, child := range x {
				if _, declared := s.Properties[name]; !declared {
					applyDefaultsDepth(ap, child, depth+1)
				}
			}
		}
	case []any:
		if s.Items2020 != nil {
			for i := len(s.PrefixItems); i < len(x); i++ {
				applyDefaultsDepth(s.Items2020, x[i], depth+1)
			}
		}
		for i, ps := range s.PrefixItems {
			if i < len(x) {
				applyDefaultsDepth(ps, x[i], depth+1)
			}
		}
	}
}

// defaultOf returns a property schema's default, looking through $ref and allOf.
func defaultOf(s *jsonschema.Schema, depth int) (any, bool) {
	if s == nil || depth > maxDefaultsDepth {
		return nil, false
	}
	if s.Default != nil {
		return *s.Default, true
	}
	if s.Ref != nil {
		if d, ok := defaultOf(s.Ref, depth+1); ok {
			return d, true
		}
	}
	for _, sub := range s.AllOf {
		if d, ok := defaultOf(sub, depth+1); ok {
			return d, true
		}
	}
	return nil, false
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = deepCopy(e)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = deepCopy(e)
		}
		return a
	default:
		return v
	}
}
