package daemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/capability"
	"github.com/ar4mirez/lucy/internal/gateway"
)

// The oracle is LUCY-RUN-010 and decision 073: armed with --supervise for an org whose Airspace
// opts in, the daemon's supervisor reaches the model only through the org's credential gateway, on
// the org's model route, with a supervisor capability the gateway verifies; the gateway, not the
// daemon, holds and injects the key; the proposal is shown beside the question and never reaches
// the Pilot; and lucy status shows the tokens against the budget. The model is a fake server.

const testModelKey = "test-model-key-not-real"

// fakeUpstream is the fake model API behind the gateway.
type fakeUpstream struct {
	mu    sync.Mutex
	auths []string
	caps  []string
	paths []string
}

func (f *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auths = append(f.auths, r.Header.Get("X-Api-Key"))
	f.caps = append(f.caps, r.Header.Get(gateway.CapabilityHeader))
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"content": []map[string]any{{"type": "text", "text": `{"answer": "use the staging account", "confident": true}`}},
		"usage":   map[string]any{"input_tokens": 120, "output_tokens": 30},
	})
}

type oneResolver struct{}

func (oneResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

type testCreds struct{}

func (testCreds) Credential(_ context.Context, org, name string) ([]byte, error) {
	if org == "acme" && name == "model-key" {
		return []byte(testModelKey), nil
	}
	return nil, errors.New("no such credential")
}

func TestTheSupervisorCallsTheModelOnlyThroughTheGateway(t *testing.T) {
	dir := shortTempDir(t)
	up := &fakeUpstream{}
	srv := httptest.NewTLSServer(up)
	t.Cleanup(srv.Close)
	port := strings.TrimPrefix(srv.URL, "https://127.0.0.1:")
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	o := opts(dir)
	o.TowerPollInterval = 20 * time.Millisecond
	o.Dispatch, o.Supervise = true, true
	o.GatewayDir = filepath.Join(dir, "gw")
	writeFlights(t, o, "spec: {}")
	for name, doc := range map[string]string{
		"airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\n" +
			"spec: { supervisor: { route: models, smallModel: small-test, largeModel: large-test, dailyTokens: 10000 } }\n",
		"gateway.yaml": "apiVersion: ai-sdlc.io/v1alpha1\nkind: AdapterBinding\nmetadata: { name: gateway, namespace: acme }\n" +
			"spec:\n  interface: SecretStore\n  type: lucy-gateway\n  version: 1.0.0\n  config:\n    routes:\n" +
			"      - { name: models, upstream: 'https://example.com:" + port + "', credential: model-key, header: X-Api-Key, " +
			"allow: [ { methods: [POST], path: /v1/messages } ] }\n",
	} {
		if err := os.WriteFile(filepath.Join(o.ResourcesDir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := startClient(t, o)
	ctx := context.Background()

	// The org's gateway, as `lucy gateway` runs it: it asks the daemon for its routes and to verify
	// each capability, and holds the key itself. Only its resolver, address policy and TLS roots
	// are the test's, to reach the fake model server.
	rec := gateway.Remote{Client: c}
	routes, err := rec.Routes(ctx, "acme")
	if err != nil || len(routes) != 1 {
		t.Fatalf("routes %+v, %v", routes, err)
	}
	g, err := gateway.New(gateway.Config{Org: "acme", Recorder: rec, Routes: routes, Credentials: testCreds{},
		Resolver: oneResolver{}, Addresses: func(netip.Addr) error { return nil }, TLS: &tls.Config{RootCAs: pool, ServerName: "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	ln, err := gateway.ListenUnix(filepath.Join(o.GatewayDir, "acme.sock"))
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: g, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close() })

	eventually := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not happen in 10 s", what)
			}
		}
	}
	var lease api.LeaseResult
	eventually("the Pilot run was leased", func() bool {
		lease, err = c.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-1", Org: "acme"})
		return err == nil && lease.Leased
	})
	if _, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: lease.Run.Run, Fence: lease.Run.Fence,
		Events: []api.RunEvent{{Seq: 1, Kind: "asking", Text: "which account?", Question: "q-1"}}}); err != nil {
		t.Fatal(err)
	}
	var q api.QuestionsList
	eventually("the supervisor's proposal is shown beside the question", func() bool {
		q, err = c.Questions(ctx)
		return err == nil && len(q.Questions) == 1 && q.Questions[0].Proposed != ""
	})
	if got := q.Questions[0]; got.ProposedBy != "supervisor/model" || got.Proposed != "use the staging account" {
		t.Fatalf("the question %+v", got)
	}
	up.mu.Lock()
	if len(up.paths) != 1 || up.paths[0] != "/v1/messages" || up.auths[0] != testModelKey || up.caps[0] != "" {
		t.Fatalf("the model got paths %q, keys %q, capabilities %q: want one call on the route, with the gateway's key and no capability",
			up.paths, up.auths, up.caps)
	}
	up.mu.Unlock()

	// The gateway forwarded it for the supervisor, under a policy decision, and the capability is
	// the supervisor's, bound to the model route.
	page, err := c.Events(ctx, api.EventsParams{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	var forwarded, issued bool
	for _, e := range page.Events {
		switch e.Type {
		case "gateway.forwarded":
			var p gateway.Forwarded
			_ = json.Unmarshal(e.Payload, &p)
			forwarded = p.Run == "supervisor/acme" && p.Route == "models" && p.DecisionSeq > 0
		case "capability.issued":
			issued = issued || strings.Contains(string(e.Payload), `"run":"supervisor/acme"`)
		}
		if strings.Contains(string(e.Payload), testModelKey) {
			t.Fatalf("the model key is in the log: %s", e.Type)
		}
	}
	if !forwarded || !issued {
		t.Fatalf("forwarded for the supervisor %v, a supervisor capability issued %v", forwarded, issued)
	}

	// The proposal never reaches the Pilot: its heartbeat carries no answer.
	if beat, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: lease.Run.Run, Fence: lease.Run.Fence}); err != nil || beat.Answer != "" {
		t.Fatalf("the Pilot's heartbeat %+v, %v", beat, err)
	}
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Supervisors) != 1 || s0(st).Org != "acme" || !s0(st).Armed || s0(st).Tokens != 150 || s0(st).DailyTokens != 10000 {
		t.Fatalf("status supervisors %+v", st.Supervisors)
	}
}

func s0(st api.Status) api.SupervisorUsage { return st.Supervisors[0] }

// Without --supervise, an org that opts in is not supervised by a model: no call, no capability,
// and status shows it unarmed.
func TestTheSupervisorIsOffByDefault(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	o.TowerPollInterval = 20 * time.Millisecond
	o.Dispatch = true
	writeFlights(t, o, "spec: {}")
	if err := os.WriteFile(filepath.Join(o.ResourcesDir, "airspace.yaml"), []byte("apiVersion: lucy/v1alpha1\nkind: Airspace\n"+
		"metadata: { name: acme, namespace: acme }\nspec: { supervisor: { route: models, smallModel: s, largeModel: l, dailyTokens: 100 } }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := startClient(t, o)
	ctx := context.Background()
	var lease api.LeaseResult
	var err error
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if lease, err = c.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-1", Org: "acme"}); err == nil && lease.Leased {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no lease")
		}
	}
	if _, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: lease.Run.Run, Fence: lease.Run.Fence,
		Events: []api.RunEvent{{Seq: 1, Kind: "asking", Text: "which account?", Question: "q-1"}}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	q, err := c.Questions(ctx)
	if err != nil || len(q.Questions) != 1 || q.Questions[0].Proposed != "" {
		t.Fatalf("questions %+v, %v", q, err)
	}
	page, err := c.Events(ctx, api.EventsParams{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Events {
		if strings.HasPrefix(e.Type, "supervisor.") {
			t.Fatalf("a supervisor event %s without --supervise", e.Type)
		}
		if e.Type == "capability.issued" {
			var p capability.Issued
			_ = json.Unmarshal(e.Payload, &p)
			if p.Run == "supervisor/acme" {
				t.Fatal("a supervisor capability issued without --supervise")
			}
		}
	}
	st, err := c.Status(ctx)
	if err != nil || len(st.Supervisors) != 1 || st.Supervisors[0].Armed || st.Supervisors[0].Tokens != 0 {
		t.Fatalf("status %+v, %v", st.Supervisors, err)
	}
}

// An org's rules apply with the Airspace opt-in alone, without --supervise: a matching rule's
// answer is shown as supervisor/rules' proposal and never reaches the Pilot, and a question about
// a secret goes to the owner with the reason recorded (LUCY-RUN-010).
func TestRulesApplyWithoutSupervise(t *testing.T) {
	for _, c := range []struct{ question, proposed, declined string }{
		{"which account?", "use the staging account", ""},
		{"what is the deploy token?", "", "a secret or a credential"},
	} {
		t.Run(c.question, func(t *testing.T) {
			dir := shortTempDir(t)
			o := opts(dir)
			o.TowerPollInterval = 20 * time.Millisecond
			o.Dispatch = true
			writeFlights(t, o, "spec: {}")
			if err := os.WriteFile(filepath.Join(o.ResourcesDir, "airspace.yaml"), []byte("apiVersion: lucy/v1alpha1\nkind: Airspace\n"+
				"metadata: { name: acme, namespace: acme }\nspec: { supervisor: { rules: [ { match: '\\baccount\\b', propose: use the staging account } ] } }\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cl := startClient(t, o)
			ctx := context.Background()
			var lease api.LeaseResult
			var err error
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				if lease, err = cl.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-1", Org: "acme"}); err == nil && lease.Leased {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no lease")
				}
			}
			if _, err := cl.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: lease.Run.Run, Fence: lease.Run.Fence,
				Events: []api.RunEvent{{Seq: 1, Kind: "asking", Text: c.question, Question: "q-1"}}}); err != nil {
				t.Fatal(err)
			}
			var q api.QuestionsList
			var reasons []string
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				q, err = cl.Questions(ctx)
				page, perr := cl.Events(ctx, api.EventsParams{Limit: 500})
				if perr != nil {
					t.Fatal(perr)
				}
				reasons = nil
				for _, e := range page.Events {
					if e.Type == "supervisor.declined" {
						reasons = append(reasons, string(e.Payload))
					}
				}
				if err == nil && len(q.Questions) == 1 && (q.Questions[0].Proposed != "" || len(reasons) > 0) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("no proposal or decline: %+v, %v", q, err)
				}
			}
			got := q.Questions[0]
			if c.proposed != "" && (got.ProposedBy != "supervisor/rules" || got.Proposed != c.proposed || len(reasons) != 0) {
				t.Fatalf("the question %+v, declined %q", got, reasons)
			}
			if c.declined != "" && (got.Proposed != "" || len(reasons) != 1 || !strings.Contains(reasons[0], c.declined)) {
				t.Fatalf("the question %+v, declined %q", got, reasons)
			}
			if beat, err := cl.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: lease.Run.Run, Fence: lease.Run.Fence}); err != nil || beat.Answer != "" {
				t.Fatalf("the Pilot's heartbeat %+v, %v", beat, err)
			}
			if st, err := cl.Status(ctx); err != nil || len(st.Supervisors) != 1 || st.Supervisors[0].Armed {
				t.Fatalf("status %+v, %v", st.Supervisors, err)
			}
		})
	}
}
