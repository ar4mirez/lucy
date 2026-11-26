package api

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Field bounds are declared once, in a `lucy` struct tag, and used both to validate params and to
// generate the schema, so the two cannot drift:
//
//	min=N, max=N            integer bounds
//	maxlen=N                string length in bytes
//	maxitems=N              array length
//	itemmin=N, itemmax=N    bounds on each integer of an array
//	required                a string or array that must be present and non-empty
type bounds struct {
	min, max, itemMin, itemMax *int64
	maxLen, maxItems           *int64
	required                   bool
}

func parseBounds(f reflect.StructField) bounds {
	var b bounds
	tag := f.Tag.Get("lucy")
	if tag == "" {
		return b
	}
	for _, part := range strings.Split(tag, ",") {
		key, val, _ := strings.Cut(part, "=")
		if key == "required" {
			b.required = true
			continue
		}
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			panic(fmt.Sprintf("api: bad lucy tag %q on %s", tag, f.Name)) // a programming error, caught by tests
		}
		switch key {
		case "min":
			b.min = &n
		case "max":
			b.max = &n
		case "maxlen":
			b.maxLen = &n
		case "maxitems":
			b.maxItems = &n
		case "itemmin":
			b.itemMin = &n
		case "itemmax":
			b.itemMax = &n
		default:
			panic(fmt.Sprintf("api: unknown lucy tag key %q on %s", key, f.Name))
		}
	}
	return b
}

// validate checks a decoded params struct against its declared bounds.
func validate(v any) *Error {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	t := rv.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		name, ok := jsonName(f)
		if !ok {
			continue
		}
		if err := checkField(name, parseBounds(f), rv.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

func checkField(name string, b bounds, v reflect.Value) *Error {
	bad := func(help string) *Error { return FieldError(CodeInvalidParams, name, help) }
	switch v.Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32:
		n := v.Int()
		if b.min != nil && n < *b.min {
			return bad(fmt.Sprintf("must be at least %d", *b.min))
		}
		if b.max != nil && n > *b.max {
			return bad(fmt.Sprintf("must be at most %d", *b.max))
		}
	case reflect.String:
		s := v.String()
		if b.required && s == "" {
			return bad("is required")
		}
		if b.maxLen != nil && int64(len(s)) > *b.maxLen {
			return bad(fmt.Sprintf("must be at most %d bytes", *b.maxLen))
		}
		if !utf8.ValidString(s) {
			return bad("must be valid UTF-8")
		}
	case reflect.Slice:
		if b.required && v.Len() == 0 {
			return bad("is required and must not be empty")
		}
		if b.maxItems != nil && int64(v.Len()) > *b.maxItems {
			return bad(fmt.Sprintf("must have at most %d items", *b.maxItems))
		}
		for i := range v.Len() {
			item := v.Index(i)
			if item.Kind() != reflect.Int && item.Kind() != reflect.Int64 {
				continue
			}
			if b.itemMin != nil && item.Int() < *b.itemMin {
				return bad(fmt.Sprintf("items must be at least %d", *b.itemMin))
			}
			if b.itemMax != nil && item.Int() > *b.itemMax {
				return bad(fmt.Sprintf("items must be at most %d", *b.itemMax))
			}
		}
	}
	return nil
}
