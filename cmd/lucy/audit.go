package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/ar4mirez/lucy/internal/api/client"
)

const auditUsage = `Usage:
  lucy audit verify [--socket <path>]   verify the running daemon's audit chain

lucy records every event as an audit entry in the ai-sdlc wire format, hash-chained with
RFC 8785 canonical JSON and SHA-256 (schemaVersion 2). verify recomputes every hash, checks
each entry links to the one before it and that each entry matches its event.
Exits 0 when the chain verifies, 1 when it is broken or cannot be read.
`

func runAudit(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "error: missing subcommand\nhelp: run `lucy audit --help`\n")
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, auditUsage)
		return 0
	case "verify":
		return runAuditVerify(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown audit subcommand %q\nhelp: run `lucy audit --help`\n", args[0])
		return 2
	}
}

func runAuditVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if code, ok := parseFlags(fs, args, auditUsage, "lucy audit --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	rep, err := c.VerifyAudit(ctx)
	if err != nil {
		return callFailed(err, c, stderr)
	}
	if !rep.Verified {
		fmt.Fprintf(stdout, "audit: broken\nverified_entries: %d\nbroken_at_seq: %d\nreason: %s\n", rep.Entries, rep.BrokenAtSeq, rep.Reason)
		fmt.Fprint(stdout, "help: the event log or the audit table was changed outside lucy; keep the database file for inspection\n")
		return 1
	}
	fmt.Fprintf(stdout, "audit: verified\nentries: %d\nhead: %s\n", rep.Entries, rep.Head)
	return 0
}
