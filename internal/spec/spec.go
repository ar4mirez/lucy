// Package spec is lucy's configuration model: ai-sdlc resources (D3b), pinned at one spec commit,
// plus lucy's own kinds.
//
//   - Every resource has the spec's envelope: apiVersion, kind, metadata (name, namespace, labels,
//     annotations), spec and status (LUCY-SPEC-001). lucy requires metadata.namespace, which names
//     the Airspace (org) the resource belongs to (LUCY-SPEC-002).
//   - Config files hold spec. Status is observed state: lucy records it as Conditions in its event
//     store (status.go) and never writes it into a file, so a file that carries status is refused
//     (LUCY-SPEC-003, LUCY-SPEC-007).
//   - Resources are validated on load against JSON Schemas (draft 2020-12) embedded in the binary:
//     the spec's, vendored verbatim at SpecCommit with their SHA-256s in
//     schemas/ai-sdlc/PROVENANCE, and lucy's own under schemas/lucy. Unknown fields are refused,
//     formats are asserted, and schema defaults are applied to what was admitted (LUCY-SPEC-004).
//     No schema is ever fetched over the network.
//   - A resource maps to the internal Resource model by kind, so the spec version a file was
//     written against is a property of the file, not of lucy's code. v1alpha1 has no stability
//     guarantee, so another apiVersion is refused until lucy ships an explicit migration for it
//     (LUCY-SPEC-005).
//   - lucy's kinds (Airspace, Runway, Flight, Pilot so far) live under LucyAPIVersion, never the
//     ai-sdlc.io prefix (LUCY-SPEC-006).
//
// Config is untrusted input: files are size-capped, decoded by a YAML reader that refuses anchors,
// aliases, merge keys and custom tags (decode.go), and walked without following symlinks
// (load.go).
package spec

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// The pinned spec and the API versions lucy reads.
const (
	// SpecCommit is the ai-sdlc commit lucy's vendored schemas come from.
	SpecCommit = "b56cecaeef256644c3066fae2106af5c7eaa684a"
	// CoreAPIVersion is the apiVersion of the spec's core kinds.
	CoreAPIVersion = "ai-sdlc.io/v1alpha1"
	// LucyAPIVersion is the apiVersion of lucy's own kinds.
	LucyAPIVersion = "lucy/v1alpha1"

	coreGroup      = "ai-sdlc.io"
	coreSchemaBase = "https://ai-sdlc.io/schemas/v1alpha1/"
	lucySchemaBase = "https://github.com/ar4mirez/lucy/blob/main/internal/spec/schemas/lucy/v1alpha1/"
)

// AuditEntrySchema is the URL of the spec's audit entry wire schema (spec section 12).
const AuditEntrySchema = coreSchemaBase + "audit-entry.json"

//go:embed schemas/ai-sdlc/v1alpha1/*.json schemas/lucy/v1alpha1/*.json
var schemaFS embed.FS

// Kind describes one resource kind lucy admits.
type Kind struct {
	APIVersion string // the apiVersion files declare
	Name       string // the kind, for example Pipeline
	Schema     string // the URL of its JSON Schema
	Core       bool   // one of the spec's core kinds, rather than lucy's own
}

// kinds is every (apiVersion, kind) lucy admits. A new spec version is added here only together
// with an explicit migration of its resources into the internal model (LUCY-SPEC-005).
var kinds = []Kind{
	{CoreAPIVersion, "Pipeline", coreSchemaBase + "pipeline.schema.json", true},
	{CoreAPIVersion, "AgentRole", coreSchemaBase + "agent-role.schema.json", true},
	{CoreAPIVersion, "QualityGate", coreSchemaBase + "quality-gate.schema.json", true},
	{CoreAPIVersion, "AutonomyPolicy", coreSchemaBase + "autonomy-policy.schema.json", true},
	{CoreAPIVersion, "AdapterBinding", coreSchemaBase + "adapter-binding.schema.json", true},
	{CoreAPIVersion, "WorktreePool", coreSchemaBase + "worktree-pool.schema.json", false},
	{LucyAPIVersion, "Airspace", lucySchemaBase + "airspace.schema.json", false},
	{LucyAPIVersion, "Runway", lucySchemaBase + "runway.schema.json", false},
	{LucyAPIVersion, "Flight", lucySchemaBase + "flight.schema.json", false},
	{LucyAPIVersion, "Pilot", lucySchemaBase + "pilot.schema.json", false},
}

// DecisionSchema is the vendored schema of the spec's Decision resource (RFC-0035): the view of a
// decision projected from the event log. lucy never admits a Decision from config; it projects
// the decisions waiting on the owner into this shape (LUCY-SPEC-013).
const DecisionSchema = coreSchemaBase + "decision.v1.schema.json"

// BacklogTaskSchema is the vendored schema of the spec's BacklogTask, the front matter of a backlog
// task. lucy reads no backlog files; it writes each Flight in this shape for tools that read the
// spec's backlog (LUCY-SPEC-017).
const BacklogTaskSchema = coreSchemaBase + "backlog-task.v1.schema.json"

// Kinds returns the kinds lucy admits, in a stable order.
func Kinds() []Kind { return slices.Clone(kinds) }

// LookupKind returns the kind declared by apiVersion and kind.
func LookupKind(apiVersion, kind string) (Kind, bool) {
	for _, k := range kinds {
		if k.APIVersion == apiVersion && k.Name == kind {
			return k, true
		}
	}
	return Kind{}, false
}

// FieldError is one schema violation: where in the document, and what is wrong.
type FieldError struct {
	Path    string `json:"path"`    // a JSON Pointer into the document; "" is the whole document
	Message string `json:"message"` // what the schema requires
}

func (e FieldError) String() string {
	p := e.Path
	if p == "" {
		p = "/"
	}
	return p + ": " + e.Message
}

// MaxFieldErrors bounds the violations reported for one document.
const MaxFieldErrors = 20

// compiled holds every embedded schema, compiled once.
type compiled struct {
	byURL map[string]*jsonschema.Schema
}

var loadSchemas = sync.OnceValues(compileSchemas)

func compileSchemas() (*compiled, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	c.UseLoader(noFetch{})
	c.UseRegexpEngine(ecmaRegexp)
	var urls []string
	for _, group := range []struct{ dir, base string }{
		{"schemas/ai-sdlc/v1alpha1", coreSchemaBase},
		{"schemas/lucy/v1alpha1", lucySchemaBase},
	} {
		entries, err := fs.ReadDir(schemaFS, group.dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			data, err := schemaFS.ReadFile(path.Join(group.dir, e.Name()))
			if err != nil {
				return nil, err
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
			if err != nil {
				return nil, fmt.Errorf("spec: schema %s: %w", e.Name(), err)
			}
			u := group.base + e.Name()
			if err := c.AddResource(u, doc); err != nil {
				return nil, fmt.Errorf("spec: schema %s: %w", e.Name(), err)
			}
			urls = append(urls, u)
		}
	}
	out := &compiled{byURL: map[string]*jsonschema.Schema{}}
	for _, u := range urls {
		s, err := c.Compile(u)
		if err != nil {
			return nil, fmt.Errorf("spec: compile %s: %w", u, err)
		}
		out.byURL[u] = s
	}
	return out, nil
}

// noFetch refuses every URL: lucy validates only against the schemas embedded in its binary.
type noFetch struct{}

func (noFetch) Load(u string) (any, error) {
	return nil, fmt.Errorf("spec: schema %s is not embedded in lucy, and lucy never fetches schemas", u)
}

// CheckSchemas compiles every embedded schema; it reports a broken build, never bad config.
func CheckSchemas() error {
	_, err := loadSchemas()
	return err
}

func schemaFor(u string) (*jsonschema.Schema, error) {
	cs, err := loadSchemas()
	if err != nil {
		return nil, err
	}
	s, ok := cs.byURL[u]
	if !ok {
		return nil, fmt.Errorf("spec: no embedded schema %s", u)
	}
	return s, nil
}

var printer = message.NewPrinter(language.English)

// Validate checks a decoded document (as Decode or encoding/json with UseNumber returns it)
// against the schema at url. It returns the violations, at most MaxFieldErrors of them, and an
// error only when the schema itself is missing.
func Validate(url string, doc any) ([]FieldError, error) {
	s, err := schemaFor(url)
	if err != nil {
		return nil, err
	}
	err = s.Validate(doc)
	if err == nil {
		return nil, nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []FieldError{{Message: err.Error()}}, nil
	}
	var out []FieldError
	leaves(ve, &out)
	if len(out) == 0 {
		out = append(out, FieldError{Path: pointer(ve.InstanceLocation), Message: ve.ErrorKind.LocalizedString(printer)})
	}
	slices.SortStableFunc(out, func(a, b FieldError) int { return strings.Compare(a.Path, b.Path) })
	out = slices.Compact(out)
	if len(out) > MaxFieldErrors {
		out = append(out[:MaxFieldErrors-1], FieldError{Message: fmt.Sprintf("and %d more violations", len(out)-MaxFieldErrors+1)})
	}
	return out, nil
}

// leaves collects the innermost errors: the ones that say what is actually wrong.
func leaves(ve *jsonschema.ValidationError, out *[]FieldError) {
	if len(ve.Causes) == 0 {
		*out = append(*out, FieldError{Path: pointer(ve.InstanceLocation), Message: ve.ErrorKind.LocalizedString(printer)})
		return
	}
	for _, c := range ve.Causes {
		leaves(c, out)
	}
}

// pointer renders instance location tokens as a JSON Pointer (RFC 6901).
func pointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(t))
	}
	return b.String()
}
