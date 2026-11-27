package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/gateway"
	"github.com/ar4mirez/lucy/internal/vault"
)

const gatewayBinding = `apiVersion: ai-sdlc.io/v1alpha1
kind: AdapterBinding
metadata: { name: gateway, namespace: acme }
spec:
  interface: SecretStore
  type: lucy-gateway
  version: 1.0.0
  config:
    routes:
      - name: github
        upstream: https://forge.invalid
        credential: github
        allow:
          - { methods: [GET], path: /repos/acme/web }
`

// TestGatewayProcess runs `lucy daemon` with an org's gateway binding and a gateway on a socket in a
// temp dir. The request carries no capability, so the gateway refuses it before it reads a
// credential or looks anything up, and the daemon records the refusal; nothing is sent anywhere.
func TestGatewayProcess(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "run", "lucy.sock")
	resources := filepath.Join(dir, "config", "lucy", "resources")
	if err := os.MkdirAll(resources, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "gateway.yaml"), []byte(gatewayBinding), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	ctx, cancel := context.WithCancel(context.Background())
	signalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() {
		cancel()
		signalContext = defaultSignalContext
	})
	var daemonErr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"daemon", "--socket", sock, "--db", filepath.Join(dir, "data", "lucy.db")}, &bytes.Buffer{}, &daemonErr)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if code, _, _ := lucy("status", "--socket", sock); code == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon not up: %s", daemonErr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() {
		lucy("daemon", "stop", "--socket", sock)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("lucy daemon did not stop")
		}
	})

	v, err := vault.Open(filepath.Join(dir, "vault"))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	creds := orgVault{v: v, org: "acme"} // an empty vault: the route's credential is not held
	c := client.New(sock, "test", "0")
	defer c.Close()
	gsock := filepath.Join(dir, "gw", "acme.sock")
	ln, err := gateway.ListenUnix(gsock)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(gsock); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("gateway socket: %v, %v", st, err)
	}
	gctx, gcancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serveGateway(gctx, c, "acme", ln, creds, slog.New(slog.DiscardHandler)) }()

	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", gsock)
	}}}
	var resp *http.Response
	for deadline := time.Now().Add(5 * time.Second); ; {
		resp, err = hc.Get("http://gateway/github/repos/acme/web")
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "carries no capability") {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	// A second gateway on the socket of one that is serving is refused.
	if _, err := gateway.ListenUnix(gsock); err == nil || !strings.Contains(err.Error(), "another gateway is listening") {
		t.Fatalf("a second listener: %v", err)
	}
	gcancel()
	if err := <-served; err != nil {
		t.Fatalf("serveGateway: %v", err)
	}
	if code, out, errOut := lucy("capability", "ls", "--socket", sock); code != 0 || out != "capabilities[0]: none\n" {
		t.Fatalf("capability ls: %d %q %q", code, out, errOut)
	}
	code, out, _ := lucy("events", "--socket", sock)
	if code != 0 || !strings.Contains(out, ",gateway/acme,1,gateway.denied,1,") {
		t.Fatalf("events: %s", out)
	}

	// An org without a gateway binding has no gateway to run.
	ln2, err := gateway.ListenUnix(filepath.Join(dir, "gw", "beta.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := serveGateway(context.Background(), c, "beta", ln2, creds, slog.New(slog.DiscardHandler)); err == nil ||
		!strings.Contains(err.Error(), "no gateway binding for org beta") {
		t.Fatalf("an org without a binding: %v", err)
	}
}
