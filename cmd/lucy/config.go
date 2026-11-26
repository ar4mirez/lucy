package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/config"
	"github.com/ar4mirez/lucy/internal/spec"
)

const configUsage = `Usage:
  lucy config validate [<dir>]        validate the spec resources in a directory
  lucy config status [--socket <path>] show the resources the running daemon loaded, with status,
                                       and the documents it refused that name no resource

validate reads every .yaml, .yml and .json file under <dir> ($XDG_CONFIG_HOME/lucy/resources
by default) the way the daemon does: each document must be an ai-sdlc.io/v1alpha1 or
lucy/v1alpha1 resource with metadata.namespace (its airspace), valid against its schema, and
without status, which lucy keeps in its database. It prints each resource's Valid condition and
every schema violation. It needs no daemon and changes no file. Exits 0 when every resource is
valid, 1 when any is refused, 2 when the directory cannot be read.
`

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "error: missing subcommand\nhelp: run `lucy config --help`\n")
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, configUsage)
		return 0
	case "validate":
		return runConfigValidate(args[1:], stdout, stderr)
	case "status":
		return runConfigStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown config subcommand %q\nhelp: run `lucy config --help`\n", args[0])
		return 2
	}
}

func runConfigValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, configUsage)
			return 0
		}
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy config --help`\n", err)
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(stderr, "error: unexpected argument %q\nhelp: run `lucy config --help`\n", fs.Arg(1))
		return 2
	}
	dir := fs.Arg(0)
	if dir == "" {
		var err error
		if dir, err = config.ResourcesDir(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 2
		}
	}
	set, err := spec.LoadDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\nhelp: pass the directory that holds your resources: `lucy config validate <dir>`\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "config: %s\n", dir)
	if len(set.Results) == 0 {
		fmt.Fprintf(stdout, "resources[0]: no .yaml, .yml or .json files in %s\n", dir)
		return 0
	}
	fmt.Fprintf(stdout, "resources[%d]{source,kind,namespace,name,valid,reason}:\n", len(set.Results))
	for _, r := range set.Results {
		c, _ := r.Condition(spec.ConditionValid)
		fmt.Fprintf(stdout, "  %s,%s,%s,%s,%s,%s\n", csv(r.Source.String()), csv(r.Kind), csv(r.Namespace), csv(r.Name), c.Status, c.Reason)
	}
	refused := set.Invalid()
	if refused == 0 {
		fmt.Fprintf(stdout, "result: all %d valid\n", len(set.Results))
		return 0
	}
	fmt.Fprintf(stdout, "conditions[%d]{source,type,status,reason,message}:\n", refused)
	var violations [][2]string
	for _, r := range set.Results {
		if r.Valid() {
			continue
		}
		c, _ := r.Condition(spec.ConditionValid)
		fmt.Fprintf(stdout, "  %s,%s,%s,%s,%s\n", csv(r.Source.String()), c.Type, c.Status, c.Reason, csv(c.Message))
		for _, e := range r.Errors {
			violations = append(violations, [2]string{r.Source.String(), e.String()})
		}
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "violations[%d]{source,violation}:\n", len(violations))
		for _, v := range violations {
			fmt.Fprintf(stdout, "  %s,%s\n", csv(v[0]), csv(v[1]))
		}
	}
	fmt.Fprintf(stdout, "result: %d of %d refused\nhelp: fix the files above, then run `lucy config validate %s` again\n", refused, len(set.Results), dir)
	return 1
}

func runConfigStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if code, ok := parseFlags(fs, args, configUsage, "lucy config --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	var all []api.ResourceInfo
	var refused []api.RefusedInfo
	var truncated int
	for offset := 0; ; {
		page, err := c.Resources(ctx, api.ResourcesParams{Offset: offset, Limit: 500})
		if err != nil {
			return callFailed(err, c, stderr)
		}
		if offset == 0 {
			refused, truncated = page.Refused, page.RefusedTruncated
		}
		all = append(all, page.Resources...)
		if !page.More {
			break
		}
		offset = page.Next
	}
	if len(all) == 0 && len(refused) == 0 {
		fmt.Fprint(stdout, "resources[0]: the daemon has loaded none\nhelp: add resources to the config directory and restart the daemon\n")
		return 0
	}
	fmt.Fprintf(stdout, "resources[%d]{namespace,kind,name,source,type,status,reason,since}:\n", len(all))
	for _, r := range all {
		for _, cond := range r.Conditions {
			fmt.Fprintf(stdout, "  %s,%s,%s,%s,%s,%s,%s,%s\n", csv(r.Namespace), csv(r.Kind), csv(r.Name), csv(r.Source),
				cond.Type, cond.Status, cond.Reason, cond.LastTransitionTime)
		}
	}
	if len(refused) == 0 {
		return 0
	}
	fmt.Fprintf(stdout, "refused[%d]{source,type,status,reason,message,since}:\n", len(refused))
	for _, r := range refused {
		for _, cond := range r.Conditions {
			fmt.Fprintf(stdout, "  %s,%s,%s,%s,%s,%s\n", csv(r.Source), cond.Type, cond.Status, cond.Reason, csv(cond.Message), cond.LastTransitionTime)
		}
	}
	if truncated > 0 {
		fmt.Fprintf(stdout, "refused_truncated: %d\n", truncated)
	}
	fmt.Fprint(stdout, "help: these name no resource, so they carry no status of their own; run `lucy config validate` for details, fix them and restart the daemon\n")
	return 0
}
