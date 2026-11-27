package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/daemon"
	"github.com/ar4mirez/lucy/internal/gateway"
)

const gatewayUsage = `Usage:
  lucy gateway --org <org> [--socket <path>] [--listen <path>] [--vault <dir>] [--passphrase-file <file>]

Runs an org's credential gateway: the one place the org's credentials are used, outside its
container. A worker calls http://<gateway>/<route>/<path> instead of the service; the gateway
forwards only what the route allows, to the route's https:// upstream, with the route's
credential added, after the daemon's policy engine cleared it and recorded the request.

The routes come from the org's SecretStore AdapterBinding (type lucy-gateway), as the daemon
loaded it. The gateway listens on a Unix socket (by default gateway/<org>.sock in lucy's runtime
directory, 0600) and records every request through the daemon.

The org's credentials come from the vault (` + "`lucy vault`" + `), and only that org's: a route whose
credential the vault does not hold is refused, and the refusal recorded. The gateway unlocks the
vault once, at start, with the vault passphrase: from --passphrase-file, else the systemd
credential lucy-vault, else asked once on the terminal; never an argument or the environment
(see ` + "`lucy vault --help`" + `). Run headless, under a service manager, point it at a 0400 file or a
service credential.

Flags:
  --org <org>        the org whose gateway this is (required)
  --socket <path>    the daemon's socket
  --listen <path>    the gateway's own socket
  --vault <dir>      the vault (vault/ in lucy's data directory by default)
  --passphrase-file <file>
                     the vault passphrase, in a file only its owner can read, outside the vault
`

func runGateway(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	org := fs.String("org", "", "")
	listen := fs.String("listen", "", "")
	vaultDir := fs.String("vault", "", "")
	passFile := fs.String("passphrase-file", "", "")
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
	v, vdir, err := openVault(*vaultDir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer v.Close()
	if err := unlockVault(v, *passFile, vdir); err != nil {
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy gateway --help`\n", err)
		return 1
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := gatewaySignalContext()
	defer cancel()
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ln, err := gateway.ListenUnix(path)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if err := serveGateway(ctx, c, *org, ln, orgVault{v: v, org: *org}, log); err != nil {
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

// gatewaySignalContext ends on SIGINT, SIGTERM or, unless the gateway was started under nohup,
// SIGHUP.
func gatewaySignalContext() (context.Context, context.CancelFunc) {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	return signal.NotifyContext(context.Background(), sigs...)
}
