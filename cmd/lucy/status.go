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
	"github.com/ar4mirez/lucy/internal/config"
)

const statusUsage = `Usage:
  lucy status [--socket <path>]

Shows the running daemon's version, API version, uptime and event log head, through the
daemon's API, and how lucy refers to you (the [you] section of config.toml). Exits 1 when no
daemon is running, 2 when config.toml is invalid.
`

const eventsUsage = `Usage:
  lucy events [--after <seq>] [--limit <n>] [--socket <path>]

Lists events from the daemon's log with a sequence number greater than --after (default 0),
oldest first, at most --limit (default 100, at most 500).
`

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if code, ok := parseFlags(fs, args, statusUsage, "lucy status --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	w, err := c.Hello(ctx)
	if err != nil {
		return callFailed(err, c, stderr)
	}
	st, err := c.Status(ctx)
	if err != nil {
		return callFailed(err, c, stderr)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\nhelp: fix config.toml, or remove it to use the defaults\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "daemon: running\nversion: %s\nclient_version: %s\napi_version: %d\npid: %d\n",
		st.ServerVersion, version, w.APIVersion, st.PID)
	fmt.Fprintf(stdout, "started_at: %s\nuptime: %s\nevent_head: %d\nrefused_connections: %d\nsocket: %s\ncaller: %s\n",
		st.StartedAt.Format(time.RFC3339), time.Duration(st.UptimeSeconds)*time.Second, st.EventHead,
		st.RefusedConnections, c.Socket(), w.Caller.Actor)
	fmt.Fprintf(stdout, "you: %s\n", cfg.You.Label())
	return 0
}

func runEvents(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	after := fs.Int64("after", 0, "")
	limit := fs.Int("limit", 0, "")
	if code, ok := parseFlags(fs, args, eventsUsage, "lucy events --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	page, err := c.Events(ctx, api.EventsParams{AfterSeq: *after, Limit: *limit})
	if err != nil {
		return callFailed(err, c, stderr)
	}
	if len(page.Events) == 0 {
		fmt.Fprintf(stdout, "events[0]: none after seq %d (head %d)\n", *after, page.Head)
		return 0
	}
	fmt.Fprintf(stdout, "events[%d]{seq,stream,stream_version,type,type_version,time,actor}:\n", len(page.Events))
	for _, e := range page.Events {
		fmt.Fprintf(stdout, "  %d,%s,%d,%s,%d,%s,%s\n", e.Seq, csv(e.Stream), e.StreamVersion, e.Type,
			e.TypeVersion, e.Time.Format(time.RFC3339Nano), csv(e.Actor))
	}
	if page.More {
		fmt.Fprintf(stdout, "more: run `lucy events --after %d`\n", page.Next)
	}
	return 0
}

// csv quotes a value that would break the row: a comma, quote or newline.
func csv(s string) string {
	if !strings.ContainsAny(s, ",\"\n\r") {
		return s
	}
	return `"` + strings.NewReplacer(`"`, `""`, "\n", `\n`, "\r", `\r`).Replace(s) + `"`
}
