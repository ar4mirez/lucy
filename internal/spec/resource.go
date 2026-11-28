package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ar4mirez/lucy/internal/jcs"
)

// ConditionStatus is a condition's status: True, False or Unknown (spec section 6).
type ConditionStatus string

// Condition statuses.
const (
	True    ConditionStatus = "True"
	False   ConditionStatus = "False"
	Unknown ConditionStatus = "Unknown"
)

// Condition is one aspect of a resource's observed state, in the spec's shape (spec section 6). Its
// JSON names are the spec's, so a condition reads the same in lucy's events, its projection and the
// spec's tooling; the API renames lastTransitionTime to last_transition_time under its snake_case
// convention.
type Condition struct {
	Type               string          `json:"type"`
	Status             ConditionStatus `json:"status"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
	LastTransitionTime string          `json:"lastTransitionTime,omitempty"`
}

// ConditionValid is the condition loading reports: whether the resource was admitted.
const ConditionValid = "Valid"

// Reasons a Valid condition gives. They are stable: tools and tests match on them.
const (
	ReasonAdmitted              = "Admitted"
	ReasonUnreadable            = "Unreadable"
	ReasonSymlink               = "Symlink"
	ReasonTooLarge              = "TooLarge"
	ReasonDecodeError           = "DecodeError"
	ReasonNotAnObject           = "NotAnObject"
	ReasonInvalidEnvelope       = "InvalidEnvelope"
	ReasonUnsupportedAPIVersion = "UnsupportedAPIVersion"
	ReasonUnknownKind           = "UnknownKind"
	ReasonStatusInConfig        = "StatusInConfig"
	ReasonMissingNamespace      = "MissingNamespace"
	ReasonSchemaViolation       = "SchemaViolation"
	ReasonNamespaceMismatch     = "NamespaceMismatch"
	ReasonDuplicate             = "Duplicate"
)

// Source is where a document came from: a file, relative to the loaded directory with forward
// slashes, and the document's position in it, from 1. Doc is 0 for a problem with the whole file.
type Source struct {
	File string `json:"file"`
	Doc  int    `json:"doc,omitempty"`
}

func (s Source) String() string {
	if s.Doc == 0 {
		return s.File
	}
	return fmt.Sprintf("%s#%d", s.File, s.Doc)
}

// Metadata is a resource's identifying information (spec section 3).
type Metadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Resource is lucy's internal model of one admitted resource: validated, with schema defaults
// applied, and without status, which lives in the event store.
type Resource struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   Metadata       `json:"metadata"`
	Spec       map[string]any `json:"spec"`
	Source     Source         `json:"-"`
}

// ID is the resource's identity: namespace, kind and name (spec section 3).
func (r Resource) ID() string { return r.Metadata.Namespace + "/" + r.Kind + "/" + r.Metadata.Name }

// SpecSHA256 is the SHA-256 of the spec's RFC 8785 canonical form, defaults included: it changes
// exactly when the desired state does, however the file is formatted.
func (r Resource) SpecSHA256() (string, error) {
	b, err := jcs.Marshal(r.Spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Result is the outcome of loading one document, or of a file no document could be read from.
type Result struct {
	Source     Source       `json:"source"`
	APIVersion string       `json:"apiVersion,omitempty"` // as declared, when readable
	Kind       string       `json:"kind,omitempty"`
	Namespace  string       `json:"namespace,omitempty"`
	Name       string       `json:"name,omitempty"`
	Conditions []Condition  `json:"conditions"`
	Errors     []FieldError `json:"errors,omitempty"` // the violations behind a False condition
	Resource   *Resource    `json:"-"`                // set when admitted
}

// Valid reports whether the document was admitted.
func (r Result) Valid() bool { return r.Resource != nil }

// Condition returns the result's condition of the given type.
func (r Result) Condition(typ string) (Condition, bool) {
	for _, c := range r.Conditions {
		if c.Type == typ {
			return c, true
		}
	}
	return Condition{}, false
}

func (r *Result) refuse(reason, message string, errs ...FieldError) {
	r.Resource = nil
	r.Errors = errs
	r.Conditions = []Condition{{Type: ConditionValid, Status: False, Reason: reason, Message: message}}
}

// Admit validates one decoded document and, when it passes, returns it as a Resource. Every
// document lucy loads goes through it: the CLI's `lucy config validate` and the daemon alike.
func Admit(doc any, src Source) Result {
	r := Result{Source: src}
	obj, ok := doc.(map[string]any)
	if !ok {
		r.refuse(ReasonNotAnObject, "a resource is a mapping with apiVersion, kind, metadata and spec")
		return r
	}
	r.APIVersion, _ = obj["apiVersion"].(string)
	r.Kind, _ = obj["kind"].(string)
	md, _ := obj["metadata"].(map[string]any)
	if md != nil {
		r.Name, _ = md["name"].(string)
		r.Namespace, _ = md["namespace"].(string)
	}
	if r.APIVersion == "" || r.Kind == "" {
		r.refuse(ReasonInvalidEnvelope, "apiVersion and kind are required strings")
		return r
	}
	k, ok := LookupKind(r.APIVersion, r.Kind)
	if !ok {
		reason, msg := unknownKind(r.APIVersion, r.Kind)
		r.refuse(reason, msg)
		return r
	}
	if _, has := obj["status"]; has {
		r.refuse(ReasonStatusInConfig, "status is observed state: lucy records it in its database and never reads it from config; remove status from the file")
		return r
	}
	if md == nil {
		r.refuse(ReasonInvalidEnvelope, "metadata is a required mapping with name and namespace")
		return r
	}
	if _, has := md["namespace"]; !has {
		r.refuse(ReasonMissingNamespace, "metadata.namespace is required: it names the airspace (org) the resource belongs to")
		return r
	}
	errs, err := Validate(k.Schema, obj)
	if err != nil {
		r.refuse(ReasonSchemaViolation, err.Error())
		return r
	}
	if len(errs) > 0 {
		r.refuse(ReasonSchemaViolation, schemaMessage(errs), errs...)
		return r
	}
	if r.Kind == "Airspace" && r.Namespace != r.Name {
		r.refuse(ReasonNamespaceMismatch, fmt.Sprintf("an Airspace's namespace is its own name: set metadata.namespace to %q", r.Name))
		return r
	}
	if r.Kind == "Airspace" {
		if msg := supervisorRules(obj); msg != "" {
			r.refuse(ReasonSchemaViolation, msg)
			return r
		}
	}
	s, err := schemaFor(k.Schema)
	if err != nil {
		r.refuse(ReasonSchemaViolation, err.Error())
		return r
	}
	applyDefaults(s, obj)
	if errs, err := Validate(k.Schema, obj); err != nil || len(errs) > 0 {
		// A vendored default the schema itself refuses: a broken pin, reported rather than admitted.
		r.refuse(ReasonSchemaViolation, "the schema's own defaults do not validate: "+schemaMessage(errs), errs...)
		return r
	}
	res := &Resource{
		APIVersion: r.APIVersion,
		Kind:       r.Kind,
		Metadata: Metadata{
			Name:        r.Name,
			Namespace:   r.Namespace,
			Labels:      stringMap(md["labels"]),
			Annotations: stringMap(md["annotations"]),
		},
		Spec:   obj["spec"].(map[string]any), // the schema requires an object
		Source: src,
	}
	r.Resource = res
	r.Conditions = []Condition{{Type: ConditionValid, Status: True, Reason: ReasonAdmitted, Message: "valid against the " + k.APIVersion + " " + k.Name + " schema"}}
	return r
}

func unknownKind(apiVersion, kind string) (string, string) {
	known := false
	var names []string
	for _, k := range kinds {
		if k.APIVersion == apiVersion {
			known = true
			names = append(names, k.Name)
		}
	}
	switch {
	case known:
		return ReasonUnknownKind, fmt.Sprintf("lucy admits no kind %q under %s; it admits %s", kind, apiVersion, strings.Join(names, ", "))
	case strings.HasPrefix(apiVersion, coreGroup+"/"):
		return ReasonUnsupportedAPIVersion, fmt.Sprintf("lucy reads %s (ai-sdlc spec commit %.12s); %s has no stability guarantee to rely on, so lucy needs an explicit migration for it first",
			CoreAPIVersion, SpecCommit, apiVersion)
	default:
		return ReasonUnsupportedAPIVersion, fmt.Sprintf("unknown apiVersion %q; lucy reads %s and %s", apiVersion, CoreAPIVersion, LucyAPIVersion)
	}
}

func schemaMessage(errs []FieldError) string {
	if len(errs) == 0 {
		return "schema violation"
	}
	if len(errs) == 1 {
		return errs[0].String()
	}
	return fmt.Sprintf("%s (and %d more)", errs[0].String(), len(errs)-1)
}

func stringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, e := range m {
		out[k], _ = e.(string) // the schema requires strings
	}
	return out
}

// sortResults orders results by source, so output and records do not depend on map order.
func sortResults(rs []Result) {
	slices.SortStableFunc(rs, func(a, b Result) int {
		if c := strings.Compare(a.Source.File, b.Source.File); c != 0 {
			return c
		}
		return a.Source.Doc - b.Source.Doc
	})
}

// supervisorRules checks an Airspace's spec.supervisor.rules, which the schema cannot: each match
// must be an RE2 regular expression. It returns why one is refused, or "".
func supervisorRules(obj map[string]any) string {
	spec, _ := obj["spec"].(map[string]any)
	sup, _ := spec["supervisor"].(map[string]any)
	rules, _ := sup["rules"].([]any)
	for i, r := range rules {
		m, _ := r.(map[string]any)
		match, _ := m["match"].(string)
		if _, err := regexp.Compile("(?i)" + match); err != nil {
			return fmt.Sprintf("spec.supervisor.rules[%d].match is not an RE2 regular expression: %v", i, err)
		}
	}
	return ""
}
