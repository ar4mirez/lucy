package worktree

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ar4mirez/lucy/internal/spec"
)

// An org's worktree pool is declared by an ai-sdlc WorktreePool resource in its namespace
// (LUCY-SPEC-017). lucy accepts only what it acts on: spec.layout, as workspace-scoped (lucy keeps
// every org's slots in its own data directory, apart from the clone), and spec.cleanup.onMerge, as
// true (a slot is given back once its work has landed). Every other field is refused by name, as
// not supported yet. rootDir above all: the pool's root stays under lucy's guarded data directory.
// The members the spec loader fills with their schema default when they are omitted
// (staleThresholdDays 14, basePort 3190, ownershipGuard strict, and in cleanup onAbort and
// onTimeout true) are accepted only at that default, the same as leaving them out, as Pipelines
// treat theirs. An org declares at most one WorktreePool; one that lucy cannot accept holds the org's Flights,
// and lucy never falls back to its defaults for it.

// ErrPoolUnsupported refuses a WorktreePool field lucy does not act on.
var ErrPoolUnsupported = errors.New("worktree: not supported yet")

// ErrPoolInvalid refuses an org's WorktreePool declaration lucy cannot accept.
var ErrPoolInvalid = errors.New("worktree: invalid WorktreePool")

// poolFields are the spec fields lucy accepts, with the one value it acts on.
var poolFields = map[string]any{"layout": "workspace-scoped", "cleanup": nil}

// poolDefaults and cleanupDefaults are the members the loader fills with their schema default;
// each is accepted only at it.
var (
	poolDefaults    = map[string]string{"staleThresholdDays": "14", "basePort": "3190", "ownershipGuard": "strict"}
	cleanupDefaults = map[string]string{"onAbort": "true", "onTimeout": "true"}
)

// ParsePool checks a WorktreePool resource the spec loader admitted against what lucy acts on.
func ParsePool(r spec.Resource) error {
	where := r.Metadata.Namespace + "/" + r.Metadata.Name
	if r.APIVersion != spec.CoreAPIVersion || r.Kind != "WorktreePool" {
		return fmt.Errorf("%w: %s is not a WorktreePool", ErrPoolInvalid, where)
	}
	for _, k := range sortedKeys(r.Spec) {
		v := r.Spec[k]
		if d, ok := poolDefaults[k]; ok && fmt.Sprint(v) == d {
			continue // the loader's default: the same as leaving it out
		}
		want, ok := poolFields[k]
		switch {
		case !ok && k == "rootDir":
			return fmt.Errorf("%w: WorktreePool %s: spec.rootDir: the pool's root stays under lucy's guarded data directory", ErrPoolUnsupported, where)
		case !ok:
			return fmt.Errorf("%w: WorktreePool %s: spec.%s: lucy does not act on it; TODO(LUCY-SPEC-017)", ErrPoolUnsupported, where, k)
		case k == "layout" && v != want:
			return fmt.Errorf("%w: WorktreePool %s: spec.layout %v: lucy keeps every pool workspace-scoped", ErrPoolUnsupported, where, v)
		case k == "cleanup":
			c, _ := v.(map[string]any)
			for _, ck := range sortedKeys(c) {
				if d, ok := cleanupDefaults[ck]; ok && fmt.Sprint(c[ck]) == d {
					continue
				}
				if ck != "onMerge" {
					return fmt.Errorf("%w: WorktreePool %s: spec.cleanup.%s: lucy does not act on it; TODO(LUCY-SPEC-017)", ErrPoolUnsupported, where, ck)
				}
				if c[ck] != true {
					return fmt.Errorf("%w: WorktreePool %s: spec.cleanup.onMerge false: lucy always gives a slot back once its work has landed", ErrPoolUnsupported, where)
				}
			}
		}
	}
	return nil
}

// PoolErrors returns, by org, why the org's WorktreePool declaration cannot be accepted: a
// WorktreePool the spec loader refused (its Valid condition says why), one ParsePool refuses, or
// more than one in the org. An org with none, or with one lucy accepts, is absent.
func PoolErrors(results []spec.Result) map[string]error {
	out := map[string]error{}
	seen := map[string]string{}
	for _, res := range results {
		if res.APIVersion != spec.CoreAPIVersion || res.Kind != "WorktreePool" {
			continue
		}
		org, where := res.Namespace, res.Namespace+"/"+res.Name
		if first, ok := seen[org]; ok {
			out[org] = fmt.Errorf("%w: org %s declares more than one WorktreePool (%s, %s)", ErrPoolInvalid, org, first, where)
			continue
		}
		seen[org] = where
		if res.Resource == nil {
			c, _ := res.Condition(spec.ConditionValid)
			out[org] = fmt.Errorf("%w: WorktreePool %s was not admitted (%s): %s", ErrPoolInvalid, where, c.Reason, c.Message)
			continue
		}
		if err := ParsePool(*res.Resource); err != nil {
			out[org] = err
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
