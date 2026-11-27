package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
)

const worktreeUsage = `Usage:
  lucy worktree ls [--socket <path>] [--org <org>]
  lucy worktree discard [--socket <path>] <org>/<repo>/<slot> --confirm <org>/<repo>/<slot>

The worktree pool holds each repository's slots: the checkouts runs work in. A run's slot is
given back when its work has landed (pushed to the remote, merged, or nothing beyond its base);
otherwise it is quarantined, its work kept, and the pool grows a new slot for the next run.

  ls       lists every slot (of --org only): its state, the run that holds or last held it, its
           branch, and why it is held when quarantined (uncommitted changes, commits on no remote
           ref, or its work could not be checked) or broken.
  discard  removes a quarantined or broken slot's directory, recorded first. --confirm repeats
           the slot exactly. Its branch stays in the repository's clone, so its commits are
           recoverable until git gc; work never committed is lost. A free or leased slot is
           refused.
`

func runWorktree(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "error: lucy worktree needs ls or discard\nhelp: run `lucy worktree --help`\n")
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, worktreeUsage)
		return 0
	case "ls", "discard":
	default:
		fmt.Fprintf(stderr, "error: unknown worktree command %q\nhelp: run `lucy worktree --help`\n", args[0])
		return 2
	}
	fs := flag.NewFlagSet("worktree "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	org := fs.String("org", "", "")
	confirm := fs.String("confirm", "", "")
	if code, ok := parseFlagsAllowingArgs(fs, args[1:], worktreeUsage, "lucy worktree --help", stdout, stderr); !ok {
		return code
	}
	slot := ""
	if args[0] == "discard" && fs.NArg() > 0 {
		// The slot comes before --confirm: parse the flags after it too.
		slot = fs.Arg(0)
		if code, ok := parseFlagsAllowingArgs(fs, fs.Args()[1:], worktreeUsage, "lucy worktree --help", stdout, stderr); !ok {
			return code
		}
	}
	switch {
	case args[0] == "ls" && (fs.NArg() != 0 || *confirm != ""):
		fmt.Fprint(stderr, "error: lucy worktree ls takes no arguments and no --confirm\nhelp: run `lucy worktree --help`\n")
		return 2
	case args[0] == "discard" && (slot == "" || fs.NArg() != 0 || *confirm == "" || *org != ""):
		fmt.Fprint(stderr, "error: lucy worktree discard takes a slot and --confirm repeating it\nhelp: run `lucy worktree --help`\n")
		return 2
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	if args[0] == "discard" {
		s, err := c.WorktreeDiscard(ctx, api.WorktreeDiscardParams{Slot: slot, Confirm: *confirm})
		if err != nil {
			return callFailed(err, c, stderr)
		}
		fmt.Fprintf(stdout, "discarded: %s\nrun: %s\nbranch: %s (kept in the repository's clone until git gc)\nhead: %s\n",
			s.Slot, untrusted(s.Run), untrusted(s.Branch), s.Head)
		return 0
	}
	list, err := c.Worktrees(ctx, *org)
	if err != nil {
		return callFailed(err, c, stderr)
	}
	if len(list.Slots) == 0 {
		fmt.Fprint(stdout, "slots[0]: none\n")
		return 0
	}
	fmt.Fprintf(stdout, "slots[%d]{slot,state,prep,run,branch,head,reason}:\n", len(list.Slots))
	for _, s := range list.Slots {
		fmt.Fprint(stdout, slotRow(s))
	}
	return 0
}

// slotRow is one row of `lucy worktree ls`; untrusted already quotes a field that needs it.
func slotRow(s api.WorktreeSlot) string {
	return fmt.Sprintf("  %s,%s,%s,%s,%s,%s,%s\n", s.Slot, s.State, s.Prep, untrusted(s.Run), untrusted(s.Branch), s.Head, untrusted(s.Reason))
}
