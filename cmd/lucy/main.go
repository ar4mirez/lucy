// Command lucy is a headless supervisor for autonomous coding agents.
//
// m0 so far: version, help and self-update. Everything else lands in later m0
// slices (see docs/milestones/m0.md).
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

Flags:
  -h, --help       show this help
  -v, --version    print the version

Commands:
  version        print the version
  self-update    update lucy through mise, which installed it

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
	default:
		fmt.Fprintf(stderr, "error: unknown command or flag %q\nhelp: run `lucy --help`\n", args[0])
		return 2
	}
}
