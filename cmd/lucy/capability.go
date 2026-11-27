package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
)

const capabilityUsage = `Usage:
  lucy capability ls [--socket <path>] [--all]
  lucy capability revoke [--socket <path>] <id> [<reason>...]

A run's capability token is what it presents to its org's credential gateway: issued by the daemon
when a runner leases the run, for the org's gateway routes, valid only while that lease is held
and for at most four hours. lucy keeps only each token's digest.

  ls       lists the capabilities not yet past their expiry (--all: every one), newest first, by
           id: the first 16 hex digits of the token's SHA-256, never the token.
  revoke   revokes one by its id; the gateway refuses it from then on.
`

func runCapability(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "error: lucy capability needs ls or revoke\nhelp: run `lucy capability --help`\n")
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, capabilityUsage)
		return 0
	case "ls", "revoke":
	default:
		fmt.Fprintf(stderr, "error: unknown capability command %q\nhelp: run `lucy capability --help`\n", args[0])
		return 2
	}
	fs := flag.NewFlagSet("capability "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	all := fs.Bool("all", false, "")
	if code, ok := parseFlagsAllowingArgs(fs, args[1:], capabilityUsage, "lucy capability --help", stdout, stderr); !ok {
		return code
	}
	if args[0] == "ls" && fs.NArg() != 0 || args[0] == "revoke" && fs.NArg() < 1 {
		fmt.Fprintf(stderr, "error: lucy capability %s takes %s\nhelp: run `lucy capability --help`\n", args[0],
			map[string]string{"ls": "no arguments", "revoke": "an id"}[args[0]])
		return 2
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	if args[0] == "revoke" {
		res, err := c.CapabilityRevoke(ctx, api.RevokeParams{ID: fs.Arg(0), Reason: strings.Join(fs.Args()[1:], " ")})
		if err != nil {
			return callFailed(err, c, stderr)
		}
		fmt.Fprintf(stdout, "revoked: %s\nrun: %s\n", res.ID, untrusted(res.Run))
		return 0
	}
	list, err := c.Capabilities(ctx, *all)
	if err != nil {
		return callFailed(err, c, stderr)
	}
	if len(list.Capabilities) == 0 {
		fmt.Fprint(stdout, "capabilities[0]: none\n")
		return 0
	}
	fmt.Fprintf(stdout, "capabilities[%d]{id,org,run,routes,issued,expires,revoked}:\n", len(list.Capabilities))
	for _, k := range list.Capabilities {
		fmt.Fprintf(stdout, "  %s,%s,%s,%s,%s,%s,%t\n", k.ID, untrusted(k.Org), untrusted(k.Run), csv(strings.Join(k.Routes, " ")),
			k.Issued.Format(time.RFC3339), k.Expires.Format(time.RFC3339), k.Revoked)
	}
	return 0
}
