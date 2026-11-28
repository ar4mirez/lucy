---
id: tech-harness
title: Harness adapters
status: active
version: 3
owner: ar4mirez
requirements: [LUCY-RUN-002, LUCY-RUN-003, LUCY-RUN-008, LUCY-RUN-013]
---

# Harness adapters

How a runner drives an agent harness (TECH.md section 6). The seam is `internal/harness`; the
Claude Code adapter is `internal/harness/claudecode`; `lucy runner --harness claude-code` runs it.

- **Adapter.** An `Adapter` gives a session's arguments and environment, refuses any program but
  its pinned version (an upgrade is held until the contract passes for it), and decodes the
  program's structured output into events tagged with their source (LUCY-RUN-002, LUCY-RUN-003).
- **Session limits.** `claudecode` runs `claude --print` over stream-json both ways, with an empty
  MCP set (`--strict-mcp-config`), no settings file but lucy's own (no user, project or local
  source; nothing pre-allowed; no hooks of its own; skills and slash commands off), no saved
  session, and every permission prompt sent to the adapter. The run directory's CLAUDE.md, or
  AGENTS.md, is appended to the system prompt as framed repository text that grants no
  permission; the directory is empty until slice 6 puts the checkout there.
- **Tool prompts.** File tools on paths inside the run's directory, Bash (`exec`) and
  WebFetch or WebSearch (`fetch`) go to `runner.permit`, decided by the policy engine with the
  Pilot as a worker of the lease's org; no rule allows `exec` or `fetch` yet. Exec and fetch text
  is recorded only redacted, with a digest. Every other prompt is refused.
- **Questions** (LUCY-RUN-013). AskUserQuestion streams as `asking` with the question's id on
  every repeat. The broker records `run.answered` (id and digest) and resends the text in each
  heartbeat reply until the runner acknowledges the id; an asking event for an answered id is
  activity, and an answer a restart lost was never acknowledged, so the runner asks again.
- **Hooks.** The adapter registers lucy, the session's host, for a callback on every lifecycle hook
  over stream-json; each callback is recorded as a `hook` run event naming it and is never acted
  on. No settings file loads, so these callbacks are the session's only hooks. The hook events
  are tested against protocol transcripts only, and are unverified against a real Claude Code
  2.1.332 session until real captures land (D3c).
- **Contract.** The recorded transcripts under `testdata/<pinned version>` are replayed in CI by
  `harnesstest`, which plays the harness's side and follows its documented settings rules;
  `harness-check.yml` checks the newest release nightly and reports it ready or held. Transcripts
  captured from real sessions wait on a real model login (D3c).

## Account state, before every spawn

An adapter that is a `harness.Converger` re-asserts its harness's account state before each spawn
(LUCY-RUN-008), in the org's synthetic home only, through an `os.Root` on that home, so nothing
it writes lands outside it, and the owner's real home is never read. For Claude Code that is
`.claude.json` (onboarding complete, auto-updates off, no user-scope MCP servers; the run's
directory trusted and onboarded with no tools pre-allowed and no MCP servers) and
`.claude/settings.json` (no hooks, hooks disabled); every other key, the login above all, is
kept. A state file, settings file or settings directory that is a symlink, or a state or settings
file that is not a JSON object, is refused rather than overwritten, and so is the spawn. On state that already holds nothing changes; what changed is logged and recorded as
the run's first event.

A `harness.Prerequisite` adapter tells, before a spawn, that its harness cannot run on its own:
Claude Code's needs a login in the org's home, and checks only that `.claude/.credentials.json` is
there or `.claude.json` names an `oauthAccount`, never what they hold. The runner then does not
spawn: it finishes the run `needs_owner` with the need (`harness_login`), and the Tower holds the
flight with a question to the owner (LUCY-RUN-008).
