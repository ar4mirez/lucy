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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/daemon"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/harness"
	"github.com/ar4mirez/lucy/internal/harness/claudecode"
	"github.com/ar4mirez/lucy/internal/runner"
)

const runnerUsage = `Usage:
  lucy runner --org <org> [--socket <path>] [--id <name>] [--root <dir>]
              [--placement host|container] [--ttl <seconds>] [--gateway-dir <dir>]
              -- <command> [<args>...]
  lucy runner [flags as above] --harness claude-code [-- <harness program> [<args>...]]
  lucy runner host-org [--socket <path>] <org>

Runs a runner in the foreground: it leases runs from the daemon over the runner protocol,
runs <command> once per leased run, streams each line of its output into the daemon's event
log, renews the lease at a third of its life and reports the outcome (exit status 0 is done;
a line "lucy:pr=<n>" reports the pull request). A runner on the host (the default placement)
needs --org, which every runner does, and is leased only that org's runs. Every process on the
host runs as one uid and can reach the others' sockets, so the daemon serves one host org: the
first host lease sets it, and a runner for any other org is refused (host_org) and exits. lucy
runner host-org <org> changes the host org, recorded, and is refused while any host run is live.
--placement container is refused: a placement a runner declares is never trusted, and container
placement needs a placement proof berth cannot issue yet (berth#216). --id defaults to one
unique to the process, built from the org, the host name and the pid. Each run gets a fresh directory under --root ($XDG_DATA_HOME/lucy/runner/<org> by default) as its
working directory and TMPDIR, and an
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
the run sent dropped. The runner never puts the token itself in the run's environment or files. A Unix socket
path is at most 107 bytes on Linux and 103 on macOS, so the runner refuses to start under a
--root too long for a run's socket, once symlinks in it are resolved, and names the longest it
can take.

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

// defaultRunnerID is a runner id unique to this process: the org, the host name and the pid, each
// reduced to the id alphabet and cut to fit.
func defaultRunnerID(org string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
				return r
			case r >= 'A' && r <= 'Z':
				return r + ('a' - 'A')
			}
			return '-'
		}, s)
	}
	host, _ := os.Hostname()
	id := strings.TrimLeft(clean(org)+"-"+clean(host), "._-")
	if id == "" {
		id = "runner"
	}
	pid := "-" + strconv.Itoa(os.Getpid())
	if len(id) > 63-len(pid) {
		id = id[:63-len(pid)]
	}
	return id + pid
}

func runRunner(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "host-org" {
		return runRunnerHostOrg(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("runner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	id := fs.String("id", "", "")
	org := fs.String("org", "", "")
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
	if *id == "" {
		*id = defaultRunnerID(*org)
	}
	if err := runner.CheckRunnerID(*id); err != nil {
		fmt.Fprintf(stderr, "error: --id %q: %v\nhelp: run `lucy runner --help`\n", *id, err)
		return 2
	}
	if !runner.Placement(*placement).Valid() {
		fmt.Fprintf(stderr, "error: --placement must be host or container, not %q\nhelp: run `lucy runner --help`\n", *placement)
		return 2
	}
	if runner.Placement(*placement) == runner.PlacementContainer {
		fmt.Fprint(stderr, "error: container placement needs berth#216 placement proof\nhelp: run `lucy runner --help`; a runner placed on the host serves the host org\n")
		return 2
	}
	if *org == "" {
		fmt.Fprint(stderr, "error: lucy runner needs --org: a runner leases the runs of one org\nhelp: run `lucy runner --help`\n")
		return 2
	}
	if *org != filepath.Base(*org) || strings.HasPrefix(*org, ".") || strings.Contains(*org, "#") || len(*org) > 200 {
		fmt.Fprintf(stderr, "error: --org %q is not an org name\nhelp: run `lucy runner --help`\n", *org)
		return 2
	}
	dir := *root
	if dir == "" {
		data, err := eventstore.DataDir()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		dir = filepath.Join(data, "runner", *org)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if err := runner.CheckRunGatewayRoot(dir); err != nil {
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy runner --help`\n", err)
		return 2
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(dir); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	} else if err := runner.CheckRunGatewayRoot(resolved); err != nil {
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy runner --help`\n", err)
		return 2
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
		ID: *id, Placement: runner.Placement(*placement), Org: *org, Protocol: runner.Remote{Client: c},
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

// runRunnerHostOrg is lucy runner host-org: the owner's command that changes the host org.
func runRunnerHostOrg(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runner host-org", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if code, ok := parseFlagsAllowingArgs(fs, args, runnerUsage, "lucy runner --help", stdout, stderr); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "error: lucy runner host-org takes one org\nhelp: run `lucy runner --help`\n")
		return 2
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	res, err := c.RunnerHostOrg(ctx, api.HostOrgParams{Org: fs.Arg(0)})
	if err != nil {
		return callFailed(err, c, stderr)
	}
	fmt.Fprintf(stdout, "host org: %s\n", untrusted(res.Org))
	return 0
}
