package runner

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOrgGateway stands in for an org's credential gateway on its Unix socket, as lucy gateway
// behaves at its door: a request without a capability token is refused (401), one with a token it
// does not hold as current is refused (403), and the current token is served. It records what it
// was sent.
type fakeOrgGateway struct {
	socket string
	mu     sync.Mutex
	valid  string
	got    []http.Header
}

func startFakeOrgGateway(t *testing.T, dir, valid string) *fakeOrgGateway {
	t.Helper()
	g := &fakeOrgGateway{socket: filepath.Join(dir, "acme.sock"), valid: valid}
	ln, err := net.Listen("unix", g.socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.got = append(g.got, r.Header.Clone())
		valid := g.valid
		g.mu.Unlock()
		switch tok := r.Header.Get(capabilityHeader); {
		case tok == "":
			http.Error(w, "the request carries no capability", http.StatusUnauthorized)
		case tok != valid:
			http.Error(w, "capability refused", http.StatusForbidden)
		default:
			io.WriteString(w, "ok "+r.URL.Path)
		}
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return g
}

func (g *fakeOrgGateway) setValid(tok string) { g.mu.Lock(); g.valid = tok; g.mu.Unlock() }

func (g *fakeOrgGateway) last() http.Header {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.got[len(g.got)-1]
}

func unixClient(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}}
}

func get(t *testing.T, c *http.Client, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://gateway"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The proxy adds the run's current token, drops any the caller set, and takes the renewed one.
func TestRunGatewayAddsTheToken(t *testing.T) {
	dir := shortDir(t)
	g := startFakeOrgGateway(t, dir, "lcap_first")
	runDir := filepath.Join(dir, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rg, err := startRunGateway(runDir, g.socket, "lcap_first", nopLog())
	if err != nil {
		t.Fatal(err)
	}
	defer rg.close()
	c := unixClient(rg.socket)
	if status, body := get(t, c, "/github/repos/acme/web", map[string]string{capabilityHeader: "lcap_forged", "X-Forwarded-For": "10.0.0.1"}); status != 200 || body != "ok /github/repos/acme/web" {
		t.Fatalf("through the run's socket: %d %q", status, body)
	}
	if h := g.last(); h.Get(capabilityHeader) != "lcap_first" || h.Get("X-Forwarded-For") != "" {
		t.Fatalf("the gateway got %v", h)
	}
	g.setValid("lcap_second")
	rg.renew("lcap_second")
	if status, _ := get(t, c, "/github/x", nil); status != 200 || g.last().Get(capabilityHeader) != "lcap_second" {
		t.Fatalf("after renewal: %d, sent %q", status, g.last().Get(capabilityHeader))
	}
	// A direct call to the org's gateway, without the run's socket, carries no token and is refused.
	if status, _ := get(t, unixClient(g.socket), "/github/x", nil); status != http.StatusUnauthorized {
		t.Fatalf("a direct call: %d", status)
	}
	if st, err := os.Stat(rg.socket); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the run's socket: %v, %v", st, err)
	}
	rg.close()
	if _, err := net.DialTimeout("unix", rg.socket, time.Second); err == nil {
		t.Error("the run's socket still answers after the run")
	}
}

// End to end: a run's work reaches its org's gateway through LUCY_RUN_GATEWAY, while neither its
// environment nor any file in its scratch directory holds the token (LUCY-CRED-005).
func TestRunGatewayInARun(t *testing.T) {
	gwDir := shortDir(t)
	const token = "lcap_made-up-run-token-0123456789"
	g := startFakeOrgGateway(t, gwDir, token)
	pf := newProc(t)
	pf.ex.Root = filepath.Join(shortDir(t), "r") // a Unix socket path must stay short
	pf.ex.GatewayDir = gwDir
	l := procLease("gateway")
	l.Capability = token
	x, err := pf.ex.Start(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	var scratch string
	deadline := time.Now().Add(10 * time.Second)
	for scratch == "" && time.Now().Before(deadline) {
		for _, e := range x.Poll().Events {
			lines = append(lines, e.Text)
			if d, ok := strings.CutPrefix(e.Text, "cwd "); ok {
				scratch = d
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if scratch == "" {
		t.Fatalf("the work never reported its directory: %q", lines)
	}
	// While the work runs: no file in its directory holds the token.
	err = filepath.WalkDir(scratch, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), token) {
			t.Errorf("%s holds the token", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rest, res := drain(t, x)
	lines = append(lines, rest...)
	out := strings.Join(lines, "\n")
	if res.Outcome != OutcomeDone || !strings.Contains(out, "gateway 200 ok /github/repos/acme/web") {
		t.Fatalf("result %+v, output:\n%s", res, out)
	}
	if strings.Contains(out, token) {
		t.Fatalf("the token reached the work:\n%s", out)
	}
	if !strings.Contains(out, "env LUCY_RUN_GATEWAY="+filepath.Join(scratch, RunGatewaySocket)) {
		t.Fatalf("the work was not told its gateway socket:\n%s", out)
	}
	if got := g.last().Get(capabilityHeader); got != token {
		t.Fatalf("the gateway got %q", got)
	}
}

// Without a capability on the lease, or without a gateway directory, the run gets no socket.
func TestNoRunGatewayWithoutACapability(t *testing.T) {
	pf := newProc(t)
	pf.ex.GatewayDir = shortDir(t)
	x, err := pf.ex.Start(context.Background(), procLease("env"))
	if err != nil {
		t.Fatal(err)
	}
	lines, _ := drain(t, x)
	if strings.Contains(strings.Join(lines, "\n"), "LUCY_RUN_GATEWAY") {
		t.Fatal("a run without a capability was given a gateway socket")
	}
	bad := procLease("env")
	bad.Capability, bad.Org = "lcap_x", "../etc"
	if _, err := pf.ex.Start(context.Background(), bad); err == nil {
		t.Fatal("an org that is not a plain name named a gateway socket")
	}
}

// shortDir is a temp dir under /tmp, not os.TempDir: on macOS that is /var/folders/.../T/, which
// resolves to a path too long for a run's gateway socket under it.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "rg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func nopLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// A runner root too long for a run's gateway socket is refused up front, and a run's socket path
// past the limit is refused before listening, naming the limit.
func TestRunGatewaySocketPathLength(t *testing.T) {
	short := "/tmp/lucy/runner/acme"
	if err := CheckRunGatewayRoot(short); err != nil {
		t.Fatalf("a short root: %v", err)
	}
	long := "/" + strings.Repeat("r", maxSocketPath()-runSocketSuffix)
	if err := CheckRunGatewayRoot(long); err == nil || !strings.Contains(err.Error(), "--root") {
		t.Fatalf("a root past the limit: %v", err)
	}
	if err := CheckRunGatewayRoot(long[:len(long)-1]); err != nil {
		t.Fatalf("a root at the limit: %v", err)
	}
	dir := shortDir(t)
	deep := filepath.Join(dir, strings.Repeat("d", maxSocketPath()-len(dir)))
	if err := os.Mkdir(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := startRunGateway(deep, filepath.Join(dir, "acme.sock"), "lcap_x", nopLog())
	if err == nil || !strings.Contains(err.Error(), "--root") {
		t.Fatalf("a run socket past the limit: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(deep, RunGatewaySocket)); !os.IsNotExist(err) {
		t.Fatalf("a socket was made past the limit: %v", err)
	}
}

// withToken is the broker as a daemon that issues capabilities serves it: each lease carries one.
type withToken struct{ *Broker }

func (p withToken) Lease(ctx context.Context, r string, pl Placement, org, proof string, ttl time.Duration) (Lease, bool, error) {
	l, ok, err := p.Broker.Lease(ctx, r, pl, org, proof, ttl)
	if ok {
		l.Capability = "lcap_" + l.Org
	}
	return l, ok, err
}

// With acme as the host org, beta's runner on the host is refused and exits before it leases, so
// no beta run, and no beta socket, exists on the host for an acme run to open.
func TestNoOtherOrgsSocketOnTheHost(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	if err := f.b.Submit(ctx, Task{Run: "b#1", Org: "beta", Project: "web", Flight: "b", Generation: 1, Goal: "gateway"}); err != nil {
		t.Fatal(err)
	}
	if err := f.b.Submit(ctx, Task{Run: "a#1", Org: "acme", Project: "web", Flight: "a", Generation: 1, Goal: "gateway"}); err != nil {
		t.Fatal(err)
	}
	gwDir := shortDir(t)
	g := startFakeOrgGateway(t, gwDir, "lcap_acme")
	base := shortDir(t)
	pf := newProc(t)
	pf.ex.Root = filepath.Join(base, "acme")
	pf.ex.GatewayDir = gwDir
	r := newRunner(t, f, "r1", pf.ex, withToken{f.b})
	defer r.Shutdown(ctx, 5*time.Second)
	for range 2 {
		if err := r.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if held := r.Held(); len(held) != 1 || held[0].Run != "a#1" {
		t.Fatalf("acme's runner holds %+v", held)
	}
	bf := newProc(t)
	bf.ex.Root = filepath.Join(base, "beta")
	bf.ex.GatewayDir = gwDir
	beta, err := New(Config{ID: "r2", Org: "beta", Protocol: withToken{f.b}, Executor: bf.ex, Now: f.clock.Now,
		TTL: 30 * time.Second, Log: nopLog()})
	if err != nil {
		t.Fatal(err)
	}
	if err := beta.Run(ctx, time.Millisecond); !errors.Is(err, ErrHostOrg) {
		t.Fatalf("beta's runner on the host: %v, want ErrHostOrg", err)
	}
	if f.run("b#1").State != StateQueued {
		t.Fatal("beta's run left the queue")
	}
	var sockets []string
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSocket != 0 {
			sockets = append(sockets, p)
		}
		return err
	})
	if err != nil || len(sockets) != 1 || !strings.HasPrefix(sockets[0], pf.ex.Root+string(filepath.Separator)) {
		t.Fatalf("sockets on the host: %q, %v; want acme's run's alone", sockets, err)
	}
	if status, _ := get(t, unixClient(sockets[0]), "/github/x", nil); status != 200 || g.last().Get(capabilityHeader) != "lcap_acme" {
		t.Fatalf("the one socket: %d, sent %q", status, g.last().Get(capabilityHeader))
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(sockets[0]), "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
