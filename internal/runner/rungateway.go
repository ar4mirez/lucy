package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// The run's gateway socket (LUCY-CRED-003, LUCY-CRED-005): a Pilot reaches its org's credential
// gateway through a Unix socket the runner opens in the run's scratch directory, and the runner
// adds the run's capability token to every request it forwards. So the token never enters the
// Pilot's process, environment or files: the Pilot is told only the socket's path, in
// LUCY_RUN_GATEWAY, and whatever capability header it sends is dropped.

// RunGatewaySocket is the socket's name in the run's scratch directory.
const RunGatewaySocket = ".lucy-gateway.sock"

// capabilityHeader is the gateway's header for the token (gateway.CapabilityHeader).
const capabilityHeader = "X-Lucy-Capability"

// Renewer is an Execution that takes the run's renewed capability token, which the runner
// passes on after each heartbeat.
type Renewer interface {
	Renew(capability string)
}

// runGateway forwards a run's requests to its org's gateway socket with the run's current token.
type runGateway struct {
	token  atomic.Pointer[string]
	ln     net.Listener
	srv    *http.Server
	socket string
}

// newRunGatewayHandler returns the proxy: every request goes to the gateway on upstream (a Unix
// socket path), with any capability header the caller set dropped and the current token added.
func newRunGatewayHandler(upstream string, token func() string) http.Handler {
	target := &url.URL{Scheme: "http", Host: "gateway"}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = "gateway"
			pr.Out.Header.Del(capabilityHeader)
			for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				pr.Out.Header.Del(h)
			}
			if t := token(); t != "" {
				pr.Out.Header.Set(capabilityHeader, t)
			}
		},
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", upstream)
			},
			ResponseHeaderTimeout: 5 * time.Minute,
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "lucy runner: the org's gateway is not reachable", http.StatusBadGateway)
		},
	}
}

// startRunGateway listens on the run's socket in dir and serves the proxy to upstream.
func startRunGateway(dir, upstream, token string, log *slog.Logger) (*runGateway, error) {
	if token == "" {
		return nil, errors.New("runner: a run gateway needs the run's capability")
	}
	socket := filepath.Join(dir, RunGatewaySocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("runner: the run's gateway socket: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		return nil, errors.Join(err, ln.Close())
	}
	g := &runGateway{ln: ln, socket: socket}
	g.token.Store(&token)
	g.srv = &http.Server{Handler: newRunGatewayHandler(upstream, g.current), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := g.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("run gateway stopped", "error", err)
		}
	}()
	return g, nil
}

func (g *runGateway) current() string {
	if t := g.token.Load(); t != nil {
		return *t
	}
	return ""
}

// renew replaces the token the proxy adds.
func (g *runGateway) renew(token string) {
	if token != "" {
		g.token.Store(&token)
	}
}

// close stops serving and forgets the token.
func (g *runGateway) close() {
	empty := ""
	g.token.Store(&empty)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = g.srv.Shutdown(ctx) // the run is over; its socket goes with its scratch directory
}
