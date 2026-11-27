package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/runner"
)

// The default runner id is a valid id that names the process, for any org, however long.
func TestDefaultRunnerID(t *testing.T) {
	pid := "-" + strconv.Itoa(os.Getpid())
	for _, org := range []string{"acme", "Ar4mirez", "_x", "", strings.Repeat("o", 200)} {
		id := defaultRunnerID(org)
		if err := runner.CheckRunnerID(id); err != nil || !strings.HasSuffix(id, pid) {
			t.Errorf("org %q: id %q, %v", org, id, err)
		}
	}
	if a, b := defaultRunnerID("acme"), defaultRunnerID("beta"); a == b {
		t.Errorf("two orgs share the id %q", a)
	}
}

// A --root that is short as written but resolves through a symlink to a path too long for a run's
// gateway socket is refused before the runner starts.
func TestRunnerRefusesARootTooLongOnceResolved(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 90))
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	links, err := os.MkdirTemp("", "l")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(links) })
	link := filepath.Join(links, "r")
	if err := os.Symlink(long, link); err != nil {
		t.Fatal(err)
	}
	if err := runner.CheckRunGatewayRoot(link); err != nil {
		t.Fatalf("the link itself is short enough: %v", err)
	}
	code, _, errOut := lucy("runner", "--org", "acme", "--root", link, "--socket", filepath.Join(links, "none.sock"), "--", "true")
	if code != 2 || !strings.Contains(errOut, "--root") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

// A runner that declares container placement is refused before it dials the daemon, naming the
// proof it lacks.
func TestRunnerRefusesSelfDeclaredContainerPlacement(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "none.sock")
	code, _, errOut := lucy("runner", "--org", "acme", "--placement", "container", "--socket", sock, "--", "true")
	if code != 2 || !strings.Contains(errOut, "container placement needs berth#216 placement proof") || strings.Contains(errOut, "not running") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

// A runner for one org refuses a root another org's runner has used, before it dials the daemon.
func TestRunnerRefusesAnotherOrgsRoot(t *testing.T) {
	// The temp dir itself is the root: a run's gateway socket path must stay short, and on macOS
	// the resolved temp dir (/private/var/folders/...) leaves no room for a subdirectory.
	root, err := os.MkdirTemp("", "l")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	sock := filepath.Join(root, "none.sock")
	if err := runner.ClaimRoot(root, "acme"); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := lucy("runner", "--org", "beta", "--root", root, "--socket", sock, "--", "true")
	if code != 2 || !strings.Contains(errOut, `bound to org "acme"`) || !strings.Contains(errOut, "help:") {
		t.Fatalf("beta: code %d stderr %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "home")); !os.IsNotExist(err) {
		t.Fatalf("beta's runner made a home in acme's root: %v", err)
	}
}

// lucy runner drain signals only the live runners that match, then waits until each has exited.
func TestRunnerDrain(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	reg, err := runnerRegistry()
	if err != nil {
		t.Fatal(err)
	}
	releases := map[string]func() error{}
	for _, r := range []struct{ id, org string }{{"acme-1", "acme"}, {"acme-2", "acme"}, {"beta-1", "beta"}} {
		release, err := runner.Register(reg, r.id, r.org)
		if err != nil {
			t.Fatal(err)
		}
		releases[r.id] = release
	}
	t.Cleanup(func() {
		for _, rel := range releases {
			_ = rel() // a release already done fails; that is fine here
		}
	})
	var signalled []int
	drainSignal, drainPoll = func(pid int) error { signalled = append(signalled, pid); return nil }, time.Millisecond
	t.Cleanup(func() {
		drainSignal = func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
		drainPoll = 500 * time.Millisecond
	})

	var out, errb bytes.Buffer
	if code := runRunner([]string{"drain", "--org", "acme", "--no-wait"}, &out, &errb); code != 0 {
		t.Fatalf("drain --no-wait: %d, %s", code, errb.String())
	}
	if len(signalled) != 2 || signalled[0] != os.Getpid() || !strings.Contains(out.String(), "draining runner acme-1 of org acme") ||
		!strings.Contains(out.String(), "draining runner acme-2") || strings.Contains(out.String(), "beta") {
		t.Fatalf("signalled %v, output %q", signalled, out.String())
	}

	// Waiting: the runner exits (its registration is released) after the signal.
	signalled = nil
	drainSignal = func(pid int) error {
		signalled = append(signalled, pid)
		go func() { time.Sleep(20 * time.Millisecond); _ = releases["beta-1"]() }()
		return nil
	}
	out.Reset()
	if code := runRunner([]string{"drain", "--id", "beta-1"}, &out, &errb); code != 0 {
		t.Fatalf("drain --id: %d, %s", code, errb.String())
	}
	if len(signalled) != 1 || !strings.Contains(out.String(), "runner beta-1 drained and exited") {
		t.Fatalf("signalled %v, output %q", signalled, out.String())
	}

	// A runner that exited between the listing and the signal: reported, not waited for.
	drainSignal = func(pid int) error { return syscall.ESRCH }
	out.Reset()
	errb.Reset()
	if code := runRunner([]string{"drain", "--org", "acme"}, &out, &errb); code != 0 ||
		!strings.Contains(out.String(), "runner acme-1 already exited") || !strings.Contains(out.String(), "runner acme-2 already exited") {
		t.Fatalf("already exited: %d, %q, %q", code, out.String(), errb.String())
	}
	drainSignal = func(pid int) error { return syscall.EPERM }
	if code := runRunner([]string{"drain", "--org", "acme"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "drain runner acme-1") {
		t.Fatalf("a signal error: %d, %q", code, errb.String())
	}

	errb.Reset()
	if code := runRunner([]string{"drain", "--org", "gamma"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "no live runner matches") {
		t.Fatalf("no match: %d, %q", code, errb.String())
	}
	if code := runRunner([]string{"drain", "extra"}, &out, &errb); code != 2 {
		t.Fatalf("an argument: %d", code)
	}
}

// The first SIGTERM drains the runner and keeps it running; a second one stops it, as Ctrl-C or
// SIGHUP does at once.
func TestRunnerSignals(t *testing.T) {
	for _, c := range []struct {
		name   string
		sigs   []os.Signal
		drain  bool
		cancel bool
	}{
		{"SIGTERM drains", []os.Signal{syscall.SIGTERM}, true, false},
		{"a second SIGTERM stops", []os.Signal{syscall.SIGTERM, syscall.SIGTERM}, true, true},
		{"Ctrl-C stops", []os.Signal{os.Interrupt}, false, true},
		{"SIGHUP stops", []os.Signal{syscall.SIGHUP}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var feed chan<- os.Signal
			runnerSignals = func(ch chan<- os.Signal) func() { feed = ch; return func() {} }
			t.Cleanup(func() { runnerSignals = defaultRunnerSignals })
			r, err := runner.New(runner.Config{ID: "r1", Org: "acme", Protocol: idleProtocol{}, Executor: noExecutor{},
				Log: slog.New(slog.DiscardHandler)})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			stop := watchRunnerSignals(ctx, r, cancel, slog.New(slog.DiscardHandler))
			for _, s := range c.sigs {
				feed <- s
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) && (r.Draining() != c.drain || (ctx.Err() != nil) != c.cancel) {
				time.Sleep(time.Millisecond)
			}
			if r.Draining() != c.drain || (ctx.Err() != nil) != c.cancel {
				t.Fatalf("draining %v, stopped %v", r.Draining(), ctx.Err() != nil)
			}
			stop()
		})
	}
}

type idleProtocol struct{}

func (idleProtocol) Queued(context.Context, runner.Placement, string) (int, error) { return 0, nil }
func (idleProtocol) Lease(context.Context, string, runner.Placement, string, string, time.Duration) (runner.Lease, bool, error) {
	return runner.Lease{}, false, nil
}
func (idleProtocol) Heartbeat(context.Context, string, string, int64, time.Duration, []runner.Event, string) (runner.Beat, error) {
	return runner.Beat{}, runner.ErrFenced
}
func (idleProtocol) Finish(context.Context, string, string, int64, runner.Result) error { return nil }

type noExecutor struct{}

func (noExecutor) Start(context.Context, runner.Lease) (runner.Execution, error) {
	return nil, errors.New("no work in this test")
}
