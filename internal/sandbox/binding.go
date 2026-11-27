package sandbox

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ar4mirez/lucy/internal/spec"
)

// Interface is the ai-sdlc AdapterBinding interface a Sandbox provider is bound under.
const Interface = "Sandbox"

// Binding is an admitted ai-sdlc AdapterBinding resource for the Sandbox interface: which
// provider runs an org's containers, which version of its contract, and its configuration. It is
// the provider seam (LUCY-SPEC-016): adding a runtime, a cloud runner say, is a new provider type
// registered in Providers, not a change to the core (LUCY-SBX-002).
type Binding struct {
	Namespace string         // the org the binding belongs to
	Name      string         // the resource's name
	Type      string         // the provider ("berth")
	Version   string         // the provider contract's version (SemVer)
	Config    map[string]any // the provider's own settings; never a credential
}

// BindingFrom reads a Sandbox binding from an admitted AdapterBinding resource.
func BindingFrom(r spec.Resource) (Binding, error) {
	if r.APIVersion != spec.CoreAPIVersion || r.Kind != "AdapterBinding" {
		return Binding{}, fmt.Errorf("sandbox: %s is a %s %s, not an AdapterBinding", r.ID(), r.APIVersion, r.Kind)
	}
	iface, _ := r.Spec["interface"].(string)
	if iface != Interface {
		return Binding{}, fmt.Errorf("sandbox: %s binds the %q interface, not %s", r.ID(), iface, Interface)
	}
	typ, _ := r.Spec["type"].(string)
	version, _ := r.Spec["version"].(string)
	if typ == "" || version == "" {
		return Binding{}, fmt.Errorf("sandbox: %s names no provider type or version", r.ID())
	}
	cfg, _ := r.Spec["config"].(map[string]any)
	if _, ok := r.Spec["config"]; ok && cfg == nil {
		return Binding{}, fmt.Errorf("sandbox: %s has a config that is not a mapping", r.ID())
	}
	return Binding{Namespace: r.Metadata.Namespace, Name: r.Metadata.Name, Type: typ, Version: version, Config: cfg}, nil
}

// Factory opens a provider from its binding. It checks the binding's version against the contract
// versions it implements and refuses configuration it does not know.
type Factory func(Binding) (Provider, error)

// Providers maps a provider type to its factory.
type Providers map[string]Factory

// ErrUnknownProvider is Open's error for a binding whose type no factory is registered for.
var ErrUnknownProvider = errors.New("sandbox: no provider of this type")

// Open returns the provider b binds.
func (p Providers) Open(b Binding) (Provider, error) {
	f, ok := p[b.Type]
	if !ok {
		types := make([]string, 0, len(p))
		for t := range p {
			types = append(types, t)
		}
		slices.Sort(types)
		return nil, fmt.Errorf("%w: %q (known: %s)", ErrUnknownProvider, b.Type, strings.Join(types, ", "))
	}
	return f(b)
}

// CheckVersion refuses a binding version whose major version is not major: a provider implements
// one major version of its contract, and a binding written for another is not guessed at.
func CheckVersion(b Binding, major int) error {
	core, _, _ := strings.Cut(b.Version, "-")
	core, _, _ = strings.Cut(core, "+")
	m, _, ok := strings.Cut(core, ".")
	n, err := strconv.Atoi(m)
	if !ok || err != nil || n != major {
		return fmt.Errorf("sandbox: binding %s/%s is for %s version %q; this lucy implements %d.x", b.Namespace, b.Name, b.Type, b.Version, major)
	}
	return nil
}

// CheckConfig refuses config keys outside allowed.
func CheckConfig(b Binding, allowed ...string) error {
	for k := range b.Config {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("sandbox: binding %s/%s: unknown %s config key %q", b.Namespace, b.Name, b.Type, k)
		}
	}
	return nil
}
