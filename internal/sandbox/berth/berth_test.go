package berth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ar4mirez/lucy/internal/sandbox"
	"github.com/ar4mirez/lucy/internal/sandbox/berth"
	"github.com/ar4mirez/lucy/internal/sandbox/berth/berthtest"
	"github.com/ar4mirez/lucy/internal/spec"
)

// The oracle for these tests is berth's own OpenAPI document (testdata/berth-api-v1.json, an
// unchanged excerpt of the operations lucy uses): the client's requests must fit its request
// schemas, and the fake server's answers must fit its response schemas, so the fake cannot drift
// from the berth it stands in for.

const apiDoc = "file:///berth-api-v1.json"

// schema compiles the schema at a JSON pointer into berth's OpenAPI document.
func schema(t *testing.T, pointer string) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "berth-api-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(apiDoc, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(apiDoc + "#" + pointer)
	if err != nil {
		t.Fatalf("compile %s: %v", pointer, err)
	}
	return s
}

func esc(path string) string { return strings.ReplaceAll(path, "/", "~1") }

func response(path, method, status string) string {
	return "/paths/" + esc(path) + "/" + method + "/responses/" + status + "/content/application~1json/schema"
}

func requestBody(path string) string {
	return "/paths/" + esc(path) + "/post/requestBody/content/application~1json/schema"
}

func validate(t *testing.T, s *jsonschema.Schema, what string, raw []byte) {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: not JSON: %v: %s", what, err, raw)
	}
	if err := s.Validate(v); err != nil {
		t.Errorf("%s does not fit berth's documented schema: %v\n%s", what, err, raw)
	}
}

// get fetches path from the fake over its socket, as raw bytes.
func get(t *testing.T, sock, method, path, body string) (int, []byte) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	req, err := http.NewRequest(method, "http://berth"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, buf.Bytes()
}

// TestFakeAnswersFitBerthSchemas checks the fake against berth's documented response schemas.
func TestFakeAnswersFitBerthSchemas(t *testing.T) {
	f := berthtest.Start(t, "acme")
	errSchema := schema(t, "/components/schemas/Error")
	cases := []struct {
		method, path, body, docPath string
		status                      int
	}{
		{"GET", "/v1", "", "/v1", 200},
		{"GET", "/v1/orgs/acme", "", "/v1/orgs/{org}", 200},
		{"POST", "/v1/orgs/acme/up", `{"confirm":"acme"}`, "/v1/orgs/{org}/up", 200},
		{"GET", "/v1/orgs/acme", "", "/v1/orgs/{org}", 200}, // now running
		{"POST", "/v1/orgs/acme/restart", `{"confirm":"acme"}`, "/v1/orgs/{org}/restart", 200},
		{"POST", "/v1/orgs/acme/down", `{"confirm":"acme"}`, "/v1/orgs/{org}/down", 200},
		{"POST", "/v1/orgs/acme/restart", `{"confirm":"acme"}`, "", 409},
		{"POST", "/v1/orgs/acme/up", `{}`, "", 403},
		{"POST", "/v1/orgs/acme/up", `{"confirm":"acme","force":true}`, "", 400},
		{"GET", "/v1/orgs/nope", "", "", 404},
		{"GET", "/v1/orgs/acme?lines=5", "", "", 400},
		{"DELETE", "/v1/orgs/acme", "", "", 404},
	}
	for _, c := range cases {
		status, raw := get(t, f.Socket, c.method, c.path, c.body)
		if status != c.status {
			t.Errorf("%s %s: status %d, want %d: %s", c.method, c.path, status, c.status, raw)
			continue
		}
		method := strings.ToLower(c.method)
		if c.status == 200 {
			validate(t, schema(t, response(c.docPath, method, "200")), c.method+" "+c.path, raw)
		} else {
			validate(t, errSchema, c.method+" "+c.path, raw)
		}
	}
}

// TestClientRequestsFitBerthSchemas checks what the client sends against berth's request schemas.
func TestClientRequestsFitBerthSchemas(t *testing.T) {
	f := berthtest.Start(t, "acme")
	c := client(t, f)
	ctx := context.Background()
	for _, op := range []sandbox.Op{sandbox.OpUp, sandbox.OpRestart, sandbox.OpDown} {
		if err := c.Do(ctx, "acme", op); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
	}
	changes := f.Changes()
	if len(changes) != 3 {
		t.Fatalf("berth saw %d changes, want 3: %+v", len(changes), changes)
	}
	for i, op := range []string{"up", "restart", "down"} {
		ch := changes[i]
		if ch.Method != "POST" || ch.Path != "/v1/orgs/acme/"+op {
			t.Errorf("change %d: %s %s, want POST /v1/orgs/acme/%s", i, ch.Method, ch.Path, op)
		}
		validate(t, schema(t, requestBody("/v1/orgs/{org}/"+op)), op+" body", []byte(ch.Body))
		var body map[string]string
		if err := json.Unmarshal([]byte(ch.Body), &body); err != nil || body["confirm"] != "acme" {
			t.Errorf("%s body %q does not confirm with the org's name", op, ch.Body)
		}
	}
}

func client(t *testing.T, f *berthtest.Server) *berth.Client {
	t.Helper()
	c, err := berth.New(berth.Options{Socket: f.Socket})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStatusAndServer(t *testing.T) {
	f := berthtest.Start(t, "acme", "beta")
	f.SetRunning("beta", true)
	c := client(t, f)
	ctx := context.Background()
	if s, err := c.Status(ctx, "acme"); err != nil || s.Running || s.Org != "acme" || s.Container != "claude-acme" {
		t.Errorf("acme: %+v, %v; want a stopped claude-acme", s, err)
	}
	if s, err := c.Status(ctx, "beta"); err != nil || !s.Running {
		t.Errorf("beta: %+v, %v; want running", s, err)
	}
	srv, err := c.Server(ctx)
	if err != nil || !srv.Restarts || srv.ReadOnly {
		t.Errorf("server: %+v, %v; want restarts allowed", srv, err)
	}
	f.SetReadOnly(true)
	if srv, err := c.Server(ctx); err != nil || srv.Restarts || !srv.ReadOnly {
		t.Errorf("read-only server: %+v, %v", srv, err)
	}
	if c.Type() != "berth" {
		t.Errorf("Type() = %q", c.Type())
	}
}

func TestErrorsMapOntoTheSandbox(t *testing.T) {
	f := berthtest.Start(t, "acme")
	c := client(t, f)
	ctx := context.Background()
	if _, err := c.Status(ctx, "nope"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("unknown org: %v, want ErrNotFound", err)
	}
	var be *berth.Error
	if err := c.Do(ctx, "acme", sandbox.OpRestart); !errors.Is(err, sandbox.ErrState) || !errors.As(err, &be) || be.Kind != "not-running" {
		t.Errorf("restart of a stopped org: %v, want ErrState (not-running)", err)
	}
	f.SetReadOnly(true)
	if err := c.Do(ctx, "acme", sandbox.OpUp); !errors.Is(err, sandbox.ErrRefused) {
		t.Errorf("read-only: %v, want ErrRefused", err)
	}
	f.SetReadOnly(false)
	f.FailNext("acme", berthtest.ErrInjected)
	err := c.Do(ctx, "acme", sandbox.OpUp)
	if err == nil || !strings.Contains(err.Error(), berthtest.ErrInjected.Error()) || errors.Is(err, sandbox.ErrRefused) ||
		!errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("failed operation: %v, want ErrUnavailable", err)
	}
	if f.Running("acme") {
		t.Error("a failed up left the org running")
	}
}

func TestRefusesBadInputWithoutAsking(t *testing.T) {
	f := berthtest.Start(t, "acme")
	c := client(t, f)
	ctx := context.Background()
	for _, org := range []string{"", "Acme", "-acme", "acme/../x", "acme@", "a b", "acme?x=1", "acme%2Fup", strings.Repeat("a", 64)} {
		if _, err := c.Status(ctx, org); err == nil {
			t.Errorf("Status(%q) was asked", org)
		}
		if err := c.Do(ctx, org, sandbox.OpDown); err == nil {
			t.Errorf("Do(%q) was asked", org)
		}
	}
	if err := c.Do(ctx, "acme", "destroy"); err == nil {
		t.Error("an unknown operation was sent")
	}
	if n := len(f.Calls()); n != 0 {
		t.Errorf("berth saw %d requests for bad input: %+v", n, f.Calls())
	}
	if _, err := c.Status(ctx, "acme@box1"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("an org on a registered host is a valid address: %v", err)
	}
}

// TestRefusesUndocumentedAnswers checks that an answer berth does not document is refused, not
// guessed at: another document version, a state berth does not name, another org, no error document.
func TestRefusesUndocumentedAnswers(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"newer info schema": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"schema":"berth.info/v2","org":"acme","container":"claude-acme","state":"running"}`))
		},
		"unknown state": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"schema":"berth.info/v1","org":"acme","container":"claude-acme","state":"paused"}`))
		},
		"another org": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"schema":"berth.info/v1","org":"beta","container":"claude-beta","state":"running"}`))
		},
		"error without a document": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`oops`))
		},
		"too large": func(w http.ResponseWriter, _ *http.Request) {
			w.Write(bytes.Repeat([]byte(" "), 1<<20+1))
		},
	}
	for name, h := range cases {
		c := serveRaw(t, h)
		if _, err := c.Status(context.Background(), "acme"); !errors.Is(err, berth.ErrContract) {
			t.Errorf("%s: %v, want ErrContract", name, err)
		}
	}
	c := serveRaw(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"org":"beta","output":""}`)) })
	if err := c.Do(context.Background(), "acme", sandbox.OpDown); !errors.Is(err, berth.ErrContract) {
		t.Errorf("an operation answered for another org: %v, want ErrContract", err)
	}
	c = serveRaw(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"schema":"berth.api/v2"}`)) })
	if _, err := c.Server(context.Background()); !errors.Is(err, berth.ErrContract) {
		t.Errorf("a newer API document: %v, want ErrContract", err)
	}
}

// serveRaw serves h on a Unix socket and returns a client for it.
func serveRaw(t *testing.T, h http.HandlerFunc) *berth.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "berth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	c, err := berth.New(berth.Options{Socket: sock})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUnavailable(t *testing.T) {
	dir := t.TempDir()
	c, err := berth.New(berth.Options{Socket: filepath.Join(dir, "missing.sock")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(context.Background(), "acme"); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("no server: %v, want ErrUnavailable", err)
	}
}

func TestNewOptions(t *testing.T) {
	bad := []berth.Options{
		{},
		{Socket: "relative.sock"},
		{Socket: "/run/../berth.sock"},
		{Socket: "/run/berth.sock", Token: "t"},
		{Socket: "/run/berth.sock", BaseURL: "https://127.0.0.1:8443", Token: "t"},
		{BaseURL: "http://127.0.0.1:8443", Token: "t"},
		{BaseURL: "https://127.0.0.1:8443"},
		{BaseURL: "https://127.0.0.1:8443/v1", Token: "t"},
		{BaseURL: "https://u:p@127.0.0.1:8443", Token: "t"},
	}
	for _, o := range bad {
		if _, err := berth.New(o); err == nil {
			t.Errorf("New(%+v) accepted", o)
		}
	}
	for _, o := range []berth.Options{{Socket: "/run/berth.sock"}, {BaseURL: "https://127.0.0.1:8443", Token: "t"}} {
		if _, err := berth.New(o); err != nil {
			t.Errorf("New(%+v): %v", o, err)
		}
	}
}

// TestTokenOverTLS checks the TCP path: TLS and a bearer token on every request.
func TestTokenOverTLS(t *testing.T) {
	var auth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"schema":"berth.info/v1","org":"acme","container":"claude-acme","state":"stopped"}`))
	}))
	defer srv.Close()
	c, err := berth.New(berth.Options{BaseURL: srv.URL, Token: "tok", HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestOpenFromBinding(t *testing.T) {
	f := berthtest.Start(t, "acme")
	res := spec.LoadFile("sandbox.yaml", []byte(`apiVersion: ai-sdlc.io/v1alpha1
kind: AdapterBinding
metadata: { name: sandbox, namespace: acme }
spec:
  interface: Sandbox
  type: berth
  version: 1.0.0
  config: { socket: `+f.Socket+` }
`))
	if len(res) != 1 || !res[0].Valid() {
		t.Fatalf("binding not admitted: %+v", res)
	}
	b, err := sandbox.BindingFrom(*res[0].Resource)
	if err != nil {
		t.Fatal(err)
	}
	p, err := sandbox.Providers{berth.Type: berth.Open}.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := p.Status(context.Background(), "acme"); err != nil || s.Org != "acme" {
		t.Errorf("Status through the binding: %+v, %v", s, err)
	}
	for name, mod := range map[string]func(*sandbox.Binding){
		"another major version": func(b *sandbox.Binding) { b.Version = "2.0.0" },
		"no socket":             func(b *sandbox.Binding) { b.Config = nil },
		"an unknown key":        func(b *sandbox.Binding) { b.Config = map[string]any{"socket": f.Socket, "token": "x"} },
		"a relative socket":     func(b *sandbox.Binding) { b.Config = map[string]any{"socket": "berth.sock"} },
		"another type":          func(b *sandbox.Binding) { b.Type = "docker" },
	} {
		bb := b
		mod(&bb)
		if _, err := berth.Open(bb); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
}
