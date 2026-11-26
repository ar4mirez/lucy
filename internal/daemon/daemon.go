// Package daemon runs `lucy daemon`: it opens the event store, serves the API on a unix socket in
// lucy's runtime directory and stops cleanly on a signal or a daemon.shutdown call.
//
// The socket is the only listener. It lives in a 0700 runtime directory, is itself 0600, and admits
// only connections whose kernel-reported peer runs as this user (LUCY-API-003). No network listener
// exists: the HTTPS transport for remote surfaces is a seam (Options.Remote) that refuses to start
// until m1 brings device pairing and TLS (LUCY-VIEW-006).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/server"
)

// ErrRemoteNotAvailable is returned when Options.Remote is set: remote listeners land in m1.
var ErrRemoteNotAvailable = errors.New("daemon: the remote (HTTPS) listener lands in m1 with device pairing and TLS (LUCY-VIEW-006); lucy serves only its unix socket")

// DaemonStream is the event stream daemon lifecycle events are appended to.
const DaemonStream = "daemon"

// Started is appended when the daemon starts serving.
type Started struct {
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
	PID        int    `json:"pid"`
}

// EventType implements eventstore.Payload.
func (Started) EventType() string { return "daemon.started" }

// EventVersion implements eventstore.Payload.
func (Started) EventVersion() int { return 1 }

// Stopped is appended when the daemon stops serving.
type Stopped struct {
	PID    int    `json:"pid"`
	Reason string `json:"reason"` // "signal", "api" or "error"
}

// EventType implements eventstore.Payload.
func (Stopped) EventType() string { return "daemon.stopped" }

// EventVersion implements eventstore.Payload.
func (Stopped) EventVersion() int { return 1 }

// Registry returns the event registry the daemon opens the store with.
func Registry() (*eventstore.Registry, error) {
	return eventstore.NewRegistry(Started{}, Stopped{}, server.CommandEvent{})
}

// RemoteOptions configures the HTTPS listener for remote surfaces. It is the seam m1 fills in:
// loopback and tailnet binds only, TLS, and paired-device credentials checked per call.
type RemoteOptions struct {
	Addr     string // host:port; loopback or tailnet only
	CertFile string
	KeyFile  string
}

// Options configures Run.
type Options struct {
	SocketPath  string         // DefaultSocketPath when empty
	DBPath      string         // eventstore.DefaultPath when empty
	SnapshotDir string         // the store's pre-migration snapshot dir; its default when empty
	Version     string         // lucy's version
	Log         *slog.Logger   // slog.Default when nil
	Remote      *RemoteOptions // nil: no network listener. Non-nil is refused until m1.
	// OnReady is called once the socket accepts connections.
	OnReady func(socketPath string)
}

// Run serves the API until ctx is cancelled or a daemon.shutdown call, then stops gracefully.
func Run(ctx context.Context, opts Options) error {
	if opts.Remote != nil {
		return ErrRemoteNotAvailable
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	sock := opts.SocketPath
	if sock == "" {
		var err error
		if sock, err = DefaultSocketPath(); err != nil {
			return err
		}
	}
	sock, err := filepath.Abs(sock)
	if err != nil {
		return err
	}
	if len(sock) > maxSocketPath() {
		return fmt.Errorf("%w: %s is %d bytes, the OS allows %d; set XDG_RUNTIME_DIR or --socket to a shorter path",
			ErrSocketPathTooLong, sock, len(sock), maxSocketPath())
	}
	dir := filepath.Dir(sock)
	if err := prepareRuntimeDir(dir); err != nil {
		return err
	}
	lk, err := lock(dir)
	if err != nil {
		return err
	}
	defer lk.Close() //nolint:errcheck // closing releases the lock

	store, err := openStore(ctx, opts)
	if err != nil {
		return err
	}
	defer store.Close() //nolint:errcheck // reported through the log by the store's callers

	if err := removeStaleSocket(sock); err != nil {
		return err
	}
	ln, err := listen(sock)
	if err != nil {
		return err
	}
	defer os.Remove(sock) //nolint:errcheck // best effort; a stale socket is removed at next start

	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	errAPIStop := errors.New("stopped through the API")
	pl := server.NewPeerListener(ln, os.Geteuid(), server.DefaultMaxConns)
	pl.OnRefuse = func(cred server.PeerCred, err error) {
		log.Warn("socket connection refused", "uid", cred.UID, "pid", cred.PID, "err", err)
	}
	srv, err := server.New(server.Options{
		Store:      store,
		Version:    opts.Version,
		Log:        log,
		OnShutdown: func() { stop(errAPIStop) },
		Refused:    pl.Refused,
	})
	if err != nil {
		return err
	}
	if _, err := store.Append(ctx, DaemonStream, eventstore.AnyVersion, "daemon",
		Started{Version: opts.Version, APIVersion: api.Version, PID: os.Getpid()}); err != nil {
		return err
	}
	hs := srv.HTTPServer()
	served := make(chan error, 1)
	go func() { served <- hs.Serve(pl) }()
	log.Info("lucy daemon serving", "socket", sock, "api", api.Path, "version", opts.Version)
	if opts.OnReady != nil {
		opts.OnReady(sock)
	}

	reason := "signal"
	var serveErr error
	select {
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), errAPIStop) {
			reason = "api"
		}
	case serveErr = <-served:
		reason = "error"
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown timed out", "err", err)
	}
	if _, err := store.Append(shutdownCtx, DaemonStream, eventstore.AnyVersion, "daemon",
		Stopped{PID: os.Getpid(), Reason: reason}); err != nil {
		log.Warn("could not record daemon.stopped", "err", err)
	}
	log.Info("lucy daemon stopped", "reason", reason)
	if serveErr != nil && !errors.Is(serveErr, net.ErrClosed) {
		return serveErr
	}
	return nil
}

func openStore(ctx context.Context, opts Options) (*eventstore.Store, error) {
	path := opts.DBPath
	if path == "" {
		var err error
		if path, err = eventstore.DefaultPath(); err != nil {
			return nil, err
		}
	}
	reg, err := Registry()
	if err != nil {
		return nil, err
	}
	return eventstore.Open(ctx, path, eventstore.Options{Registry: reg, SnapshotDir: opts.SnapshotDir})
}

// listen creates the socket and makes it 0600. The runtime dir is 0700, so nobody else can reach
// the socket even in the moment before the chmod.
func listen(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("daemon: listen on %s: %w", path, err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false) // Run removes the socket itself, after shutdown
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close() //nolint:errcheck // the chmod error is what matters
		return nil, fmt.Errorf("daemon: chmod %s: %w", path, err)
	}
	return ln, nil
}
