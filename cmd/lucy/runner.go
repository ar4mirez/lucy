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

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/runner"
)

const runnerUsage = `Usage:
  lucy runner [--socket <path>] [--id <name>] [--root <dir>] [--placement host|container]
              [--ttl <seconds>] -- <command> [<args>...]

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
`

// runnerSignalContext is swapped in tests.
var runnerSignalContext = defaultRunnerSignalContext

func defaultRunnerSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}

func runRunner(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	id := fs.String("id", "local", "")
	root := fs.String("root", "", "")
	placement := fs.String("placement", string(runner.PlacementHost), "")
	ttl := fs.Int("ttl", int(runner.DefaultTTL/time.Second), "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, runnerUsage)
			return 0
		}
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy runner --help`\n", err)
		return 2
	}
	command := fs.Args()
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
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r, err := runner.New(runner.Config{
		ID: *id, Placement: runner.Placement(*placement), Protocol: runner.Remote{Client: c},
		Executor: &runner.ProcessExecutor{Root: dir, Command: command, Log: log},
		TTL:      time.Duration(*ttl) * time.Second, Log: log,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	ctx, cancel := runnerSignalContext()
	defer cancel()
	log.Info("lucy runner started", "id", *id, "socket", c.Socket(), "root", dir)
	if err := r.Run(ctx, time.Second); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
