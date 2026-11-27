package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
)

const questionsUsage = `Usage:
  lucy questions [--socket <path>]

Lists the Pilots' open questions, as the Tower records them: each flight that is asking, with
its airspace and runway, when it started asking and its question, oldest first. A Pilot has no
other way to reach you. Answer one with ` + "`lucy answer`" + `.

The questions come from the flights the daemon's hosted Tower flies; it dispatches Ready Flights
only when the daemon was started with --dispatch.
`

const answerUsage = `Usage:
  lucy answer [--socket <path>] <flight> <answer>...

Answers a flight's open question. The Tower relays the answer to its Pilot, through the runner's
heartbeat, and the log keeps only the answer's digest. The words after the flight are the answer.

Only a flight that is asking can be answered.
`

func runQuestions(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("questions", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if code, ok := parseFlags(fs, args, questionsUsage, "lucy questions --help", stdout, stderr); !ok {
		return code
	}
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	list, err := c.Questions(ctx)
	if err != nil {
		return callFailed(err, c, stderr)
	}
	writeQuestions(stdout, list)
	return 0
}

func writeQuestions(w io.Writer, list api.QuestionsList) {
	if len(list.Questions) == 0 {
		fmt.Fprint(w, "questions[0]: no Pilot is asking\n")
		return
	}
	fmt.Fprintf(w, "questions[%d]{flight,org,project,since,question}:\n", len(list.Questions))
	for _, q := range list.Questions {
		fmt.Fprintf(w, "  %s,%s,%s,%s,%s\n", untrusted(q.Flight), untrusted(q.Org), untrusted(q.Project), q.Since.Format(time.RFC3339), untrusted(q.Question))
	}
	fmt.Fprint(w, "help: run `lucy answer <flight> <answer>` to answer one\n")
}

func runAnswer(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, answerUsage)
			return 0
		}
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy answer --help`\n", err)
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprint(stderr, "error: lucy answer needs a flight and an answer\nhelp: run `lucy answer --help`; `lucy questions` lists the open questions\n")
		return 2
	}
	flight, answer := fs.Arg(0), strings.Join(fs.Args()[1:], " ")
	c, code := dial(*sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	res, err := c.Answer(ctx, api.AnswerParams{Flight: flight, Answer: answer})
	if err != nil {
		return callFailed(err, c, stderr)
	}
	fmt.Fprintf(stdout, "answered: %s\nstate: %s\n", res.Flight, res.State)
	return 0
}
