package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ar4mirez/lucy/internal/audit"
	"github.com/ar4mirez/lucy/internal/sim"
)

const simUsage = `Usage:
  lucy sim [--seed <n>] [--log]   fly the simulation demo and print how each flight ended

A development command. It flies a scripted airspace in the simulation harness: a fake Pilot
runtime, a fake forge and a virtual clock, on a throwaway event store in a new temporary
directory, removed afterwards. No agent, network, git or real time is involved, and nothing
outside that directory is touched. The same seed always prints the same output.

Flags:
  --seed <n>   the seed every random draw comes from (default 1)
  --log        also print every event: seq, virtual time, stream and type
`

func runSim(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sim", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	seed := fs.Uint64("seed", 1, "")
	showLog := fs.Bool("log", false, "")
	if code, ok := parseFlags(fs, args, simUsage, "lucy sim --help", stdout, stderr); !ok {
		return code
	}
	dir, err := os.MkdirTemp("", "lucy-sim-")
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a temporary directory this command created
	if err := flySim(context.Background(), dir, *seed, *showLog, stdout); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func flySim(ctx context.Context, dir string, seed uint64, showLog bool, stdout io.Writer) error {
	w, err := sim.Open(ctx, dir, seed, sim.Options{})
	if err != nil {
		return err
	}
	defer w.Close() //nolint:errcheck // read-only from here on
	if err := sim.Demo(ctx, w, sim.RunOptions{}); err != nil {
		return err
	}
	log, err := w.Log(ctx)
	if err != nil {
		return err
	}
	rep, err := audit.VerifyStore(ctx, w.Store)
	if err != nil {
		return err
	}
	flights := w.Tower.Flights()
	fmt.Fprintf(stdout, "seed: %d\nflights[%d]{id,state,reason,pr,pilots}:\n", seed, len(flights))
	for _, f := range flights {
		fmt.Fprintf(stdout, "  %s,%s,%s,%d,%d\n", f.ID, f.State, f.Reason, f.PR, f.Generation)
	}
	fmt.Fprintf(stdout, "merges: %d\nevents: %d\naudit_head: %s\nvirtual_time: %s\n", len(w.Forge.Merges()), len(log), rep.Head,
		w.Clock.Now().Sub(sim.Epoch))
	if showLog {
		fmt.Fprintf(stdout, "log[%d]{seq,time,stream,type}:\n", len(log))
		for _, l := range log {
			fmt.Fprintf(stdout, "  %d,%s,%s,%s\n", l.Seq, l.Time.Format("2006-01-02T15:04:05Z"), l.Stream, l.Type)
		}
	}
	return nil
}
