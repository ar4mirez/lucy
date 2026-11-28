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
  lucy answer [--socket <path>] <flight> --accept-proposal <proposal>

Answers a flight's open question. The Tower relays the answer to its Pilot, through the runner's
heartbeat, and the log keeps only the answer's digest. The words after the flight are the answer.
--accept-proposal answers with the supervisor's proposed answer, which lucy questions shows beside
the question with the proposal to name, for example p12.0123456789abcdef: it is then your answer,
recorded as yours. It is refused when the question or its proposal changed since lucy questions
showed it. A supervisor's proposal alone never reaches the Pilot.

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
	proposals := false
	for _, q := range list.Questions {
		fmt.Fprintf(w, "  %s,%s,%s,%s,%s\n", untrusted(q.Flight), untrusted(q.Org), untrusted(q.Project), q.Since.Format(time.RFC3339), untrusted(q.Question))
		if q.ProposalID != "" {
			proposals = true
			fmt.Fprintf(w, "    proposal %s by %s: %s\n", untrusted(proposalToken(q)), untrusted(q.ProposedBy), untrusted(q.Proposed))
		}
	}
	fmt.Fprint(w, "help: run `lucy answer <flight> <answer>` to answer one\n")
	if proposals {
		fmt.Fprint(w, "help: run `lucy answer <flight> --accept-proposal <proposal>` to answer with a proposal, as your answer\n")
	}
}

// proposalToken names a proposal and the question it answers in one token: the proposal's id, a
// dot and the first 16 hex characters of the question's digest.
func proposalToken(q api.Question) string {
	d := q.QuestionSHA256
	if len(d) > proposalDigest {
		d = d[:proposalDigest]
	}
	return q.ProposalID + "." + d
}

const proposalDigest = 16

// parseProposal splits a token proposalToken made into the proposal's id and the question's digest.
func parseProposal(tok string) (id, digest string, ok bool) {
	id, digest, ok = strings.Cut(tok, ".")
	return id, digest, ok && id != "" && len(digest) >= proposalDigest
}

func runAnswer(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sock := fs.String("socket", "", "")
	accept, proposal := false, ""
	var rest []string
	for i := 0; i < len(args); i++ { // --accept-proposal may follow the flight
		a := args[i]
		if v, ok := strings.CutPrefix(a, "--accept-proposal="); ok {
			accept, proposal = true, v
			continue
		}
		if a == "--accept-proposal" {
			accept = true
			if i+1 < len(args) {
				i++
				proposal = args[i]
			}
			continue
		}
		rest = append(rest, a)
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, answerUsage)
			return 0
		}
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy answer --help`\n", err)
		return 2
	}
	if accept {
		id, digest, ok := parseProposal(proposal)
		if !ok {
			fmt.Fprint(stderr, "error: --accept-proposal needs the proposal `lucy questions` shows, for example p12.0123456789abcdef\nhelp: run `lucy answer --help`\n")
			return 2
		}
		if fs.NArg() != 1 {
			fmt.Fprint(stderr, "error: lucy answer --accept-proposal needs a flight and no answer\nhelp: run `lucy answer --help`\n")
			return 2
		}
		return answerWith(*sock, api.AnswerParams{Flight: fs.Arg(0), AcceptProposal: id, QuestionSHA256: digest}, stdout, stderr)
	}
	if fs.NArg() < 2 {
		fmt.Fprint(stderr, "error: lucy answer needs a flight and an answer\nhelp: run `lucy answer --help`; `lucy questions` lists the open questions\n")
		return 2
	}
	return answerWith(*sock, api.AnswerParams{Flight: fs.Arg(0), Answer: strings.Join(fs.Args()[1:], " ")}, stdout, stderr)
}

func answerWith(sock string, p api.AnswerParams, stdout, stderr io.Writer) int {
	c, code := dial(sock, stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
	defer cancel()
	res, err := c.Answer(ctx, p)
	if err != nil {
		return callFailed(err, c, stderr)
	}
	fmt.Fprintf(stdout, "answered: %s\nstate: %s\n", res.Flight, res.State)
	return 0
}
