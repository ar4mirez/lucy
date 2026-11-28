package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/backlog"
)

const flightsUsage = `Usage:
  lucy flights [--socket <path>] [--json]

Lists every Flight the daemon loaded, by org and name: its shape, whether it is ready, whether the
Tower flies it and the Tower's state of it. --json prints each as the spec's BacklogTask document
(the front matter of a backlog task), one JSON object per line, checked against the vendored
schema: id is org/name, title its goal, status the Tower's state or ready or not ready,
dispatchable whether it is ready, blocked why a ready one is not dispatched.
`

func runFlights(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("flights", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	asJSON := fs.Bool("json", false, "")
	if code, ok := parseFlags(fs, args, flightsUsage, "lucy flights --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	var list api.FlightList
	if err := c.Call(ctx, api.MethodFlights, api.NoParams{}, &list); err != nil {
		return callFailed(err, c, stderr)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		for _, f := range list.Flights {
			t := backlog.Task(f)
			if err := backlog.Validate(t); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			if err := enc.Encode(t); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
		}
		return 0
	}
	if len(list.Flights) == 0 {
		fmt.Fprint(stdout, "flights[0]: no Flight is loaded\n")
		return 0
	}
	fmt.Fprintf(stdout, "flights[%d]{flight,shape,ready,dispatched,state,goal}:\n", len(list.Flights))
	for _, f := range list.Flights {
		fmt.Fprintf(stdout, "  %s/%s,%s,%s,%s,%s,%s\n", f.Org, f.Name, f.Shape, condText(f.Ready), condText(f.Dispatched), f.State, untrusted(f.Goal))
	}
	return 0
}

// condText is a condition as status (reason), or - when there is none.
func condText(c api.ConditionInfo) string {
	if c.Type == "" {
		return "-"
	}
	if c.Reason == "" {
		return c.Status
	}
	return c.Status + " (" + c.Reason + ")"
}
