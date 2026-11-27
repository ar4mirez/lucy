package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/daemon"
	"github.com/ar4mirez/lucy/internal/gateway"
)

const gatewayUsage = `Usage:
  lucy gateway --org <org> [--socket <path>] [--listen <path>]

Runs an org's credential gateway: the one place the org's credentials are used, outside its
container. A worker calls http://<gateway>/<route>/<path> instead of the service; the gateway
forwards only what the route allows, to the route's https:// upstream, with the route's
credential added, after the daemon's policy engine cleared it and recorded the request.

The routes come from the org's SecretStore AdapterBinding (type lucy-gateway), as the daemon
loaded it. The gateway listens on a Unix socket (by default gateway/<org>.sock in lucy's runtime
directory, 0600) and records every request through the daemon.

The org's credentials come from its vault, which is not built yet (m1 slice 4b): until then the
gateway refuses every request that needs a credential, and records the refusal.

Flags:
  --org <org>        the org whose gateway this is (required)
  --socket <path>    the daemon's socket
  --listen <path>    the gateway's own socket
`

// noVault is the gateway's credential source until the vault lands: every credential is
// unavailable, so every request that needs one is refused (fail closed).
type noVault struct{}

func (noVault) Credential(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("the org's vault is not built yet (m1 slice 4b)")
}

func runGateway(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	org := fs.String("org", "", "")
	listen := fs.String("listen", "", "")
	if code, ok := parseFlags(fs, args, gatewayUsage, "lucy gateway --help", stdout, stderr); !ok {
		return code
	}
	if *org == "" {
		fmt.Fprint(stderr, "error: lucy gateway needs --org\nhelp: run `lucy gateway --help`\n")
		return 2
	}
	path := *listen
	if path == "" {
		dir, err := daemon.RuntimeDir()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		path = filepath.Join(dir, "gateway", *org+".sock")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := runnerSignalContext()
	defer cancel()
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ln, err := gateway.ListenUnix(path)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if err := serveGateway(ctx, c, *org, ln, noVault{}, log); err != nil {
		return callFailed(err, c, stderr)
	}
	return 0
}

// serveGateway serves org's gateway on ln until ctx ends, with routes from the daemon and every
// request recorded through it.
func serveGateway(ctx context.Context, c *client.Client, org string, ln net.Listener, creds gateway.Credentials, log *slog.Logger) error {
	defer ln.Close() //nolint:errcheck // closed by Shutdown too
	rec := gateway.Remote{Client: c}
	rctx, cancel := context.WithTimeout(ctx, client.DefaultTimeout)
	routes, err := rec.Routes(rctx, org)
	cancel()
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		return fmt.Errorf("the daemon has no gateway binding for org %s (an AdapterBinding with interface SecretStore and type lucy-gateway)", org)
	}
	g, err := gateway.New(gateway.Config{Org: org, Recorder: rec, Routes: routes, Credentials: creds})
	if err != nil {
		return err
	}
	defer g.Close()
	srv := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	log.Info("lucy gateway started", "org", org, "listen", ln.Addr().String(), "routes", len(routes))
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	log.Info("lucy gateway stopped", "org", org)
	return nil
}
