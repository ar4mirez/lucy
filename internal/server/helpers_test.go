package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/audit"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/gateway"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/sandbox"
	"github.com/ar4mirez/lucy/internal/spec"
)

// note is a test payload type.
type note struct {
	Text string `json:"text"`
}

func (note) EventType() string { return "test.note" }
func (note) EventVersion() int { return 1 }

// shortTempDir returns a temp dir short enough for a unix socket path (macOS allows 103 bytes;
// t.TempDir there can be longer).
func shortTempDir(t *testing.T) string {
	t.Helper()
	if d := t.TempDir(); len(d) < 80 {
		return d
	}
	d, err := os.MkdirTemp("", "lucy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

type fixture struct {
	srv    *Server
	store  *eventstore.Store
	pl     *PeerListener
	hs     *http.Server
	sock   string
	client *client.Client
}

// start serves a Server on a real unix socket in a temp dir. allowUID is the uid the peer check
// admits; tweak adjusts the options before New.
func start(t *testing.T, allowUID, maxConns int, tweak func(*Options), onRefuse ...func(PeerCred, error)) *fixture {
	t.Helper()
	dir := shortTempDir(t)
	reg, err := eventstore.NewRegistry(append(append(spec.Payloads(), CommandEvent{}, policy.DecisionRecord{}, note{}), append(append(runner.Payloads(), sandbox.Payloads()...), gateway.Payloads()...)...)...)
	if err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(context.Background(), filepath.Join(dir, "data", "lucy.db"),
		eventstore.Options{Registry: reg, SnapshotDir: filepath.Join(dir, "snap"),
			Projectors: []eventstore.Projector{spec.StatusProjector{}, runner.Projector{}, sandbox.Projector{}, audit.Projector{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	pl := NewPeerListener(ln, allowUID, maxConns)
	if len(onRefuse) > 0 {
		pl.OnRefuse = onRefuse[0]
	}
	opts := Options{Store: store, Version: "1.2.3-test", Refused: pl.Refused, Log: slog.New(slog.DiscardHandler)}
	if tweak != nil {
		tweak(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	hs := srv.HTTPServer()
	go hs.Serve(pl) //nolint:errcheck // returns ErrServerClosed at cleanup
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hs.Shutdown(ctx) //nolint:errcheck // test cleanup
	})
	c := client.New(sock, "test", "0")
	t.Cleanup(c.Close)
	return &fixture{srv: srv, store: store, pl: pl, hs: hs, sock: sock, client: c}
}

// raw posts body to path over the fixture's socket and returns the HTTP status and decoded response.
func (f *fixture) raw(t *testing.T, method, path, contentType string, body []byte) (int, api.Response, string) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", f.sock)
	}}, Timeout: 5 * time.Second}
	defer hc.CloseIdleConnections()
	req, err := http.NewRequest(method, "http://lucy"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var r api.Response
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("response is not JSON-RPC: %q", data)
	}
	return resp.StatusCode, r, string(data)
}

func rpc(method, params string) []byte {
	b := `{"jsonrpc":"2.0","id":7,"method":"` + method + `"`
	if params != "" {
		b += `,"params":` + params
	}
	return []byte(b + "}")
}

// wantCode asserts err is an *api.Error with the code and the code's fixed message.
func wantCode(t *testing.T, err error, code api.Code) *api.Error {
	t.Helper()
	ae, ok := err.(*api.Error)
	if !ok {
		t.Fatalf("got %v (%T), want an api error %d", err, err, code)
	}
	if ae.Code != code || ae.Message != code.Message() || ae.Data == nil || ae.Data.Kind != code.Kind() {
		t.Fatalf("got %+v, want code %d", ae, code)
	}
	return ae
}

func noLeak(t *testing.T, body string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(body, s) {
			t.Errorf("response leaks %q: %s", s, body)
		}
	}
}
