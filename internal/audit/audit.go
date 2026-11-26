// Package audit is lucy's audit log: an append-only, hash-chained sequence of audit entries in the
// ai-sdlc wire format (spec section 12, LUCY-SPEC-009).
//
// Each entry's hash is the SHA-256 of the RFC 8785 canonical JSON of the entry without its hash
// field (schemaVersion "2"), and each entry after the first names the hash of the one before it,
// so changing any byte of any entry breaks verification from that entry on. lucy reproduces the
// spec's cross-SDK fixture and verifies the chains the spec's SDKs wrote (audit_test.go).
//
// In lucy the chain is a projector over the event log (projector.go): every event becomes one
// entry, written in the same transaction as the event. VerifyStore checks the chain and that every
// entry still matches the event it was made from, so an edit to either is detected. The chain's
// head hash is what a later, signed proof anchors outside the database.
package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/ar4mirez/lucy/internal/jcs"
	"github.com/ar4mirez/lucy/internal/spec"
)

// SchemaVersion selects the spec's hash rule; lucy writes and verifies only this version.
const SchemaVersion = "2"

// Decisions an entry records (spec section 12).
const (
	DecisionAllowed    = "allowed"
	DecisionDenied     = "denied"
	DecisionOverridden = "overridden"
)

// TimeFormat is the entry timestamp format: RFC 3339 UTC with millisecond precision.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// Entry is one audit entry, in the spec's wire format.
type Entry struct {
	SchemaVersion string          `json:"schemaVersion"`
	ID            string          `json:"id"`
	Timestamp     string          `json:"timestamp"`
	Actor         string          `json:"actor"`
	Action        string          `json:"action"`
	Resource      string          `json:"resource"`
	Policy        string          `json:"policy,omitempty"`
	Decision      string          `json:"decision"`
	Details       json.RawMessage `json:"details,omitempty"` // omitted when absent; {} is distinct from absent
	PreviousHash  string          `json:"previousHash,omitempty"`
	Hash          string          `json:"hash,omitempty"`
}

// ComputeHash returns the entry's hash: SHA-256 over the canonical form of every field but hash.
func (e Entry) ComputeHash() (string, error) {
	e.Hash = ""
	b, err := jcs.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("audit: canonicalize entry %q: %w", e.ID, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Seal makes e the next entry after an entry with hash prev ("" for the first entry): it sets the
// schema version, the link and the hash.
func Seal(e Entry, prev string) (Entry, error) {
	e.SchemaVersion = SchemaVersion
	e.PreviousHash = prev
	h, err := e.ComputeHash()
	if err != nil {
		return Entry{}, err
	}
	e.Hash = h
	return e, nil
}

// Line returns the sealed entry's canonical JSON, the form lucy stores and exports.
func (e Entry) Line() ([]byte, error) { return jcs.Marshal(e) }

// Report is the outcome of a verification that passed.
type Report struct {
	Entries int    `json:"entries"`
	Head    string `json:"head"` // the last entry's hash; "" for an empty chain
}

// VerifyError says where and why a chain failed verification.
type VerifyError struct {
	Index  int   // the entry's position in the chain, from 1
	Seq    int64 // the event's seq, for lucy's own chain; 0 otherwise
	Reason string
}

func (e *VerifyError) Error() string {
	if e.Seq != 0 {
		return fmt.Sprintf("audit: chain broken at entry %d (event %d): %s", e.Index, e.Seq, e.Reason)
	}
	return fmt.Sprintf("audit: chain broken at entry %d: %s", e.Index, e.Reason)
}

// ErrBroken matches every *VerifyError.
var ErrBroken = errors.New("audit: chain broken")

// Is lets errors.Is(err, ErrBroken) match.
func (e *VerifyError) Is(target error) bool { return target == ErrBroken }

var hexHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Verifier checks a chain one entry at a time.
type Verifier struct {
	n    int
	prev string
}

// Next verifies one entry, given as JSON, against the chain so far. It checks the entry against
// the spec's audit entry schema, recomputes its hash and checks its link to the previous entry.
func (v *Verifier) Next(line []byte) (Entry, error) {
	v.n++
	fail := func(format string, args ...any) (Entry, error) {
		return Entry{}, &VerifyError{Index: v.n, Reason: fmt.Sprintf(format, args...)}
	}
	parsed, err := jcs.Parse(line)
	if err != nil {
		return fail("not I-JSON: %v", err)
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return fail("not a JSON object")
	}
	sv, has := obj["schemaVersion"]
	if !has {
		return fail("no schemaVersion: a legacy entry, hashed by its own SDK's rule; lucy verifies only schemaVersion %q", SchemaVersion)
	}
	if sv != SchemaVersion {
		return fail("schemaVersion %v is not %q", sv, SchemaVersion)
	}
	errs, err := spec.Validate(spec.AuditEntrySchema, schemaValue(obj))
	if err != nil {
		return fail("%v", err)
	}
	if len(errs) > 0 {
		return fail("not a valid audit entry: %s", errs[0])
	}
	claimed := obj["hash"].(string) // the schema requires it
	delete(obj, "hash")
	canon, err := jcs.Encode(obj)
	if err != nil {
		return fail("%v", err)
	}
	sum := sha256.Sum256(canon)
	if got := hex.EncodeToString(sum[:]); got != claimed {
		return fail("hash is %s, the entry hashes to %s: the entry was changed", claimed, got)
	}
	prev, hasPrev := obj["previousHash"].(string)
	switch {
	case v.prev == "" && hasPrev:
		return fail("the first entry has a previousHash; the genesis entry omits it")
	case v.prev != "" && !hasPrev:
		return fail("no previousHash: an entry is missing before this one, or the chain was cut")
	case v.prev != "" && prev != v.prev:
		return fail("previousHash %s does not match the previous entry's hash %s", prev, v.prev)
	}
	v.prev = claimed
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil {
		return fail("%v", err)
	}
	return e, nil
}

// Report returns the verified chain's length and head.
func (v *Verifier) Report() Report { return Report{Entries: v.n, Head: v.prev} }

// MaxLineBytes bounds one entry in a JSONL chain.
const MaxLineBytes = 1 << 20

// VerifyJSONL verifies a chain written one entry per line (JSON Lines), as the spec's samples are.
// Blank lines are skipped.
func VerifyJSONL(r io.Reader) (Report, error) {
	var v Verifier
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if _, err := v.Next(line); err != nil {
			return Report{}, err
		}
	}
	if err := sc.Err(); err != nil {
		return Report{}, fmt.Errorf("audit: read chain: %w", err)
	}
	return v.Report(), nil
}

// schemaValue converts parsed numbers to json.Number, the form the schema validator reads.
func schemaValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = schemaValue(e)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = schemaValue(e)
		}
		return a
	case jcs.Number:
		s, _ := jcs.FormatNumber(float64(x))
		return json.Number(s)
	default:
		return v
	}
}
