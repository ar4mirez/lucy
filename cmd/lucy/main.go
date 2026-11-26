// Command lucy is a headless supervisor for autonomous coding agents.
//
// This is the m0 skeleton: it prints its version and help. Everything else
// lands in later m0 slices (see docs/milestones/m0.md).
package main

import (
	"fmt"
	"io"
	"os"
)

// version is set at build time with -ldflags "-X main.version=<v>".
var version = "dev"

const usage = `lucy: a headless supervisor for autonomous coding agents.

Usage:
  lucy [flags]
  lucy version

Flags:
  -h, --help       show this help
  -v, --version    print the version

Status: m0 in progress; only version and help exist so far.
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
		fmt.Fprintf(stdout, "lucy %s\n", version)
		return 0
	default:
		fmt.Fprintf(stderr, "error: unknown command or flag %q\nhelp: run `lucy --help`\n", args[0])
		return 2
	}
}
