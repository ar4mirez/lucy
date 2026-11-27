package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
)

const sandboxUsage = `Usage:
  lucy sandbox held [--socket <path>]
  lucy sandbox confirm [--socket <path>] <org> <op> <decision>

Starting, restarting or stopping an org's container (up, restart, down) stops every session
running in it, so while a session is live there lucy holds the operation for you.

  held      lists each container operation that waits on you: the org, the operation, the
            decision that waits, the sessions it would stop and when it was decided, oldest first.
  confirm   confirms stopping exactly the sessions that decision named. lucy's next request for
            the operation is then cleared, once, while those sessions are unchanged; if a session
            starts or ends first, the confirmation lapses and the operation waits on you again.
            A newer decision replaces the one you confirm: run held again and confirm that one.

The daemon does not open the org's Sandbox binding yet (m1 slice 3), so in production nothing is
held and a confirmation is reported unavailable.
`

func runSandbox(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "error: lucy sandbox needs held or confirm\nhelp: run `lucy sandbox --help`\n")
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, sandboxUsage)
		return 0
	case "held":
		return runSandboxHeld(args[1:], stdout, stderr)
	case "confirm":
		return runSandboxConfirm(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "error: unknown sandbox command %q\nhelp: run `lucy sandbox --help`\n", args[0])
	return 2
}

func runSandboxHeld(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sandbox held", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if code, ok := parseFlags(fs, args, sandboxUsage, "lucy sandbox --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	list, err := c.SandboxHeld(ctx)
	if err != nil {
		return callFailed(err, c, stderr)
	}
	writeHeld(stdout, list)
	return 0
}

func writeHeld(w io.Writer, list api.HeldList) {
	if len(list.Held) == 0 {
		fmt.Fprint(w, "held[0]: no container operation waits on you\n")
		return
	}
	fmt.Fprintf(w, "held[%d]{org,op,decision,outcome,since,live_sessions}:\n", len(list.Held))
	for _, h := range list.Held {
		live := make([]string, len(h.LiveSessions))
		for i, s := range h.LiveSessions {
			live[i] = untrusted(s)
		}
		fmt.Fprintf(w, "  %s,%s,%d,%s,%s,%s\n", untrusted(h.Org), untrusted(h.Op), h.Decision, untrusted(h.Outcome),
			h.Since.Format(time.RFC3339), csv(strings.Join(live, " ")))
	}
	fmt.Fprint(w, "help: run `lucy sandbox confirm <org> <op> <decision>` to confirm stopping those sessions\n")
}

func runSandboxConfirm(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sandbox confirm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, sandboxUsage)
			return 0
		}
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy sandbox --help`\n", err)
		return 2
	}
	if fs.NArg() != 3 {
		fmt.Fprint(stderr, "error: lucy sandbox confirm needs an org, an operation and a decision\nhelp: run `lucy sandbox --help`; `lucy sandbox held` lists what waits on you\n")
		return 2
	}
	decision, err := strconv.ParseInt(fs.Arg(2), 10, 64)
	if err != nil || decision < 1 {
		fmt.Fprintf(stderr, "error: the decision must be a positive number, not %q\nhelp: run `lucy sandbox held`\n", fs.Arg(2))
		return 2
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	res, err := c.SandboxConfirm(ctx, api.ConfirmParams{Org: fs.Arg(0), Op: fs.Arg(1), Decision: decision})
	if err != nil {
		return callFailed(err, c, stderr)
	}
	fmt.Fprintf(stdout, "confirmed: %s %s\napproval: %s\n", res.Op, res.Org, res.Approval)
	return 0
}
