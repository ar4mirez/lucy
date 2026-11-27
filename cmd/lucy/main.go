// Command lucy is a headless supervisor for autonomous coding agents.
//
// So far: version, help, self-update, the daemon, the first API clients (status, events),
// config validation, audit chain verification and the local runner.
// Everything else lands in the m1 slices (see docs/milestones/m1.md).
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
  lucy audit verify
  lucy questions
  lucy answer <flight> <answer>...
  lucy sandbox held | confirm <org> <op> <decision>
  lucy runner [--socket <path>] [--id <name>] [--root <dir>] -- <command> [<args>...]

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
  audit          verify the hash-chained audit log
  runner         run a runner on this machine: lease runs, run a command for each
  sandbox        list and confirm org container operations that wait on you

Run ` + "`lucy <command> --help`" + ` for a command's flags.

Status: m1 in progress; only these commands exist so far.
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
	case "questions":
		return runQuestions(args[1:], stdout, stderr)
	case "answer":
		return runAnswer(args[1:], stdout, stderr)
	case "runner":
		return runRunner(args[1:], stdout, stderr)
	case "sandbox":
		return runSandbox(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown command or flag %q\nhelp: run `lucy --help`\n", args[0])
		return 2
	}
}
