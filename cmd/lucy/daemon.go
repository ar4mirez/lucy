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
	"syscall"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/daemon"
)

const daemonUsage = `Usage:
  lucy daemon [--socket <path>] [--db <path>] [--resources <dir>]   run the daemon in the foreground
  lucy daemon stop [--socket <path>]                                ask the running daemon to stop

The daemon serves lucy's API on a unix socket only ($XDG_RUNTIME_DIR/lucy/lucy.sock by
default; 0600, in a 0700 directory, refusing other users). It opens the event store at
$XDG_DATA_HOME/lucy/lucy.db unless --db names another file. At start it loads the spec
resources in $XDG_CONFIG_HOME/lucy/resources (or --resources) the way ` + "`lucy config validate`" + `
does and records their status in the event store; refused resources are recorded, not
admitted. Stop it with Ctrl-C, SIGTERM or ` + "`lucy daemon stop`" + `.
`

// signalContext is swapped in tests.
var signalContext = defaultSignalContext

func defaultSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runDaemon(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "stop" {
		return runDaemonStop(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	db := fs.String("db", "", "")
	resources := fs.String("resources", "", "")
	if code, ok := parseFlags(fs, args, daemonUsage, "lucy daemon --help", stdout, stderr); !ok {
		return code
	}
	ctx, cancel := signalContext()
	defer cancel()
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	err := daemon.Run(ctx, daemon.Options{SocketPath: *sock, DBPath: *db, ResourcesDir: *resources, Version: version, Log: log})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			fmt.Fprint(stderr, "help: check it with `lucy status`, or stop it with `lucy daemon stop`\n")
		}
		return 1
	}
	return 0
}

func runDaemonStop(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon stop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if code, ok := parseFlags(fs, args, daemonUsage, "lucy daemon --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	if _, err := c.Shutdown(ctx); err != nil {
		return callFailed(err, c, stderr)
	}
	fmt.Fprintln(stdout, "daemon: stopping")
	return 0
}

// parseFlags parses args; ok is false when the caller should return code (help shown, or a usage
// error reported).
func parseFlags(fs *flag.FlagSet, args []string, usage, helpCmd string, stdout, stderr io.Writer) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0, false
		}
		fmt.Fprintf(stderr, "error: %v\nhelp: run `%s`\n", err, helpCmd)
		return 2, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected argument %q\nhelp: run `%s`\n", fs.Arg(0), helpCmd)
		return 2, false
	}
	return 0, true
}

// parseFlagsAllowingArgs parses flags like parseFlags and leaves the arguments after them to the
// caller.
func parseFlagsAllowingArgs(fs *flag.FlagSet, args []string, usage, helpCmd string, stdout, stderr io.Writer) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0, false
		}
		fmt.Fprintf(stderr, "error: %v\nhelp: run `%s`\n", err, helpCmd)
		return 2, false
	}
	return 0, true
}

// dial returns a client for the socket (the default when empty), or nil and an exit code.
func dial(sock string, stderr io.Writer) (*client.Client, int) {
	if sock == "" {
		var err error
		if sock, err = daemon.DefaultSocketPath(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return nil, 1
		}
	}
	return client.New(sock, "lucy-cli", version), 0
}

// callFailed reports a failed API call in the CLI's error format and returns exit code 1.
func callFailed(err error, c *client.Client, stderr io.Writer) int {
	var ae *api.Error
	switch {
	case errors.Is(err, client.ErrNotRunning):
		fmt.Fprintf(stderr, "error: lucy daemon is not running (no daemon at %s)\nhelp: start it with `lucy daemon`\n", c.Socket())
	case errors.As(err, &ae):
		fmt.Fprintf(stderr, "error: %s (%s)\n", ae.Message, ae.Code.Kind())
		if ae.Data != nil && ae.Data.Help != "" {
			fmt.Fprintf(stderr, "help: %s\n", ae.Data.Help)
		}
	default:
		fmt.Fprintf(stderr, "error: %v\n", err)
	}
	return 1
}
