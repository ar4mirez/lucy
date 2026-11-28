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
	"github.com/ar4mirez/lucy/internal/capability"
	"github.com/ar4mirez/lucy/internal/config"
	"github.com/ar4mirez/lucy/internal/converge"
	"github.com/ar4mirez/lucy/internal/decision"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/gateway"
	"github.com/ar4mirez/lucy/internal/pipeline"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/role"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/sandbox"
	"github.com/ar4mirez/lucy/internal/server"
	"github.com/ar4mirez/lucy/internal/spec"
	"github.com/ar4mirez/lucy/internal/tower"
	"github.com/ar4mirez/lucy/internal/vault"
	"github.com/ar4mirez/lucy/internal/worktree"
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
	payloads = append(append(append(payloads, runner.Payloads()...), sandbox.Payloads()...), gateway.Payloads()...)
	payloads = append(append(append(payloads, capability.Payloads()...), vault.Payloads()...), worktree.Payloads()...)
	payloads = append(payloads, tower.Payloads()...)
	return eventstore.NewRegistry(payloads...)
}

// Projectors returns the projections the daemon's store maintains: resource status, the runner
// protocol's runs and leases, the container operations that wait on the owner, capability tokens,
// the worktree pool's slots, and the audit hash chain, which every event extends in its own append
// transaction.
func Projectors() []eventstore.Projector {
	return []eventstore.Projector{spec.StatusProjector{}, runner.Projector{}, sandbox.Projector{}, capability.Projector{}, worktree.Projector{},
		decision.Projector{}, audit.Projector{}}
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
	// WorktreeRoot holds the worktree pool's clones and slots; worktrees/ in lucy's data directory
	// when empty. WorktreeRemote names a Runway repository's remote (tests point it at local
	// repositories); https://github.com/<owner>/<name>.git when nil.
	WorktreeRoot   string
	WorktreeRemote func(repository string) string
	// WorktreePrepareTimeout bounds one background preparation or release of a run's slot, apart
	// from any API call's deadline; worktree.DefaultPrepareTimeout (30 minutes) when zero. They
	// stop only when this does, or the daemon stops.
	WorktreePrepareTimeout time.Duration
	// WorktreeGit is the git program the pool runs; "git" on PATH when empty (tests wrap it).
	WorktreeGit string
	// CallTimeout bounds one API call; server.DefaultCallTimeout when zero.
	CallTimeout time.Duration
	// Dispatch lets the hosted Tower dispatch the Ready Flights it loads; off by default, so the
	// daemon only flies the flights already in its log. Even on, a Flight whose Runway names a
	// repository with a real remote is refused (ErrTakeoverGate).
	Dispatch bool
	// TowerPollInterval is the longest the hosted Tower goes between steps while a flight is active
	// (a change to the log steps it sooner); tower.DefaultPollInterval when zero.
	TowerPollInterval time.Duration
	Version           string       // lucy's version
	Log               *slog.Logger // slog.Default when nil
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
	stoppedAt, err := lastEvent(ctx, store)
	if err != nil {
		return err
	}

	set, err := loadResources(opts.ResourcesDir)
	if err != nil {
		return err
	}
	gateways, err := gatewayRoutes(store, set, log)
	if err != nil {
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
	// Runners kept working while the daemon was down; before anything is expired, every held
	// lease is moved later by the outage so its runner reconnects under its fence (LUCY-OPS-005,
	// LUCY-RUN-005).
	if n, err := broker.Resume(ctx, stoppedAt); err != nil {
		return fmt.Errorf("daemon: resume runner leases: %w", err)
	} else if n > 0 {
		log.Info("runner leases resumed after a restart", "runs", n, "down", time.Since(stoppedAt).Round(time.Millisecond))
	}
	caps := &capability.Issuer{Store: store, Leases: broker}
	wtRoot := opts.WorktreeRoot
	if wtRoot == "" {
		data, err := eventstore.DataDir()
		if err != nil {
			return err
		}
		wtRoot = filepath.Join(data, "worktrees")
	}
	if wtRoot, err = filepath.Abs(wtRoot); err != nil {
		return err
	}
	worktrees := &worktree.Source{Pool: &worktree.Pool{Store: store, Root: wtRoot, Git: opts.WorktreeGit}, Remote: opts.WorktreeRemote,
		Gate: takeoverGate, Context: ctx, Timeout: opts.WorktreePrepareTimeout, Log: log}
	worktrees.SetRunways(set.Admitted())
	pipelines, err := pipeline.NewCatalog(set.Results)
	if err != nil {
		return fmt.Errorf("daemon: load the pipelines: %w", err)
	}
	// An org Pipeline lucy cannot fly holds that org's Flights of its shape; it never stops the
	// daemon, and the built-in Pipeline never stands in for it (LUCY-SPEC-014).
	for _, err := range pipelines.Invalid() {
		log.Warn("pipeline not flown: its org's Flights of its shape are held", "err", err)
	}
	for org, err := range worktree.PoolErrors(set.Results) {
		log.Warn("worktree pool refused: its org's Flights are held", "org", org, "err", err)
	}
	roles, err := role.NewCatalog(set.Admitted())
	if err != nil {
		return fmt.Errorf("daemon: load the roles: %w", err)
	}
	// A Pilot's prompted tool is decided with its role's blocked paths, relative to its checkout
	// (LUCY-SPEC-015).
	broker.PermitFactsWith(func(ctx context.Context, ri runner.RunInfo) policy.Facts {
		f := policy.Facts{BlockedPaths: roles.BlockedPaths(ri.Org, string(policy.RoleWorker))}
		if p, ok := worktrees.Checkout(ctx, ri.Org, ri.Project, ri.Run); ok {
			f.WorkRoot = p
		}
		return f
	})
	defer func() { stop(nil); worktrees.Wait() }()
	// A preparation or release a stop cut short is settled before anything is leased: a slot left
	// preparing is recorded as stopped, so its run fails and a retry gets a fresh one, and a
	// finished run's slot is released (LUCY-RUN-007).
	if err := worktrees.Recover(ctx, func(ctx context.Context, run string) (bool, error) {
		ri, err := broker.Run(ctx, run)
		if errors.Is(err, runner.ErrNoRun) {
			return true, nil
		}
		return err == nil && ri.State.Final(), err
	}); err != nil {
		return fmt.Errorf("daemon: recover the worktree pool: %w", err)
	}
	gateways.Capabilities = caps
	poll := opts.TowerPollInterval
	if poll <= 0 {
		poll = tower.DefaultPollInterval
	}
	gate := func(org, project string) error {
		remote, ok, err := worktrees.RemoteOf(org, project)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrTakeoverGate, err)
		}
		if !ok {
			return nil
		}
		return takeoverGate(remote)
	}
	hosted, err := startTower(ctx, store, broker, &set, pipelines, poll, log, opts.Dispatch, gate)
	if err != nil {
		return fmt.Errorf("daemon: start the tower: %w", err)
	}
	if err := recordResources(ctx, store, set, log); err != nil {
		return err
	}
	srv, err := server.New(server.Options{
		Store:        store,
		Version:      opts.Version,
		Log:          log,
		OnShutdown:   func() { stop(errAPIStop) },
		Refused:      pl.Refused,
		Runs:         broker,
		Gateways:     gateways,
		Capabilities: caps,
		Worktrees:    worktrees,
		CallTimeout:  opts.CallTimeout,
		Questions:    server.TowerQuestions{Tower: hosted.tw, Lock: &hosted.mu},
		Approvals:    server.TowerQuestions{Tower: hosted.tw, Lock: &hosted.mu},
		Flights:      func() []api.FlightInfo { return hosted.flights(&set) },
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
		expireLeases(ctx, store, broker, caps, log)
	}()
	towering := make(chan struct{})
	go func() {
		defer close(towering)
		hosted.run(ctx, store, poll)
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
	<-towering
	worktrees.Wait()
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

// expireLeases runs the lease converge loop until ctx ends (LUCY-SPEC-008): it expires runner
// leases that were not renewed in time, at least every ExpiryInterval, so a run whose runner went
// silent is lost and its fence refused (LUCY-RUN-001), and it revokes the capability tokens of every
// run no longer leased under them (LUCY-CRED-003). A change to the log wakes it too, so a finished
// run's token is revoked when its end is recorded, not on the next sweep.
func expireLeases(ctx context.Context, store *eventstore.Store, b *runner.Broker, caps *capability.Issuer, log *slog.Logger) {
	converge.Run(ctx, leaseLoop{b: b, caps: caps, log: log}, store, converge.Options{Resync: ExpiryInterval, MinInterval: time.Second,
		MaxBackoff: ExpiryInterval, Log: log})
}

// leaseLoop is the lease converge loop's body: idempotent, since an expired lease or a revoked token
// is not expired or revoked again. Its two sweeps are independent: a failed expiry still revokes
// ended runs' tokens, and a failure never pushes the next sweep past ExpiryInterval.
type leaseLoop struct {
	b    *runner.Broker
	caps *capability.Issuer
	log  *slog.Logger
}

func (leaseLoop) Name() string { return "leases" }

func (l leaseLoop) Reconcile(ctx context.Context) (time.Time, error) {
	var errs []error
	if n, err := l.b.Expire(ctx); err != nil {
		errs = append(errs, fmt.Errorf("expire runner leases: %w", err))
	} else if n > 0 {
		l.log.Info("runner leases expired", "runs", n)
	}
	if n, err := l.caps.RevokeEnded(ctx); err != nil {
		errs = append(errs, fmt.Errorf("revoke ended runs' capabilities: %w", err))
	} else if n > 0 {
		l.log.Info("capabilities of ended runs revoked", "capabilities", n)
	}
	return time.Now().Add(ExpiryInterval), errors.Join(errs...)
}

// lastEvent returns the time of the newest event in the log, before this start appends any: when
// the last daemon stopped, recording daemon.stopped, or, had it crashed, no earlier than its last
// record, which a live lease's heartbeats keep recent. It is zero for an empty log.
func lastEvent(ctx context.Context, store *eventstore.Store) (time.Time, error) {
	head, err := store.Head(ctx)
	if err != nil || head == 0 {
		return time.Time{}, err
	}
	evs, err := store.ReadAfter(ctx, head-1, 1)
	if err != nil || len(evs) == 0 {
		return time.Time{}, err
	}
	return evs[0].Time, nil
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
// (spec.LoadDir). Refused resources do not stop the daemon: recordResources records them as
// Valid=False and they are not admitted. A directory that cannot be read at all is an error.
func loadResources(dir string) (spec.Set, error) {
	if dir == "" {
		var err error
		if dir, err = config.ResourcesDir(); err != nil {
			return spec.Set{}, err
		}
	}
	set, err := spec.LoadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		set = spec.Set{Dir: dir}
	case err != nil:
		return spec.Set{}, fmt.Errorf("daemon: %w", err)
	}
	return set, nil
}

// recordResources records each loaded resource's status in the event store, never in the files
// (LUCY-SPEC-003), once the hosted Tower has added each Ready Flight's Dispatched condition. Only
// a changed status is appended.
func recordResources(ctx context.Context, store *eventstore.Store, set spec.Set, log *slog.Logger) error {
	sum, err := spec.Record(ctx, store, "daemon", set)
	if err != nil {
		return fmt.Errorf("daemon: record config status: %w", err)
	}
	level := slog.LevelInfo
	if set.Invalid() > 0 {
		level = slog.LevelWarn
	}
	log.Log(ctx, level, "config loaded", "dir", set.Dir, "resources", len(set.Results), "refused", set.Invalid(),
		"changed", sum.Changed, "removed", sum.Removed)
	return nil
}

// gatewayRoutes reads each org's credential gateway routes from its SecretStore binding. A binding
// that does not read is logged and left out whole, so that org's gateway forwards nothing.
func gatewayRoutes(store *eventstore.Store, set spec.Set, log *slog.Logger) (*gateway.StoreRecorder, error) {
	byOrg, errs := gateway.Bindings(set.Admitted())
	for _, err := range errs {
		log.Warn("gateway binding refused", "error", err)
	}
	rec := &gateway.StoreRecorder{Store: store}
	if err := rec.SetRoutes(byOrg); err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}
	return rec, nil
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
