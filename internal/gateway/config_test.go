package gateway_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/gateway"
	"github.com/ar4mirez/lucy/internal/spec"
)

const binding = `apiVersion: ai-sdlc.io/v1alpha1
kind: AdapterBinding
metadata: { name: gateway, namespace: acme }
spec:
  interface: SecretStore
  type: lucy-gateway
  version: 1.0.0
  config:
    routes:
      - name: github
        upstream: https://api.github.com
        credential: github
        scope: repo:acme/web
        prefix: "Bearer "
        allow:
          - { methods: [GET, POST], path: /repos/acme/web }
`

func admit(t *testing.T, doc string) spec.Resource {
	t.Helper()
	res := spec.LoadFile("gateway.yaml", []byte(doc))
	if len(res) != 1 || !res[0].Valid() {
		t.Fatalf("not admitted: %+v", res)
	}
	return *res[0].Resource
}

func TestBindingFrom(t *testing.T) {
	org, routes, ok, err := gateway.BindingFrom(admit(t, binding))
	if err != nil || !ok || org != "acme" || len(routes) != 1 {
		t.Fatalf("%q %+v %v %v", org, routes, ok, err)
	}
	r := routes[0]
	if r.Name != "github" || r.Upstream != "https://api.github.com" || r.Credential != "github" || r.Scope != "repo:acme/web" ||
		r.Prefix != "Bearer " || len(r.Allow) != 1 || strings.Join(r.Allow[0].Methods, ",") != "GET,POST" || r.Allow[0].Path != "/repos/acme/web" {
		t.Fatalf("route %+v", r)
	}
	// Another interface or type is not the gateway's.
	if _, _, ok, err := gateway.BindingFrom(admit(t, strings.Replace(binding, "SecretStore", "Sandbox", 1))); ok || err != nil {
		t.Errorf("a Sandbox binding was read: %v %v", ok, err)
	}
	bad := map[string][2]string{
		"another major version": {"version: 1.0.0", "version: 2.0.0"},
		"a misspelt key":        {"credential: github", "credentail: github"},
		"an unknown config key": {"    routes:", "    token: x\n    routes:"},
		"an http upstream":      {"https://api.github.com", "http://api.github.com"},
		"an unknown method":     {"[GET, POST]", "[GET, TRACE]"},
		"no rules":              {"        allow:\n          - { methods: [GET, POST], path: /repos/acme/web }\n", "        allow: []\n"},
		"a relative path":       {"path: /repos/acme/web", "path: repos"},
		"a number for a string": {"scope: repo:acme/web", "scope: 7"},
		"a git push path":       {"path: /repos/acme/web", "path: /acme/web.git/git-receive-pack"},
	}
	for name, c := range bad {
		doc := strings.Replace(binding, c[0], c[1], 1)
		if doc == binding {
			t.Fatalf("%s: the case changed nothing", name)
		}
		if _, _, ok, err := gateway.BindingFrom(admit(t, doc)); !ok || err == nil {
			t.Errorf("%s: read (%v, %v)", name, ok, err)
		}
	}
}

func TestABindingThatAllowsAPushIsRefused(t *testing.T) {
	doc := strings.Replace(binding, "path: /repos/acme/web", "path: /acme/web.git/git-receive-pack", 1)
	if _, _, ok, err := gateway.BindingFrom(admit(t, doc)); !ok || !errors.Is(err, gateway.ErrPush) {
		t.Fatalf("read (%v, %v), want ErrPush", ok, err)
	}
}

func TestBindingsFailClosed(t *testing.T) {
	good := admit(t, binding)
	other := admit(t, strings.Replace(binding, "namespace: acme", "namespace: beta", 1))
	broken := admit(t, strings.Replace(strings.Replace(binding, "namespace: acme", "namespace: gamma", 1), "https://", "http://", 1))
	second := admit(t, strings.Replace(binding, "name: gateway,", "name: gateway2,", 1))
	byOrg, errs := gateway.Bindings([]spec.Resource{good, other, broken})
	if len(byOrg) != 2 || byOrg["acme"] == nil || byOrg["beta"] == nil || len(errs) != 1 {
		t.Fatalf("%v %v", byOrg, errs)
	}
	byOrg, errs = gateway.Bindings([]spec.Resource{good, second, other})
	if _, ok := byOrg["acme"]; ok || byOrg["beta"] == nil || len(errs) != 1 {
		t.Fatalf("two bindings for one org: %v %v", byOrg, errs)
	}
}
