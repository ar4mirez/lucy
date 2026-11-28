package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/shadow"
)

const shadowUsage = `Usage:
  lucy shadow import [--socket <path>] <file>
  lucy shadow ls [--socket <path>] [--org <org>]

Shadow mode compares lucy's decisions with the existing supervisor's on the same tasks, per task
class, before lucy takes a class over (LUCY-RUN-012). lucy learns what the existing supervisor did
only from a file you give it: it fetches nothing and reads no live state.

  import   reads <file>, a versioned JSON Lines record of the existing supervisor's tasks and their
           actions (docs/tech/shadow.md), and records each task. The whole file is checked first and
           refused, naming the line, if any line is refused (an unknown field, a value out of
           bounds, anything that looks like a credential); then nothing is imported. The daemon
           then records the tasks one by one, and refuses a task of an org with no admitted
           Airspace or a task id imported before with other contents: the import stops there, and
           the tasks recorded before it stay recorded. A task imported before is unchanged, so
           importing the fixed file again records only what is new.
  ls       lists the imported tasks by org, class and task.
`

func runShadow(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "error: lucy shadow needs import or ls\nhelp: run `lucy shadow --help`\n")
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, shadowUsage)
		return 0
	case "import", "ls":
	default:
		fmt.Fprintf(stderr, "error: unknown shadow command %q\nhelp: run `lucy shadow --help`\n", args[0])
		return 2
	}
	fs := flag.NewFlagSet("shadow "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	org := fs.String("org", "", "")
	if code, ok := parseFlagsAllowingArgs(fs, args[1:], shadowUsage, "lucy shadow --help", stdout, stderr); !ok {
		return code
	}
	if args[0] == "import" && (fs.NArg() != 1 || *org != "") || args[0] == "ls" && fs.NArg() != 0 {
		fmt.Fprintf(stderr, "error: lucy shadow %s takes %s\nhelp: run `lucy shadow --help`\n", args[0],
			map[string]string{"import": "one file", "ls": "no arguments"}[args[0]])
		return 2
	}
	var tasks []shadow.Task
	if args[0] == "import" {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		tasks, err = shadow.Parse(f)
		_ = f.Close() // read-only
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\nhelp: nothing was imported; fix the file (docs/tech/shadow.md) and import it again\n", err)
			return 1
		}
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	if args[0] == "ls" {
		ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
		defer cancel()
		list, err := c.ShadowList(ctx, *org)
		if err != nil {
			return callFailed(err, c, stderr)
		}
		if len(list.Tasks) == 0 {
			fmt.Fprint(stdout, "tasks[0]: none\n")
			return 0
		}
		fmt.Fprintf(stdout, "tasks[%d]{org,class,task,posture,actions}:\n", len(list.Tasks))
		for _, t := range list.Tasks {
			fmt.Fprintf(stdout, "  %s,%s,%s,%s,%d\n", csv(t.Org), csv(t.Class), csv(t.Task), csv(t.Posture), t.Actions)
		}
		return 0
	}
	counts := map[string]int{}
	for i, t := range tasks {
		p := api.ShadowTask{Org: t.Org, Task: t.ID, Class: t.Class, Goal: t.Goal, Repository: t.Repository, Posture: t.Posture}
		for _, a := range t.Actions {
			p.Actions = append(p.Actions, api.ShadowAction{At: a.At, Action: a.Action, Text: a.Text, By: a.By, Checks: a.Checks})
		}
		ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
		res, err := c.ShadowImport(ctx, p)
		cancel()
		if err != nil {
			fmt.Fprintf(stdout, "imported: %d\nunchanged: %d\nstopped_at: task %s (%d of %d)\n", counts["recorded"], counts["unchanged"], csv(t.ID), i+1, len(tasks))
			code := callFailed(err, c, stderr)
			fmt.Fprintf(stderr, "help: the %d tasks before it stay recorded; fix the file and import it again, which leaves them unchanged\n", i)
			return code
		}
		counts[res.Outcome]++
	}
	fmt.Fprintf(stdout, "imported: %d\nunchanged: %d\n", counts["recorded"], counts["unchanged"])
	return 0
}
