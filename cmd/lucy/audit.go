package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/audit"
)

const auditUsage = `Usage:
  lucy audit verify [--socket <path>]      verify the running daemon's audit chain
  lucy audit verify --file <chain.jsonl>   verify an exported chain; - reads stdin
  lucy audit export [--after <seq>] [--socket <path>]   print the chain as JSON Lines

lucy records every event as an audit entry in the ai-sdlc wire format, hash-chained with
RFC 8785 canonical JSON and SHA-256 (schemaVersion 2). verify recomputes every hash, checks
each entry links to the one before it and, against the daemon, that each entry matches its
event. --file needs no daemon and verifies chains other tools wrote in the same format; a
chain exported with --after verifies only together with the entries before it.
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
	case "export":
		return runAuditExport(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown audit subcommand %q\nhelp: run `lucy audit --help`\n", args[0])
		return 2
	}
}

func runAuditVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	file := fs.String("file", "", "")
	if code, ok := parseFlags(fs, args, auditUsage, "lucy audit --help", stdout, stderr); !ok {
		return code
	}
	if *file != "" {
		return verifyFile(*file, stdout, stderr)
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

func verifyFile(path string, stdout, stderr io.Writer) int {
	var r io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		defer f.Close() //nolint:errcheck // read-only
		r = f
	}
	rep, err := audit.VerifyJSONL(r)
	var ve *audit.VerifyError
	switch {
	case errors.As(err, &ve):
		fmt.Fprintf(stdout, "audit: broken\nverified_entries: %d\nbroken_at_entry: %d\nreason: %s\n", ve.Index-1, ve.Index, ve.Reason)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "audit: verified\nentries: %d\nhead: %s\n", rep.Entries, rep.Head)
	return 0
}

func runAuditExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	after := fs.Int64("after", 0, "")
	if code, ok := parseFlags(fs, args, auditUsage, "lucy audit --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	next := *after
	for {
		ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
		page, err := c.AuditEntries(ctx, api.EntriesParams{AfterSeq: next, Limit: api.MaxEventsLimit})
		cancel()
		if err != nil {
			return callFailed(err, c, stderr)
		}
		for _, e := range page.Entries {
			fmt.Fprintf(stdout, "%s\n", e)
		}
		if !page.More {
			return 0
		}
		next = page.Next
	}
}
