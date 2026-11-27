package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/gateway"
	"github.com/ar4mirez/lucy/internal/policy"
)

// A gateway process records through the API: the daemon decides each credential use from its own
// routes and records it, and the gateway forwards only on that decision. Credentials are made up by
// the test; the upstream is a local TLS server behind a fake resolver.

type gwCreds map[string][]byte

func (c gwCreds) Credential(_ context.Context, org, name string) ([]byte, error) {
	v, ok := c[org+"/"+name]
	if !ok {
		return nil, errors.New("none")
	}
	return bytes.Clone(v), nil
}

type gwResolver map[string][]netip.Addr

func (r gwResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := r[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

func TestGatewayRecordsThroughTheAPI(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
	}))
	t.Cleanup(up.Close)
	_, port, _ := strings.Cut(strings.TrimPrefix(up.URL, "https://"), ":")
	routes := []gateway.Route{{Name: "github", Upstream: "https://example.com:" + port, Credential: "github", Scope: "repo:acme/web",
		Prefix: "Bearer ", Allow: []gateway.Rule{{Methods: []string{"GET"}, Path: "/repos/acme/web"}}}}
	f := start(t, os.Geteuid(), 0, func(o *Options) {
		o.Gateways = &gateway.StoreRecorder{Store: o.Store}
		if err := o.Gateways.SetRoutes(map[string][]gateway.Route{"acme": routes}); err != nil {
			t.Fatal(err)
		}
	})
	rec := gateway.Remote{Client: f.client}
	got, err := rec.Routes(ctx, "acme")
	if err != nil || len(got) != 1 || got[0].Name != "github" || got[0].Upstream != routes[0].Upstream || got[0].Credential != "github" {
		t.Fatalf("routes %+v, %v", got, err)
	}
	if none, err := rec.Routes(ctx, "beta"); err != nil || len(none) != 0 {
		t.Fatalf("an org without a binding: %+v, %v", none, err)
	}

	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	tok := []byte("test_" + hex.EncodeToString(b))
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	loop := netip.MustParseAddr("127.0.0.1")
	g, err := gateway.New(gateway.Config{Org: "acme", Recorder: rec, Routes: got, Credentials: gwCreds{"acme/github": tok},
		Resolver: gwResolver{"example.com": {loop}}, TLS: &tls.Config{RootCAs: pool},
		Addresses: func(a netip.Addr) error {
			if a == loop {
				return nil
			}
			return gateway.PublicOnly(a)
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	front := httptest.NewServer(g)
	t.Cleanup(front.Close)
	resp, err := http.Get(front.URL + "/github/repos/acme/web/pulls")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok /repos/acme/web/pulls auth=Bearer "+gateway.Redacted {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	resp, err = http.Get(front.URL + "/github/repos/acme/other")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a refused path: status %d", resp.StatusCode)
	}

	evs, err := f.store.ReadAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		if bytes.Contains(e.Payload, tok) {
			t.Fatalf("event %s holds the credential", e.Type)
		}
		if e.Stream == gateway.Stream("acme") {
			types = append(types, e.Type)
		}
		if e.Type == (policy.DecisionRecord{}).EventType() && !strings.Contains(string(e.Payload), `"cleared"`) {
			t.Errorf("decision %s", e.Payload)
		}
	}
	if strings.Join(types, ",") != "gateway.forwarded,gateway.completed,gateway.denied" {
		t.Fatalf("gateway events %v", types)
	}

	// The daemon decides from its own routes: a route it does not have, or another upstream, is refused.
	if _, err := rec.Decide(ctx, "acme", gateway.Use{Route: "github", Method: "GET", Path: "/x", Upstream: "evil.example:443"}); !errors.Is(err, gateway.ErrUnknownRoute) {
		t.Fatalf("another upstream: %v", err)
	}
}

// Every request is recorded whatever its path or method: a path longer than any API field on an
// allowed route is decided and recorded with the hash of the whole path, and a method lucy does
// not forward is refused and recorded with the method clipped.
func TestGatewayRecordsEveryRequestThroughTheAPI(t *testing.T) {
	routes := []gateway.Route{{Name: "github", Upstream: "https://example.com", Credential: "github", Scope: "repo:acme/web",
		Allow: []gateway.Rule{{Methods: []string{"GET"}, Path: "/repos/acme/web"}}}}
	f := start(t, os.Geteuid(), 0, func(o *Options) {
		o.Gateways = &gateway.StoreRecorder{Store: o.Store}
		if err := o.Gateways.SetRoutes(map[string][]gateway.Route{"acme": routes}); err != nil {
			t.Fatal(err)
		}
	})
	loop := netip.MustParseAddr("127.0.0.1")
	g, err := gateway.New(gateway.Config{Org: "acme", Recorder: gateway.Remote{Client: f.client}, Routes: routes,
		Credentials: gwCreds{"acme/github": []byte("test_made_up_credential")}, Resolver: gwResolver{"example.com": {loop}},
		Addresses: func(a netip.Addr) error {
			if a == loop {
				return nil
			}
			return gateway.PublicOnly(a)
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	front := httptest.NewServer(g)
	t.Cleanup(front.Close)

	gatewayEvents := func() []eventstore.Event {
		t.Helper()
		evs, err := f.store.ReadStream(ctx, gateway.Stream("acme"), 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}

	long := "/repos/acme/web/" + strings.Repeat("a/", 2100) + "z"
	resp, err := http.Get(front.URL + "/github" + long)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	evs := gatewayEvents()
	if len(evs) == 0 || evs[0].Type != (gateway.Forwarded{}).EventType() {
		t.Fatalf("a %d-byte path on an allowed route (status %d) left %d gateway events", len(long), resp.StatusCode, len(evs))
	}
	var fw gateway.Forwarded
	if err := json.Unmarshal(evs[0].Payload, &fw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("/github" + long))
	if !strings.HasSuffix(fw.Path, " sha256:"+hex.EncodeToString(sum[:])) {
		t.Fatalf("the recorded path %q does not hash the whole path", fw.Path)
	}
	all, err := f.store.ReadAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var decided bool
	for _, e := range all {
		decided = decided || e.Type == (policy.DecisionRecord{}).EventType()
	}
	if !decided {
		t.Fatal("the long path's credential use has no decision")
	}

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET http://gw HTTP/1.1\r\nHost: gw\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err = http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	conn.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an empty absolute-form path: status %d", resp.StatusCode)
	}
	evs = gatewayEvents()
	if last := evs[len(evs)-1]; last.Type != (gateway.Denied{}).EventType() || !strings.Contains(string(last.Payload), `"(empty path)"`) {
		t.Fatalf("an empty absolute-form path was not recorded as denied: %s %s", last.Type, last.Payload)
	}

	method := strings.Repeat("A", 20)
	req, err := http.NewRequest(method, front.URL+"/github/repos/acme/web", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("a 20-byte method: status %d", resp.StatusCode)
	}
	evs = gatewayEvents()
	last := evs[len(evs)-1]
	var dn gateway.Denied
	if err := json.Unmarshal(last.Payload, &dn); err != nil || last.Type != (gateway.Denied{}).EventType() {
		t.Fatalf("a 20-byte method was not recorded as denied: %s %s %v", last.Type, last.Payload, err)
	}
	if dn.Method != method[:gateway.MaxMethod] || !strings.Contains(dn.Reason, "not an HTTP method lucy forwards") {
		t.Fatalf("denial %+v", dn)
	}
}

func TestGatewayMethodsWithoutARecorder(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	if r, err := f.client.GatewayRoutes(ctx, "acme"); err != nil || len(r.Routes) != 0 {
		t.Fatalf("routes %+v, %v", r, err)
	}
	_, err := f.client.GatewayUse(ctx, api.GatewayUseParams{Org: "acme", Route: "github", Method: "GET", Path: "/x", Upstream: "example.com"})
	wantCode(t, err, api.CodeUnavailable)
}
