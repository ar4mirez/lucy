// Package pipeline is how lucy flies a Flight, as ai-sdlc Pipeline resources (LUCY-SPEC-014): the
// ship Pipeline (a pull request) and the research Pipeline (a report). lucy bundles one of each
// (builtin/, in the reserved namespace lucy); an org replaces one with a Pipeline of the same name
// in its namespace. Parse reads a Pipeline's FailurePolicy and ApprovalPolicy into a Policy.
//
// Parse accepts only these fields of a Pipeline's spec:
//   - triggers and providers, with any content: required by the format, not acted on yet;
//   - stages, each with name (pilot, and for ship also merge), agent (pilot only), onFailure
//     (pilot only: strategy abort or retry, and maxRetries for retry) and approval (merge only:
//     required, blocking true, and timeout with onTimeout abort).
//
// The members the spec loader fills with their schema default (spec.artifactSchemaVersion,
// defaultModel and defaultHarness; a stage's schedule and databaseAccess) are accepted only at that
// default, the same as leaving them out. Every other field, anywhere in the spec, is refused with ErrUnsupported naming its path, so a
// Pipeline never carries a setting lucy does not act on. An approval timeout that escalates (or
// names no onTimeout) is refused the same way: the Tower acts only on abort. Above all, a timeout
// never approves a merge: an ApprovalPolicy whose onTimeout is auto-approve is refused with
// ErrAutoApprove, on any stage.
//
// lucy flies two stages: pilot (the Pilot's session) and, for ship, merge (the Tower's merge).
// The spec marks spec.stages deprecated in favour of a Workflow document; lucy reads its policies
// from the stages until that document is vendored. TODO(LUCY-SPEC-014): read them from the
// Workflow document once the spec's schema for it is vendored.
package pipeline

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/ar4mirez/lucy/internal/spec"
)

// The Pipelines lucy flies, by name: a Flight's shape names its Pipeline.
const (
	Ship     = "ship"
	Research = "research"
)

// Names are the Pipelines lucy flies, sorted.
var Names = []string{Research, Ship}

// Namespace holds lucy's built-in Pipelines; no org may take it.
const Namespace = "lucy"

// Stages lucy flies.
const (
	StagePilot = "pilot" // the Pilot's session: its failure policy
	StageMerge = "merge" // the Tower's merge: its approval policy (ship only)
)

// Failure strategies lucy supports, and the approval timeout actions.
const (
	StrategyAbort  = "abort" // a failed Pilot grounds the flight
	StrategyRetry  = "retry" // a lost Pilot is replaced, up to maxRetries times
	OnTimeoutAbort = "abort" // a merge held past its timeout grounds the flight
)

// ErrAutoApprove refuses an ApprovalPolicy that would approve on a timeout.
var ErrAutoApprove = errors.New("pipeline: a timeout never approves a merge: onTimeout auto-approve is refused")

// ErrInvalid refuses a Pipeline lucy cannot fly.
var ErrInvalid = errors.New("pipeline: invalid")

// ErrUnsupported refuses a Pipeline setting lucy does not act on yet, so a Pipeline never carries
// a setting that silently does nothing.
var ErrUnsupported = errors.New("pipeline: not supported yet")

// Failure is a stage's FailurePolicy.
type Failure struct {
	Strategy   string // abort or retry
	MaxRetries int    // for retry: how many lost Pilot sessions the Tower replaces; 0 for abort
}

// Approval is the merge stage's ApprovalPolicy.
type Approval struct {
	Required  bool          // the owner approves every merge, whatever the autonomy posture
	Timeout   time.Duration // how long a held merge waits; 0 for no limit
	OnTimeout string        // abort with a timeout, else empty; never an approval
}

// Policy is one Pipeline, read.
type Policy struct {
	Name, Namespace string
	Pilot           Failure
	Merge           Approval // ship only
}

//go:embed builtin/*.yaml
var builtinFS embed.FS

// Builtin returns lucy's built-in Pipelines, by name, admitted through the spec loader and read.
func Builtin() (map[string]Policy, error) {
	out := map[string]Policy{}
	for _, name := range Names {
		data, err := builtinFS.ReadFile("builtin/" + name + ".yaml")
		if err != nil {
			return nil, err
		}
		results := spec.LoadFile(name+".yaml", data)
		if len(results) != 1 || results[0].Resource == nil {
			return nil, fmt.Errorf("pipeline: the built-in %s Pipeline does not load: %+v", name, results)
		}
		p, err := Parse(*results[0].Resource)
		if err != nil {
			return nil, err
		}
		out[name] = p
	}
	return out, nil
}

// Parse reads a Pipeline named ship or research, as the spec loader admitted it. It refuses with
// ErrUnsupported every field outside the accepted ones (see the package doc), and with ErrInvalid
// what lucy does not fly: a stage other than pilot or merge (merge only for ship), a missing pilot
// stage, a failure strategy other than abort or retry, an approval anywhere but the merge stage, a
// merge that does not block on its approval, or onTimeout without a timeout.
func Parse(r spec.Resource) (Policy, error) {
	refuse := func(kind error, format string, a ...any) error {
		return fmt.Errorf("%w: Pipeline %s/%s: %s", kind, r.Metadata.Namespace, r.Metadata.Name, fmt.Sprintf(format, a...))
	}
	bad := func(format string, a ...any) (Policy, error) {
		return Policy{}, refuse(ErrInvalid, format, a...)
	}
	if r.APIVersion != spec.CoreAPIVersion || r.Kind != "Pipeline" {
		return bad("not a Pipeline")
	}
	name := r.Metadata.Name
	if !slices.Contains(Names, name) {
		return bad("lucy flies only the Pipelines %v", Names)
	}
	if err := only(refuse, r.Spec, "spec", specDefaults, "triggers", "providers", "stages"); err != nil {
		return Policy{}, err
	}
	p := Policy{Name: name, Namespace: r.Metadata.Namespace, Pilot: Failure{Strategy: StrategyAbort}}
	stages, _ := r.Spec["stages"].([]any)
	seen := map[string]bool{}
	for i, s := range stages {
		path := fmt.Sprintf("spec.stages[%d]", i)
		st, _ := s.(map[string]any)
		sname, _ := st["name"].(string)
		if a, ok := st["approval"].(map[string]any); ok {
			if t, _ := a["onTimeout"].(string); t == "auto-approve" {
				return Policy{}, fmt.Errorf("%w (Pipeline %s/%s, stage %s)", ErrAutoApprove, r.Metadata.Namespace, name, sname)
			}
		}
		switch {
		case sname == StagePilot, sname == StageMerge && name == Ship:
		default:
			return bad("stage %d is %q; a %s Pipeline has the stages %v", i, sname, name, stagesOf(name))
		}
		if seen[sname] {
			return bad("stage %q appears twice", sname)
		}
		seen[sname] = true
		if err := only(refuse, st, path, stageDefaults, "name", "agent", "onFailure", "approval"); err != nil {
			return Policy{}, err
		}
		if sname == StageMerge {
			if _, ok := st["agent"]; ok {
				return bad("the merge stage names an agent; the Tower merges, no agent does")
			}
			if _, ok := st["onFailure"]; ok {
				return bad("the merge stage has an onFailure; a failed merge is retried by the Tower, then held for the owner")
			}
			if a, ok := st["approval"].(map[string]any); ok {
				ap, err := approval(refuse, a, path+".approval")
				if err != nil {
					return Policy{}, err
				}
				p.Merge = ap
			}
			continue
		}
		want := map[string]string{Ship: "worker", Research: "researcher"}[name]
		if ag, ok := st["agent"].(string); ok && ag != want {
			return bad("the pilot stage's agent is %q; a %s Pilot is a %s", ag, name, want)
		}
		if _, ok := st["approval"]; ok {
			return bad("the pilot stage has an approval; lucy does not hold a Pilot's start for the owner yet")
		}
		if f, ok := st["onFailure"].(map[string]any); ok {
			fp, err := failure(refuse, f, path+".onFailure")
			if err != nil {
				return Policy{}, err
			}
			p.Pilot = fp
		}
	}
	if !seen[StagePilot] {
		return bad("it has no pilot stage")
	}
	return p, nil
}

type refuser func(kind error, format string, a ...any) error

// The members the spec loader fills with their schema default when they are omitted. Parse accepts
// each only at that default, which is the same as leaving it out.
var (
	specDefaults  = map[string]any{"artifactSchemaVersion": "v1", "defaultModel": "sonnet", "defaultHarness": "claude-code"}
	stageDefaults = map[string]any{"schedule": "now", "databaseAccess": "none"}
)

// only refuses, with ErrUnsupported, the first field of obj (at path) that is neither allowed nor
// at the default the loader fills in.
func only(refuse refuser, obj map[string]any, path string, defaults map[string]any, allowed ...string) error {
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if d, ok := defaults[k]; ok && obj[k] == d {
			continue
		}
		if !slices.Contains(allowed, k) {
			return refuse(ErrUnsupported, "%s.%s: lucy does not act on it yet; TODO(LUCY-SPEC-014) wire it", path, k)
		}
	}
	return nil
}

func stagesOf(name string) []string {
	if name == Ship {
		return []string{StagePilot, StageMerge}
	}
	return []string{StagePilot}
}

func failure(refuse refuser, f map[string]any, path string) (Failure, error) {
	if err := only(refuse, f, path, nil, "strategy", "maxRetries"); err != nil {
		return Failure{}, err
	}
	s, _ := f["strategy"].(string)
	switch s {
	case StrategyAbort:
		if _, ok := f["maxRetries"]; ok {
			return Failure{}, refuse(ErrInvalid, "%s: maxRetries with strategy abort; abort replaces no Pilot", path)
		}
		return Failure{Strategy: s}, nil
	case StrategyRetry:
		num, _ := f["maxRetries"].(json.Number)
		n64, err := num.Int64()
		n := int(n64)
		if err != nil || n < 1 || n > 10 {
			return Failure{}, refuse(ErrInvalid, "%s: retry needs maxRetries from 1 to 10", path)
		}
		return Failure{Strategy: s, MaxRetries: n}, nil
	}
	return Failure{}, refuse(ErrInvalid, "%s: strategy %q is not one lucy flies (abort or retry)", path, s)
}

func approval(refuse refuser, a map[string]any, path string) (Approval, error) {
	if err := only(refuse, a, path, nil, "required", "blocking", "timeout", "onTimeout"); err != nil {
		return Approval{}, err
	}
	var out Approval
	out.Required, _ = a["required"].(bool)
	if b, ok := a["blocking"].(bool); ok && !b {
		return Approval{}, refuse(ErrInvalid, "%s: blocking is false; a merge always waits for its approval", path)
	}
	if t, ok := a["timeout"].(string); ok {
		d, err := Duration(t)
		if err != nil || d <= 0 {
			return Approval{}, refuse(ErrInvalid, "%s: timeout %q is not a positive ISO 8601 duration", path, t)
		}
		out.Timeout = d
	}
	t, _ := a["onTimeout"].(string)
	switch {
	case t != "" && out.Timeout == 0:
		return Approval{}, refuse(ErrInvalid, "%s: onTimeout %q without a timeout", path, t)
	case out.Timeout == 0:
	case t == OnTimeoutAbort:
		out.OnTimeout = t
	case t == "" || t == "escalate":
		return Approval{}, refuse(ErrUnsupported, "%s.timeout: the Tower does not act on an escalating timeout, only on onTimeout abort; TODO(LUCY-SPEC-014) wire it", path)
	default:
		return Approval{}, refuse(ErrInvalid, "%s: onTimeout %q is not abort", path, t)
	}
	return out, nil
}

var durationRE = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// Duration reads an ISO 8601 duration of days, hours, minutes and seconds (P1DT2H30M, PT10M).
// Years, months and weeks are refused: their length is not fixed.
func Duration(s string) (time.Duration, error) {
	m := durationRE.FindStringSubmatch(s)
	if m == nil || s == "P" || s == "PT" || (len(s) > 0 && s[len(s)-1] == 'T') {
		return 0, fmt.Errorf("%q is not an ISO 8601 duration of days, hours, minutes and seconds", s)
	}
	var total time.Duration
	for i, unit := range []time.Duration{24 * time.Hour, time.Hour, time.Minute, time.Second} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.ParseInt(m[i+1], 10, 64)
		if err != nil || n > int64(1<<62)/int64(unit) {
			return 0, fmt.Errorf("%q is too long", s)
		}
		total += time.Duration(n) * unit
		if total < 0 {
			return 0, fmt.Errorf("%q is too long", s)
		}
	}
	return total, nil
}

// Catalog holds each org's Pipelines: the built-in ones, or the org's own of the same name.
type Catalog struct {
	builtin map[string]Policy
	orgs    map[string]map[string]Policy // org -> name -> the org's Pipeline
}

// NewCatalog returns a catalog of the built-in Pipelines and the Pipelines in rs named ship or
// research, by namespace (the org); one in the namespace lucy is ignored. An org Pipeline lucy
// cannot fly is an error naming it, as other admission failures are.
func NewCatalog(rs []spec.Resource) (*Catalog, error) {
	b, err := Builtin()
	if err != nil {
		return nil, err
	}
	c := &Catalog{builtin: b, orgs: map[string]map[string]Policy{}}
	for _, r := range rs {
		if r.APIVersion != spec.CoreAPIVersion || r.Kind != "Pipeline" || r.Metadata.Namespace == Namespace || !slices.Contains(Names, r.Metadata.Name) {
			continue
		}
		p, err := Parse(r)
		if err != nil {
			return nil, err
		}
		if c.orgs[r.Metadata.Namespace] == nil {
			c.orgs[r.Metadata.Namespace] = map[string]Policy{}
		}
		c.orgs[r.Metadata.Namespace][r.Metadata.Name] = p
	}
	return c, nil
}

// For returns org's Pipeline named name: its own, else the built-in one.
func (c *Catalog) For(org, name string) (Policy, bool) {
	if p, ok := c.orgs[org][name]; ok {
		return p, true
	}
	p, ok := c.builtin[name]
	return p, ok
}
