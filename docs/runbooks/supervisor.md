---
id: runbook-supervisor
title: The supervisor of Pilots' questions
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-RUN-010]
---

# The supervisor of Pilots' questions

**When:** turning on suggestions for an org's Pilot questions, writing its rules, arming its models,
or reading why a question went to you.

A supervisor only suggests. Its suggestion is shown beside the question in `lucy questions` and
`lucy decisions`, never reaches the Pilot and never closes the question; you answer, in your own
words or by accepting the suggestion ([daemon runbook](daemon.md#questions)). With no
`spec.supervisor` in an org's Airspace, nothing is suggested.

## How a question is handled

1. **Built-in safety rules.** A question about a secret or a credential (a password, token, key,
   login, certificate), a payment, or an irreversible or production change (deploy, production,
   delete, drop, force-push, `rm -rf`, rewriting history) goes to you, never to a model and never
   to a rule's answer. These are fixed in lucy; an org's rules can add to them, never remove one.
2. **The org's rules**, in order. The first whose `match` finds the question decides: `propose`
   suggests its fixed answer, named `supervisor/rules`; `owner: true` sends the question to you with
   no model call.
3. **The models**, only when the daemon runs with `lucy daemon --supervise` (off by default) and
   the org names them: the small model first, the large one when the small is not confident.
4. **You.** Whatever is left waits for you, as without a supervisor.

Rules call nothing and spend nothing, so they apply with the Airspace alone, without `--supervise`.

## Configure

```yaml
spec:
  supervisor:
    rules:                                        # at most 64, asked in order
      - { match: '\bregion\b', propose: eu-west }  # RE2, case-insensitive, at most 512 characters
      - { match: 'time ?zone', owner: true }
    route: models                                 # these four together, or none of them
    smallModel: <model>
    largeModel: <model>
    dailyTokens: 200000
```

`match` is an [RE2](https://github.com/google/re2/wiki/Syntax) expression, found anywhere in the
question; an expression RE2 refuses (a lookahead, say) refuses the whole Airspace at load. A
`propose` answer is at most 1024 bytes (UTF-8), the most lucy keeps of a suggestion; a longer one
refuses the Airspace at load too.

`route` names a route of the org's gateway binding whose upstream is the model API, allowing
`POST /v1/messages`. The key stays in the org's vault and the gateway injects it, so the daemon
holds none; run the org's gateway with `lucy gateway --org <org>`. Each question is sent with the
flight's org, project and goal, and nothing else. A model's reply must be the plain JSON object
`{"answer": ..., "confident": ...}`; anything else counts as not confident.

## Check

- `lucy status` lists each org that opts in: its tokens today (UTC) against `dailyTokens`, its
  route, and `armed`, true when its models are asked.
- `lucy events` shows `supervisor.called` for each model call with its tokens, and
  `supervisor.declined` with the reason a question went to you: a safety rule, the org's rule, a
  spent budget, a refused or failed call, or neither model confident.
- A model's suggestion longer than 1024 bytes is kept cut short and marked `(cut short: type your
  answer)` in `lucy questions` and `lucy decisions`; it cannot be accepted, so type your answer.
