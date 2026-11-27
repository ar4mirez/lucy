// Package daemon runs `lucy daemon`: it opens the event store, serves the API on a unix socket in
// lucy's runtime directory and stops cleanly on a signal or a daemon.shutdown call.
//
// The socket is the only listener. It lives in a 0700 runtime directory, is itself 0600, and admits
// only connections whose kernel-reported peer runs as this user (LUCY-API-003). No network listener
// exists; the HTTPS transport for remote surfaces lands in m1 with device pairing and TLS
// (LUCY-VIEW-006).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/audit"
	"github.com/ar4mirez/lucy/internal/config"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/sandbox"
	"github.com/ar4mirez/lucy/internal/server"
	"github.com/ar4mirez/lucy/internal/spec"
)

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
	payloads := append(spec.Payloads(), Started{}, Stopped{}, server.CommandEvent{}, policy.DecisionRecord{})
	return eventstore.NewRegistry(append(append(payloads, runner.Payloads()...), sandbox.Payloads()...)...)
}

// Projectors returns the projections the daemon's store maintains: resource status, the runner
// protocol's runs and leases, the container operations that wait on the owner, and the audit hash
// chain, which every event extends in its own append transaction.
func Projectors() []eventstore.Projector {
	return []eventstore.Projector{spec.StatusProjector{}, runner.Projector{}, sandbox.Projector{}, audit.Projector{}}
}

// ExpiryInterval is how often the daemon expires runner leases that were not renewed in time.
const ExpiryInterval = 5 * time.Second

// Options configures Run.
type Options struct {
	SocketPath  string // DefaultSocketPath when empty
	DBPath      string // eventstore.DefaultPath when empty
	SnapshotDir string // the store's pre-migration snapshot dir; its default when empty
	// ResourcesDir holds the spec resources the daemon loads at start; config.ResourcesDir when
	// empty. A missing directory means no resources.
	ResourcesDir string
	Version      string       // lucy's version
	Log          *slog.Logger // slog.Default when nil
	// OnReady is called once the socket accepts connections.
	OnReady func(socketPath string)
}

// Run serves the API until ctx is cancelled or a daemon.shutdown call, then stops gracefully.
func Run(ctx context.Context, opts Options) error {
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

	if err := loadResources(ctx, store, opts.ResourcesDir, log); err != nil {
		return err
	}

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
	broker := runner.NewBroker(store, nil)
	srv, err := server.New(server.Options{
		Store:      store,
		Version:    opts.Version,
		Log:        log,
		OnShutdown: func() { stop(errAPIStop) },
		Refused:    pl.Refused,
		Runs:       broker,
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
	expiring := make(chan struct{})
	go func() {
		defer close(expiring)
		expireLeases(ctx, broker, log)
	}()
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
	stop(nil)
	<-expiring
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

// expireLeases expires runner leases that were not renewed in time, every ExpiryInterval, until
// ctx ends. A run whose runner went silent is then lost, and its fence refused (LUCY-RUN-001).
func expireLeases(ctx context.Context, b *runner.Broker, log *slog.Logger) {
	t := time.NewTicker(ExpiryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n, err := b.Expire(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("could not expire runner leases", "err", err)
		case n > 0:
			log.Info("runner leases expired", "runs", n)
		}
	}
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
	return eventstore.Open(ctx, path, eventstore.Options{Registry: reg, Projectors: Projectors(), SnapshotDir: opts.SnapshotDir})
}

// loadResources loads the config directory through the same path as `lucy config validate`
// (spec.LoadDir) and records each resource's status in the event store, never in the files
// (LUCY-SPEC-003). Refused resources do not stop the daemon: they are recorded as Valid=False
// and are not admitted. A directory that cannot be read at all is an error.
func loadResources(ctx context.Context, store *eventstore.Store, dir string, log *slog.Logger) error {
	if dir == "" {
		var err error
		if dir, err = config.ResourcesDir(); err != nil {
			return err
		}
	}
	set, err := spec.LoadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		set = spec.Set{Dir: dir}
	case err != nil:
		return fmt.Errorf("daemon: %w", err)
	}
	sum, err := spec.Record(ctx, store, "daemon", set)
	if err != nil {
		return fmt.Errorf("daemon: record config status: %w", err)
	}
	level := slog.LevelInfo
	if set.Invalid() > 0 {
		level = slog.LevelWarn
	}
	log.Log(ctx, level, "config loaded", "dir", dir, "resources", len(set.Results), "refused", set.Invalid(),
		"changed", sum.Changed, "removed", sum.Removed)
	return nil
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
		_ = ln.Close() // the chmod error is what matters
		return nil, fmt.Errorf("daemon: chmod %s: %w", path, err)
	}
	return ln, nil
}
