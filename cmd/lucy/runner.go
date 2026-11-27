package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ar4mirez/lucy/internal/daemon"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/harness"
	"github.com/ar4mirez/lucy/internal/harness/claudecode"
	"github.com/ar4mirez/lucy/internal/runner"
)

const runnerUsage = `Usage:
  lucy runner [--socket <path>] [--id <name>] [--root <dir>] [--placement host|container]
              [--ttl <seconds>] [--gateway-dir <dir>] -- <command> [<args>...]
  lucy runner [flags as above] --harness claude-code [-- <harness program> [<args>...]]

Runs a runner in the foreground: it leases runs from the daemon over the runner protocol,
runs <command> once per leased run, streams each line of its output into the daemon's event
log, renews the lease at a third of its life and reports the outcome (exit status 0 is done;
a line "lucy:pr=<n>" reports the pull request). Each run gets a fresh directory under --root
($XDG_DATA_HOME/lucy/runner by default) as its working directory and TMPDIR, and an
environment of only PATH, HOME, TMPDIR and LUCY_RUN, LUCY_RUN_ORG, LUCY_RUN_PROJECT,
LUCY_RUN_FLIGHT, LUCY_RUN_GENERATION, LUCY_RUN_GOAL and LUCY_RUN_BASE; the directory is
deleted when the command exits. A lease not renewed within --ttl (60 s by default) expires:
the run is lost and the daemon refuses anything sent under its fence afterwards. Ctrl-C,
SIGTERM or a closed terminal (SIGHUP) stops the runner; it stops its runs and reports them lost.
A runner started under nohup keeps ignoring SIGHUP and outlives its terminal.

Each leased run carries a capability token for its org's credential gateway. The runner keeps it
in memory and opens the run its own gateway socket, .lucy-gateway.sock in the run's directory and
named in LUCY_RUN_GATEWAY, which forwards to the org's gateway socket in --gateway-dir (gateway/
in lucy's runtime directory by default, where lucy gateway listens) with the token added and any
the run sent dropped. The token itself is never in the run's environment or files.

With --harness claude-code the runner drives Claude Code (` + "`claude`" + ` unless a program follows
--) over its stream-json protocol instead of streaming lines: the harness must report version
` + claudecode.Pinned + `, the one this lucy supports, or the run fails without starting. Each session gets no
MCP servers, no settings file but lucy's own (so neither the user's nor a repository's committed
.claude settings apply: nothing is pre-allowed, no settings file can add a hook, and skills and
slash commands are off) and no saved transcript. Each lifecycle hook the session fires is recorded
as a hook event and never acted on. lucy reads the run directory's CLAUDE.md (or AGENTS.md) and the files it
imports with @path inside that directory, one level deep and size-capped, and appends them to the
system prompt as repository text, which grants no permission; until the worktree pool (m1 slice
6) checks the repository out there, the run directory is empty and nothing is injected. Its
question to the owner is streamed as asking and answered only with what the Tower relays. A
prompted file tool (read, edit or write) on a path inside the run's directory, a shell command
(Bash) and a fetch or search (WebFetch, WebSearch) are decided by the daemon's policy engine, as
the Pilot, and recorded; no rule allows a command or a fetch yet, so those are refused. Every
other permission prompt is refused.
`

// runnerSignalContext is swapped in tests.
var runnerSignalContext = defaultRunnerSignalContext

func defaultRunnerSignalContext() (context.Context, context.CancelFunc) {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	return signal.NotifyContext(context.Background(), sigs...)
}

func runRunner(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	id := fs.String("id", "local", "")
	root := fs.String("root", "", "")
	placement := fs.String("placement", string(runner.PlacementHost), "")
	ttl := fs.Int("ttl", int(runner.DefaultTTL/time.Second), "")
	harnessName := fs.String("harness", "", "")
	gatewayDir := fs.String("gateway-dir", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, runnerUsage)
			return 0
		}
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy runner --help`\n", err)
		return 2
	}
	command := fs.Args()
	var adapter harness.Adapter
	switch *harnessName {
	case "":
	case claudecode.Name:
		adapter = claudecode.Adapter{}
		if len(command) == 0 {
			command = []string{"claude"}
		}
	default:
		fmt.Fprintf(stderr, "error: --harness %q is not a harness lucy supports\nhelp: run `lucy runner --help`; the supported harness is %s\n", *harnessName, claudecode.Name)
		return 2
	}
	if len(command) == 0 {
		fmt.Fprint(stderr, "error: no command to run\nhelp: run `lucy runner --help`; the command follows --\n")
		return 2
	}
	if err := runner.CheckRunnerID(*id); err != nil {
		fmt.Fprintf(stderr, "error: --id %q: %v\nhelp: run `lucy runner --help`\n", *id, err)
		return 2
	}
	if !runner.Placement(*placement).Valid() {
		fmt.Fprintf(stderr, "error: --placement must be host or container, not %q\nhelp: run `lucy runner --help`\n", *placement)
		return 2
	}
	dir := *root
	if dir == "" {
		data, err := eventstore.DataDir()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		dir = filepath.Join(data, "runner")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	gwDir := *gatewayDir
	if gwDir == "" {
		rt, err := daemon.RuntimeDir()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		gwDir = filepath.Join(rt, "gateway")
	}
	if gwDir, err = filepath.Abs(gwDir); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r, err := runner.New(runner.Config{
		ID: *id, Placement: runner.Placement(*placement), Protocol: runner.Remote{Client: c},
		Executor: &runner.ProcessExecutor{Root: dir, Command: command, Harness: adapter, Permitter: runner.Remote{Client: c}, Log: log,
			GatewayDir: gwDir},
		TTL: time.Duration(*ttl) * time.Second, Log: log,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	ctx, cancel := runnerSignalContext()
	defer cancel()
	if adapter != nil {
		log.Info("lucy runner drives a harness", "harness", adapter.Name(), "pinned", adapter.Pinned(), "program", command[0])
	}
	log.Info("lucy runner started", "id", *id, "socket", c.Socket(), "root", dir)
	if err := r.Run(ctx, time.Second); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
