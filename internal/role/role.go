// Package role is lucy's agent roles as ai-sdlc AgentRole resources (LUCY-SPEC-015): the
// supervisor, the ship worker, the researcher and the reviewer. lucy bundles one AgentRole for
// each (builtin/, in the reserved namespace lucy); an org adds to its own by an AgentRole of the
// same name in its namespace. What an org's role blocks (constraints.blockedPaths) is the
// built-in role's list and the org's together, and it feeds the policy engine, which refuses a
// Pilot's write or delete of a blocked path.
package role

import (
	"embed"
	"fmt"
	"slices"
	"sort"

	"github.com/ar4mirez/lucy/internal/spec"
)

// Names are lucy's role ids, as the policy engine and the AgentRoles name them.
var Names = []string{"supervisor", "worker", "researcher", "reviewer"}

// Namespace holds lucy's built-in AgentRoles; no org may take it.
const Namespace = "lucy"

//go:embed builtin/*.yaml
var builtinFS embed.FS

// Builtin returns lucy's built-in AgentRoles, by name, admitted through the spec loader.
func Builtin() (map[string]spec.Resource, error) {
	out := map[string]spec.Resource{}
	for _, name := range Names {
		data, err := builtinFS.ReadFile("builtin/" + name + ".yaml")
		if err != nil {
			return nil, err
		}
		results := spec.LoadFile(name+".yaml", data)
		if len(results) != 1 || results[0].Resource == nil {
			return nil, fmt.Errorf("role: the built-in %s AgentRole does not load: %+v", name, results)
		}
		out[name] = *results[0].Resource
	}
	return out, nil
}

// Catalog holds each org's roles: the built-in AgentRoles and the org's own.
type Catalog struct {
	builtin map[string]spec.Resource
	orgs    map[string]map[string]spec.Resource // org -> role name -> the org's AgentRole
}

// NewCatalog returns a catalog of the built-in roles and the AgentRoles in rs whose name is a role
// id, by their namespace (the org). An AgentRole in the namespace lucy is ignored: that one is
// lucy's own.
func NewCatalog(rs []spec.Resource) (*Catalog, error) {
	b, err := Builtin()
	if err != nil {
		return nil, err
	}
	c := &Catalog{builtin: b, orgs: map[string]map[string]spec.Resource{}}
	for _, r := range rs {
		if r.APIVersion != spec.CoreAPIVersion || r.Kind != "AgentRole" || r.Metadata.Namespace == Namespace || !slices.Contains(Names, r.Metadata.Name) {
			continue
		}
		if c.orgs[r.Metadata.Namespace] == nil {
			c.orgs[r.Metadata.Namespace] = map[string]spec.Resource{}
		}
		c.orgs[r.Metadata.Namespace][r.Metadata.Name] = r
	}
	return c, nil
}

// BlockedPaths returns the globs org's role blocks: the built-in role's and the org's AgentRole's,
// sorted and without repeats.
func (c *Catalog) BlockedPaths(org, name string) []string {
	var out []string
	for _, r := range []spec.Resource{c.builtin[name], c.orgs[org][name]} {
		cons, _ := r.Spec["constraints"].(map[string]any)
		list, _ := cons["blockedPaths"].([]any)
		for _, g := range list {
			if s, ok := g.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}
