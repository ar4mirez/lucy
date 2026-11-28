package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/decision"
)

const decisionsUsage = `Usage:
  lucy decisions [--socket <path>] [--all] [--json]

Lists the decisions that wait on you, oldest first: a Pilot's question (settle it with lucy
answer), a merge that holds for your approval (lucy approve) and an operation on an org's
container that holds until you confirm the sessions it stops (lucy sandbox confirm). Each is
projected from the event log; only your recorded word closes one, nothing expires it and no agent
can answer it. --all lists the answered, superseded and archived ones too. --json prints each as
the spec's Decision resource (ai-sdlc.io/v1alpha1, RFC-0035), one JSON object per line.
`

const approveUsage = `Usage:
  lucy approve [--socket <path>] <flight>

Approves, as you, the merge a flight holds for. The Tower asks the policy engine again with your
approval, bound to the facts it holds now, so an approval lapses when the pull request's head or
base moves. lucy decisions lists the merges that wait on you.
`

func runDecisions(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("decisions", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	all := fs.Bool("all", false, "")
	asJSON := fs.Bool("json", false, "")
	if code, ok := parseFlags(fs, args, decisionsUsage, "lucy decisions --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	var list api.CatalogList
	if err := c.Call(ctx, api.MethodDecList, api.CatalogParams{All: *all}, &list); err != nil {
		return callFailed(err, c, stderr)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		for _, d := range list.Decisions {
			if err := enc.Encode(resourceOf(d)); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
		}
		return 0
	}
	if len(list.Decisions) == 0 {
		fmt.Fprint(stdout, "decisions[0]: nothing waits on you\n")
		return 0
	}
	fmt.Fprintf(stdout, "decisions[%d]{id,lifecycle,scope,opened,summary,settle}:\n", len(list.Decisions))
	for _, d := range list.Decisions {
		settle := ""
		if len(d.Options) > 0 {
			settle = d.Options[0].Description
		}
		fmt.Fprintf(stdout, "  %s,%s,%s,%s,%s,%s\n", d.ID, d.Lifecycle, untrusted(d.Scope), d.Opened.Format(time.RFC3339), untrusted(d.Summary), untrusted(settle))
		if d.Body != "" {
			fmt.Fprintf(stdout, "    asks: %s\n", untrusted(d.Body))
		}
		if d.ProposedBy != "" {
			fmt.Fprintf(stdout, "    suggested by %s (proposal %s; accept it as `lucy questions` shows it): %s%s\n", untrusted(d.ProposedBy),
				untrusted(d.ProposalID), untrusted(d.Proposed), cutShort(d.ProposalTruncated))
		}
	}
	return 0
}

// resourceOf is a catalog decision in the spec's Decision shape.
func resourceOf(d api.CatalogDecision) decision.Resource {
	var seq int64
	_, _ = fmt.Sscanf(d.ID, "DEC-%d", &seq) // the id is the daemon's own, DEC-<seq>
	var sup int64
	if d.SupersededBy != "" {
		_, _ = fmt.Sscanf(d.SupersededBy, "DEC-%d", &sup)
	}
	return decision.Row{Seq: seq, Kind: d.Kind, Org: d.Org, Subject: d.Subject, Ref: d.Ref, Summary: d.Summary, Body: d.Body,
		Lifecycle: d.Lifecycle, AnsweredBy: d.AnsweredBy, AnsweredAt: d.AnsweredAt, SupersededBy: sup, Opened: d.Opened,
		Updated: d.Updated}.Resource()
}

func runApprove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if code, ok := parseFlagsAllowingArgs(fs, args, approveUsage, "lucy approve --help", stdout, stderr); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "error: lucy approve needs one flight\nhelp: run `lucy approve --help`; `lucy decisions` lists the merges that wait on you\n")
		return 2
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	var res api.ApproveResult
	if err := c.Call(ctx, api.MethodApprove, api.ApproveParams{Flight: fs.Arg(0)}, &res); err != nil {
		return callFailed(err, c, stderr)
	}
	fmt.Fprintf(stdout, "approved: %s\nstate: %s\n", res.Flight, res.State)
	return 0
}
