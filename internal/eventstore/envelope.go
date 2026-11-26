package eventstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on what one event or one append may carry. They bound the work a single caller can make
// the store do and keep large artifacts (logs, diffs, transcripts) out of the log: store those
// elsewhere and put a reference in the event.
const (
	MaxPayloadBytes    = 64 << 10 // the JSON payload of one event
	MaxEventsPerAppend = 256      // events in one Append call
	MaxStreamBytes     = 256      // a stream ID
	MaxActorBytes      = 256      // an actor
	MaxTypeBytes       = 64       // an event type name
)

// EnvelopeVersion is the version of the envelope written by this build. It changes only when the
// envelope's own fields change, never for a payload change (that is the event type's version).
const EnvelopeVersion = 1

// Payload is the typed body of an event. Each payload Go type names its event type and the version
// of that type's schema; a changed payload shape is a new version, registered alongside the old one
// so old events stay readable.
type Payload interface {
	EventType() string
	EventVersion() int
}

// Event is one entry of the log: a versioned envelope around a JSON payload.
type Event struct {
	Seq             int64           // position in the whole log, increasing in commit order
	Stream          string          // the stream (aggregate) the event belongs to
	StreamVersion   int64           // position in its stream: 1, 2, 3, ... with no gaps
	Type            string          // event type name, for example "task.created"
	TypeVersion     int             // version of the payload schema
	EnvelopeVersion int             // version of this envelope
	Time            time.Time       // when the store appended it, UTC
	Actor           string          // who caused it, for example "owner" or "org/ar4mirez/supervisor"
	Payload         json.RawMessage // the payload as stored
}

// Registry maps event types and versions to their payload Go types. Appending an unregistered type
// is refused, and decoding is strict: unknown fields are an error.
type Registry struct {
	types map[typeKey]reflect.Type
}

type typeKey struct {
	name    string
	version int
}

var typeName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// Errors returned for invalid input.
var (
	ErrUnknownType     = errors.New("eventstore: event type not registered")
	ErrInvalidEvent    = errors.New("eventstore: invalid event")
	ErrPayloadTooLarge = errors.New("eventstore: payload too large")
	ErrSecretField     = errors.New("eventstore: payload field name looks like a secret")
)

// NewRegistry returns a registry holding the given payload types.
func NewRegistry(samples ...Payload) (*Registry, error) {
	r := &Registry{types: map[typeKey]reflect.Type{}}
	for _, s := range samples {
		if err := r.Register(s); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register adds a payload type. The sample's EventType and EventVersion name it. Registration fails
// if the name or version is invalid, the pair is already registered, or a JSON field name of the
// type looks like a secret (see SecretFieldName).
func (r *Registry) Register(sample Payload) error {
	if sample == nil {
		return fmt.Errorf("%w: nil sample", ErrInvalidEvent)
	}
	k := typeKey{sample.EventType(), sample.EventVersion()}
	if err := checkTypeKey(k); err != nil {
		return err
	}
	if _, ok := r.types[k]; ok {
		return fmt.Errorf("%w: %s v%d registered twice", ErrInvalidEvent, k.name, k.version)
	}
	t := reflect.TypeOf(sample)
	if field, ok := secretField(t, map[reflect.Type]bool{}); ok {
		return fmt.Errorf("%w: %s v%d field %q", ErrSecretField, k.name, k.version, field)
	}
	r.types[k] = t
	return nil
}

func checkTypeKey(k typeKey) error {
	if len(k.name) > MaxTypeBytes || !typeName.MatchString(k.name) {
		return fmt.Errorf("%w: event type %q must match %s and be at most %d bytes",
			ErrInvalidEvent, k.name, typeName, MaxTypeBytes)
	}
	if k.version < 1 {
		return fmt.Errorf("%w: event type %q version %d must be at least 1", ErrInvalidEvent, k.name, k.version)
	}
	return nil
}

// Decode returns an event's payload as its registered Go type. Fields the type does not know are an
// error, so a payload can never smuggle data past its schema.
func (r *Registry) Decode(e Event) (Payload, error) {
	t, ok := r.types[typeKey{e.Type, e.TypeVersion}]
	if !ok {
		return nil, fmt.Errorf("%w: %s v%d", ErrUnknownType, e.Type, e.TypeVersion)
	}
	v := reflect.New(t)
	dec := json.NewDecoder(bytes.NewReader(e.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v.Interface()); err != nil {
		return nil, fmt.Errorf("eventstore: decode %s v%d at seq %d: %w", e.Type, e.TypeVersion, e.Seq, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("eventstore: decode %s v%d at seq %d: trailing data", e.Type, e.TypeVersion, e.Seq)
	}
	return v.Elem().Interface().(Payload), nil
}

// DecodeAs decodes an event's payload and asserts its Go type.
func DecodeAs[T Payload](r *Registry, e Event) (T, error) {
	var zero T
	p, err := r.Decode(e)
	if err != nil {
		return zero, err
	}
	v, ok := p.(T)
	if !ok {
		return zero, fmt.Errorf("eventstore: seq %d is %T, not %T", e.Seq, p, zero)
	}
	return v, nil
}

// encode validates a payload against the registry and returns its JSON.
func (r *Registry) encode(p Payload) (typeKey, []byte, error) {
	if p == nil {
		return typeKey{}, nil, fmt.Errorf("%w: nil payload", ErrInvalidEvent)
	}
	k := typeKey{p.EventType(), p.EventVersion()}
	t, ok := r.types[k]
	if !ok {
		return k, nil, fmt.Errorf("%w: %s v%d", ErrUnknownType, k.name, k.version)
	}
	if got := reflect.TypeOf(p); got != t {
		return k, nil, fmt.Errorf("%w: %s v%d is registered as %v, got %v", ErrInvalidEvent, k.name, k.version, t, got)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return k, nil, fmt.Errorf("eventstore: encode %s v%d: %w", k.name, k.version, err)
	}
	if len(data) > MaxPayloadBytes {
		return k, nil, fmt.Errorf("%w: %s v%d is %d bytes, limit %d", ErrPayloadTooLarge, k.name, k.version, len(data), MaxPayloadBytes)
	}
	// Maps and json.RawMessage fields escape the registration-time check, so check the encoded keys.
	if key, ok := secretKeyInJSON(data); ok {
		return k, nil, fmt.Errorf("%w: %s v%d key %q", ErrSecretField, k.name, k.version, key)
	}
	return k, data, nil
}

// secretSuffixes are the endings of field names that name a secret. A normalized field name (lower
// case, letters and digits only) that ends with one of them is refused, so "github_token",
// "clientSecret" and "db-password" are refused while "token_count", "secret_ref" and
// "credential_policy" are allowed. The list is deliberately blunt: name a non-secret field so it
// does not end with one of these (a fencing token is a "fence", not a "fencing_token").
var secretSuffixes = []string{
	"password", "passwd", "passphrase", "secret", "token", "apikey", "privatekey", "accesskey",
	"sessionkey", "signingkey", "credential", "credentials", "cookie", "bearer",
}

// secretNames are refused only as the whole normalized name.
var secretNames = []string{"authorization", "auth", "pat", "otp"}

// SecretFieldName reports whether a JSON field name looks like it holds a secret. Events never
// carry secrets: they are kept forever, replayed into projections and read by every consumer.
func SecretFieldName(name string) bool {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	n := b.String()
	for _, s := range secretNames {
		if n == s {
			return true
		}
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// secretField walks a Go type's JSON field names.
func secretField(t reflect.Type, seen map[reflect.Type]bool) (string, bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return "", false
	}
	seen[t] = true
	for i := range t.NumField() {
		f := t.Field(i)
		name := f.Name
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		if n, _, _ := strings.Cut(tag, ","); n != "" {
			name = n
		}
		if !f.Anonymous && !f.IsExported() {
			continue
		}
		if !(f.Anonymous && tag == "") && SecretFieldName(name) {
			return name, true
		}
		if inner, ok := secretField(f.Type, seen); ok {
			return name + "." + inner, true
		}
	}
	return "", false
}

// secretKeyInJSON walks every object key in a JSON document.
func secretKeyInJSON(data []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false // encoding/json produced it; it is valid
	}
	return secretKeyIn(v)
}

func secretKeyIn(v any) (string, bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, inner := range x {
			if SecretFieldName(k) {
				return k, true
			}
			if key, ok := secretKeyIn(inner); ok {
				return k + "." + key, true
			}
		}
	case []any:
		for _, inner := range x {
			if key, ok := secretKeyIn(inner); ok {
				return key, true
			}
		}
	}
	return "", false
}

// checkLabel validates a stream ID or actor: valid UTF-8, no control characters, bounded.
func checkLabel(what, s string, max int, allowEmpty bool) error {
	if s == "" && !allowEmpty {
		return fmt.Errorf("%w: empty %s", ErrInvalidEvent, what)
	}
	if len(s) > max {
		return fmt.Errorf("%w: %s is %d bytes, limit %d", ErrInvalidEvent, what, len(s), max)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalidEvent, what)
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidEvent, what)
		}
	}
	return nil
}
