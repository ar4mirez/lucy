package gateway

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ar4mirez/lucy/internal/spec"
)

// An org's gateway is configured by an ai-sdlc AdapterBinding (LUCY-SPEC-016): the gateway is the
// SecretStore binding, of type lucy-gateway, and its config lists the routes.
//
//	apiVersion: ai-sdlc.io/v1alpha1
//	kind: AdapterBinding
//	metadata: { name: gateway, namespace: acme }
//	spec:
//	  interface: SecretStore
//	  type: lucy-gateway
//	  version: 1.0.0
//	  config:
//	    routes:
//	      - name: github
//	        upstream: https://api.github.com
//	        credential: github
//	        scope: repo:acme/web
//	        prefix: "Bearer "
//	        allow:
//	          - { methods: [GET], path: /repos/acme/web }
//
// The config holds no credential, only its name; credentials live in the org's vault.
const (
	Interface    = "SecretStore"
	BindingType  = "lucy-gateway"
	ConfigMajor  = 1 // the binding versions this lucy reads: 1.x
	maxRoutes    = 64
	maxRules     = 64
	maxRuleVerbs = 9
)

// BindingFrom reads one org's gateway routes from an admitted AdapterBinding resource. It
// returns ok false for a binding of another interface or type, and an error for a gateway binding
// it cannot read: an unknown or misspelt key, a value of the wrong type, a route that does not
// check. Nothing in a refused binding is used.
func BindingFrom(r spec.Resource) (org string, routes []Route, ok bool, err error) {
	if r.APIVersion != spec.CoreAPIVersion || r.Kind != "AdapterBinding" {
		return "", nil, false, nil
	}
	if iface, _ := r.Spec["interface"].(string); iface != Interface {
		return "", nil, false, nil
	}
	if typ, _ := r.Spec["type"].(string); typ != BindingType {
		return "", nil, false, nil
	}
	org = r.Metadata.Namespace
	fail := func(format string, a ...any) (string, []Route, bool, error) {
		return "", nil, true, fmt.Errorf("gateway: binding %s: "+format, append([]any{r.ID()}, a...)...)
	}
	version, _ := r.Spec["version"].(string)
	if m, _, _ := strings.Cut(version, "."); m != fmt.Sprint(ConfigMajor) {
		return fail("version %q; this lucy reads %d.x", version, ConfigMajor)
	}
	cfg, _ := r.Spec["config"].(map[string]any)
	if cfg == nil {
		return fail("has no config")
	}
	if err := onlyKeys(cfg, "routes"); err != nil {
		return fail("config: %v", err)
	}
	list, ok := cfg["routes"].([]any)
	if !ok || len(list) == 0 || len(list) > maxRoutes {
		return fail("config.routes must list 1 to %d routes", maxRoutes)
	}
	for i, item := range list {
		rt, err := routeFrom(item)
		if err != nil {
			return fail("config.routes[%d]: %v", i, err)
		}
		if _, err := checkRoute(rt); err != nil {
			return fail("config.routes[%d]: %v", i, err)
		}
		if slices.ContainsFunc(routes, func(o Route) bool { return o.Name == rt.Name }) {
			return fail("config.routes[%d]: route %q is defined twice", i, rt.Name)
		}
		routes = append(routes, rt)
	}
	return org, routes, true, nil
}

func routeFrom(item any) (Route, error) {
	m, ok := item.(map[string]any)
	if !ok {
		return Route{}, fmt.Errorf("not a mapping")
	}
	if err := onlyKeys(m, "name", "upstream", "credential", "scope", "header", "prefix", "allow"); err != nil {
		return Route{}, err
	}
	var r Route
	for key, dst := range map[string]*string{"name": &r.Name, "upstream": &r.Upstream, "credential": &r.Credential,
		"scope": &r.Scope, "header": &r.Header, "prefix": &r.Prefix} {
		v, present := m[key]
		if !present {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return Route{}, fmt.Errorf("%s is not a string", key)
		}
		*dst = s
	}
	rules, ok := m["allow"].([]any)
	if !ok || len(rules) == 0 || len(rules) > maxRules {
		return Route{}, fmt.Errorf("allow must list 1 to %d rules", maxRules)
	}
	for j, ri := range rules {
		rm, ok := ri.(map[string]any)
		if !ok {
			return Route{}, fmt.Errorf("allow[%d] is not a mapping", j)
		}
		if err := onlyKeys(rm, "methods", "path"); err != nil {
			return Route{}, fmt.Errorf("allow[%d]: %v", j, err)
		}
		var rule Rule
		rule.Path, _ = rm["path"].(string)
		verbs, ok := rm["methods"].([]any)
		if !ok || len(verbs) == 0 || len(verbs) > maxRuleVerbs {
			return Route{}, fmt.Errorf("allow[%d].methods must list 1 to %d methods", j, maxRuleVerbs)
		}
		for _, v := range verbs {
			s, _ := v.(string)
			if !slices.Contains(Methods, s) {
				return Route{}, fmt.Errorf("allow[%d].methods: %q is not an HTTP method lucy forwards", j, v)
			}
			rule.Methods = append(rule.Methods, s)
		}
		r.Allow = append(r.Allow, rule)
	}
	return r, nil
}

func onlyKeys(m map[string]any, allowed ...string) error {
	for k := range m {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("unknown key %q", k)
		}
	}
	return nil
}

// Bindings reads every org's gateway routes from admitted resources. An org with two gateway
// bindings, or a binding that does not read, is reported and left out whole: its gateway has no
// routes, so it forwards nothing (fail closed).
func Bindings(rs []spec.Resource) (map[string][]Route, []error) {
	out := map[string][]Route{}
	seen := map[string]bool{}
	var errs []error
	for _, r := range rs {
		org, routes, ok, err := BindingFrom(r)
		if !ok {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			delete(out, r.Metadata.Namespace)
			seen[r.Metadata.Namespace] = true
			continue
		}
		if seen[org] {
			errs = append(errs, fmt.Errorf("gateway: org %s has more than one gateway binding; none is used", org))
			delete(out, org)
			continue
		}
		seen[org] = true
		out[org] = routes
	}
	return out, errs
}
