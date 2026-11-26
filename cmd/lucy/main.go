// Command lucy is a headless supervisor for autonomous coding agents.
//
// m0 so far: version, help, self-update, the daemon, the first API clients (status, events),
// config validation and audit chain verification.
// Everything else lands in later m0 slices (see docs/milestones/m0.md).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ar4mirez/lucy/internal/selfupdate"
)

// Set at build time with -ldflags "-X main.version=<v>"; goreleaser sets it (.goreleaser.yaml).
var version = "dev"

const usage = `lucy: a headless supervisor for autonomous coding agents.

Usage:
  lucy [flags]
  lucy version
  lucy self-update [--to <version> | --check]
  lucy daemon [--socket <path>] [--db <path>] [--resources <dir>]
  lucy daemon stop
  lucy status
  lucy events [--after <seq>] [--limit <n>]
  lucy config validate [<dir>]
  lucy config status
  lucy audit verify [--file <chain.jsonl>]
  lucy audit export [--after <seq>]

Flags:
  -h, --help       show this help
  -v, --version    print the version

Commands:
  version        print the version
  self-update    update lucy through mise, which installed it
  daemon         run the daemon: the API on a unix socket, over the event store
  status         show the running daemon's status, through its API
  events         list events from the daemon's log
  config         validate spec resources; show the daemon's view of them
  audit          verify or export the hash-chained audit log

Run ` + "`lucy <command> --help`" + ` for a command's flags.

Status: m0 in progress; only these commands exist so far.
Docs: https://github.com/ar4mirez/lucy
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes lucy with args and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "-v", "--version", "version":
		fmt.Fprintln(stdout, "lucy "+version)
		return 0
	case "self-update":
		return selfupdate.New(version).Run(args[1:], stdout, stderr)
	case "daemon":
		return runDaemon(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "events":
		return runEvents(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "audit":
		return runAudit(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown command or flag %q\nhelp: run `lucy --help`\n", args[0])
		return 2
	}
}
