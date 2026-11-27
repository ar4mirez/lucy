package daemon

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/spec"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	if d := t.TempDir(); len(d) < 70 {
		return d
	}
	d, err := os.MkdirTemp("", "lucy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

type running struct {
	sock, db string
	done     chan error
	cancel   context.CancelFunc
}

func opts(dir string) Options {
	return Options{
		SocketPath:   filepath.Join(dir, "run", SocketFile),
		DBPath:       filepath.Join(dir, "data", "lucy.db"),
		SnapshotDir:  filepath.Join(dir, "state"),
		ResourcesDir: filepath.Join(dir, "resources"), // missing: no resources
		Version:      "9.9.9-test",
		Log:          slog.New(slog.DiscardHandler),
	}
}

// startDaemon runs Run in the background and waits until its socket is ready.
func startDaemon(t *testing.T, o Options) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	o.OnReady = func(s string) { ready <- s }
	r := &running{sock: o.SocketPath, db: o.DBPath, done: make(chan error, 1), cancel: cancel}
	go func() { r.done <- Run(ctx, o) }()
	select {
	case <-ready:
	case err := <-r.done:
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon not ready in 10s")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	return r
}

func (r *running) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		r.done <- err // for the cleanup
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop in 10s")
		return nil
	}
}

func mode(t *testing.T, p string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

func TestRunServesThenStopsThroughTheAPI(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	r := startDaemon(t, o)

	if m := mode(t, r.sock); m.Type() != fs.ModeSocket || m.Perm() != 0o600 {
		t.Errorf("socket mode = %v, want a 0600 socket", m)
	}
	if m := mode(t, filepath.Dir(r.sock)); m.Perm() != 0o700 {
		t.Errorf("runtime dir mode = %v, want 0700", m)
	}

	c := client.New(r.sock, "test", "0")
	defer c.Close()
	ctx := context.Background()
	w, err := c.Hello(ctx)
	if err != nil || w.ServerVersion != "9.9.9-test" {
		t.Fatalf("hello = %+v, %v", w, err)
	}
	st, err := c.Status(ctx)
	if err != nil || st.EventHead < 1 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	// The daemon serves the runner protocol; with nothing queued a runner's poll is a read.
	if q, err := c.RunnerQueued(ctx, api.QueuedParams{}); err != nil || q.Queued != 0 {
		t.Fatalf("runner.queued = %+v, %v", q, err)
	}
	if l, err := c.RunnerLease(ctx, api.LeaseParams{Runner: "local", Org: "acme"}); err != nil || l.Leased {
		t.Fatalf("runner.lease = %+v, %v", l, err)
	}

	// A second daemon on the same runtime dir is refused while the first holds the lock.
	if err := Run(ctx, opts(dir)); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second daemon: %v, want ErrAlreadyRunning", err)
	}

	if _, err := c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if _, err := os.Lstat(r.sock); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("socket left behind: %v", err)
	}
	if _, err := c.Status(ctx); !errors.Is(err, client.ErrNotRunning) {
		t.Errorf("status after stop: %v, want ErrNotRunning", err)
	}

	// The log holds the lifecycle and the recorded command, in order.
	reg, _ := Registry()
	store, err := eventstore.Open(ctx, r.db, eventstore.Options{Registry: reg, Projectors: Projectors(), SnapshotDir: o.SnapshotDir})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	evs, err := store.ReadAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	if got := strings.Join(types, ","); got != "config.loaded,daemon.started,api.command,api.command,daemon.stopped" {
		t.Errorf("events = %s", got)
	}
	stopped, err := eventstore.DecodeAs[Stopped](reg, evs[4])
	if err != nil || stopped.Reason != "api" {
		t.Errorf("daemon.stopped = %+v, %v", stopped, err)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	r := startDaemon(t, opts(shortTempDir(t)))
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestRunRemovesAStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	if err := os.MkdirAll(filepath.Dir(o.SocketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", o.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // a dead daemon's socket
	startDaemon(t, o)
}

func TestRunRefuses(t *testing.T) {
	ctx := context.Background()
	t.Run("a runtime dir others can read", func(t *testing.T) {
		o := opts(shortTempDir(t))
		if err := os.MkdirAll(filepath.Dir(o.SocketPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(o.SocketPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := Run(ctx, o); !errors.Is(err, ErrInsecureRuntimeDir) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("a symlinked runtime dir", func(t *testing.T) {
		dir := shortTempDir(t)
		o := opts(dir)
		real := filepath.Join(dir, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Dir(o.SocketPath)); err != nil {
			t.Fatal(err)
		}
		if err := Run(ctx, o); !errors.Is(err, ErrInsecureRuntimeDir) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("a non-socket at the socket path", func(t *testing.T) {
		o := opts(shortTempDir(t))
		if err := os.MkdirAll(filepath.Dir(o.SocketPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(o.SocketPath, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Run(ctx, o); !errors.Is(err, ErrInsecureRuntimeDir) {
			t.Errorf("got %v", err)
		}
		if data, _ := os.ReadFile(o.SocketPath); string(data) != "keep me" {
			t.Error("the daemon removed a file that was not a socket")
		}
	})
	t.Run("a socket path too long for the OS", func(t *testing.T) {
		o := opts(shortTempDir(t))
		o.SocketPath = filepath.Join(filepath.Dir(o.SocketPath), strings.Repeat("s", 120)+".sock")
		if err := Run(ctx, o); !errors.Is(err, ErrSocketPathTooLong) {
			t.Errorf("got %v", err)
		}
	})
}

func TestRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/42")
	if d, err := RuntimeDir(); err != nil || d != "/run/user/42/lucy" {
		t.Errorf("RuntimeDir = %q, %v", d, err)
	}
	if p, err := DefaultSocketPath(); err != nil || p != "/run/user/42/lucy/lucy.sock" {
		t.Errorf("DefaultSocketPath = %q, %v", p, err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "relative")
	if _, err := RuntimeDir(); !errors.Is(err, ErrInsecureRuntimeDir) {
		t.Errorf("relative XDG_RUNTIME_DIR: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if d, err := RuntimeDir(); err != nil || !strings.HasPrefix(filepath.Base(d), "lucy-") {
		t.Errorf("fallback RuntimeDir = %q, %v", d, err)
	}
}

// TestDaemonLoadsConfig checks the daemon admits config through the spec loader, records status
// in the store (not in the files), and chains every event into the audit log.
func TestDaemonLoadsConfig(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	if err := os.MkdirAll(o.ResourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\nspec: {}\n",
		"runway.yaml":   "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web }\nspec: {}\n",
	}
	for name, doc := range files {
		if err := os.WriteFile(filepath.Join(o.ResourcesDir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := startDaemon(t, o)
	c := client.New(r.sock, "test", "0")
	defer c.Close()
	ctx := context.Background()
	page, err := c.Resources(ctx, api.ResourcesParams{})
	if err != nil {
		t.Fatal(err)
	}
	// The runway has no namespace, so it has no identity to carry status: only the airspace does,
	// and the runway's refusal is listed with its source.
	if len(page.Resources) != 1 || page.Resources[0].Kind != "Airspace" || page.Resources[0].Conditions[0].Status != "True" {
		t.Fatalf("resources %+v", page)
	}
	if len(page.Refused) != 1 || page.RefusedTruncated != 0 {
		t.Fatalf("refused %+v", page.Refused)
	}
	ref := page.Refused[0]
	if ref.Source != "runway.yaml#1" || ref.Reason != spec.ReasonMissingNamespace || len(ref.Conditions) != 1 ||
		ref.Conditions[0].Type != spec.ConditionValid || ref.Conditions[0].Status != "False" ||
		ref.Conditions[0].Reason != spec.ReasonMissingNamespace || ref.Conditions[0].Message == "" || ref.Conditions[0].LastTransitionTime == "" {
		t.Fatalf("runway refusal %+v", ref)
	}
	rep, err := c.VerifyAudit(ctx)
	if err != nil || !rep.Verified || rep.Entries < 3 {
		t.Fatalf("audit %+v %v", rep, err)
	}
	for name, doc := range files {
		got, err := os.ReadFile(filepath.Join(o.ResourcesDir, name))
		if err != nil || string(got) != doc {
			t.Fatalf("%s changed: %q %v", name, got, err)
		}
	}
}

func TestDaemonRefusesUnreadableConfig(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	o.ResourcesDir = filepath.Join(dir, "resources.yaml")
	if err := os.WriteFile(o.ResourcesDir, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Run = %v, want a refusal", err)
	}
}

// A runner kept working while the daemon was down for an hour, far past its lease: the daemon
// that starts again resumes the lease, so the runner's heartbeat under its old fence is accepted
// rather than fenced, and the run is not lost (LUCY-OPS-005, LUCY-RUN-005).
func TestRunResumesLeasesHeldWhileDown(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	ctx := context.Background()
	reg, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(ctx, o.DBPath, eventstore.Options{Registry: reg, Projectors: Projectors(), SnapshotDir: o.SnapshotDir})
	if err != nil {
		t.Fatal(err)
	}
	hourAgo := func() time.Time { return time.Now().Add(-time.Hour) }
	b := runner.NewBroker(store, hourAgo)
	if err := b.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	l, ok, err := b.Lease(ctx, "r1", runner.PlacementHost, "acme", "", 0)
	if err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	r := startDaemon(t, o)
	c := client.New(r.sock, "test", "0")
	defer c.Close()
	beat, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "r1", Run: l.Run, Fence: l.Fence})
	if err != nil || !beat.ExpiresAt.After(time.Now()) {
		t.Fatalf("heartbeat under the old fence: %+v, %v", beat, err)
	}
}
