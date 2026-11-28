package role

import (
	"slices"
	"testing"

	"github.com/ar4mirez/lucy/internal/spec"
)

// Every lucy role is a valid spec AgentRole (LUCY-SPEC-015): the loader admits each built-in one
// against the vendored agent-role schema.
func TestBuiltinRolesAreSpecAgentRoles(t *testing.T) {
	b, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != len(Names) {
		t.Fatalf("built-in roles %v, want %v", b, Names)
	}
	for _, n := range Names {
		r := b[n]
		if r.APIVersion != spec.CoreAPIVersion || r.Kind != "AgentRole" || r.Metadata.Name != n || r.Metadata.Namespace != Namespace {
			t.Errorf("built-in %s: %+v", n, r)
		}
	}
}

func agentRole(ns, name string, blocked ...string) spec.Resource {
	list := make([]any, 0, len(blocked))
	for _, b := range blocked {
		list = append(list, b)
	}
	return spec.Resource{APIVersion: spec.CoreAPIVersion, Kind: "AgentRole", Metadata: spec.Metadata{Name: name, Namespace: ns},
		Spec: map[string]any{"role": name, "goal": "g", "tools": []any{"read"}, "constraints": map[string]any{"blockedPaths": list}}}
}

// An org's AgentRole of a role's name adds its blockedPaths to that org's role only; one in the
// reserved namespace, or of another name, changes nothing.
func TestOrgRolesAddBlockedPaths(t *testing.T) {
	c, err := NewCatalog([]spec.Resource{
		agentRole("acme", "worker", ".github/workflows/**", "**/secrets/**", ".github/workflows/**"),
		agentRole("acme", "code-agent", "everything/**"),
		agentRole(Namespace, "worker", "lucy-override/**"),
		agentRole("beta", "reviewer", "docs/**"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.BlockedPaths("acme", "worker"); !slices.Equal(got, []string{"**/secrets/**", ".github/workflows/**"}) {
		t.Errorf("acme's worker blocks %v", got)
	}
	if got := c.BlockedPaths("beta", "worker"); len(got) != 0 {
		t.Errorf("beta's worker blocks %v; another org's AgentRole leaked", got)
	}
	if got := c.BlockedPaths("beta", "reviewer"); !slices.Equal(got, []string{"docs/**"}) {
		t.Errorf("beta's reviewer blocks %v", got)
	}
}
