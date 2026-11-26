---
id: glossary
title: "lucy: glossary"
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-POL-005, LUCY-POL-006, LUCY-OPS-001, LUCY-OPS-002]
---

# lucy: glossary

lucy names its parts after a control tower. The terms below are what the docs, the CLI and every
surface say; the right-hand column is the neutral id the same thing has in code, the API, policy
and the event log. Ids never change with display text, so recorded decisions stay comparable.

## Terms

| Term | Means | In code, API and policy |
|---|---|---|
| **Owner** | The human in charge: decides, grants postures, answers holding decisions, steers. lucy serves one owner. The display title is configurable (see below) | role `owner`, input source `owner`, posture `owner`, approval `owner` |
| **Tower** | The main daemon, `lucy daemon`: the control plane that supervises everything and runs the policy engine | role `daemon` |
| **Approach** | The supervisor of one org (one airspace): turns the owner's intent into flights, supervises its pilots, escalates | role `supervisor` |
| **Airspace** | An org: one hard-isolated sandbox with its own credentials, postures and projects | `org` |
| **Runway** | A project (a repository) inside an airspace | `project` |
| **Pilot** | A worker agent flying one flight: a ship worker, a researcher or a reviewer | roles `worker`, `researcher`, `reviewer` |
| **Flight** | A task: a goal, a delivery path, an autonomy level and an acceptance test. A *ship* flight lands a change; a *research* flight returns knowledge and ships nothing | `task` |
| **Clearance** | A policy engine decision: cleared, denied or holding, with its rule, reason and rule set version | `policy.decision` event; outcome `cleared`, `denied`, `holding` |
| **Holding** | Waiting on the owner: the action is cleared only once the owner approves that exact action under the same facts | outcome `holding` |
| **Grounded** | A flight that is blocked or cancelled | task state, from m1 |
| **Pre-flight check** | The validation pipeline a change passes before it may land | validation stages, from m1 |
| **Flight log** | The signed proof of what a flight did and how it was validated | signed proof, from m1 |
| **Takeoff** | A merge, or a release | action kind `merge`; a release tag |
| **`lucy preflight`** | The setup command: plans and installs everything lucy needs, checks it (`--check`), repairs what it can (`--repair`), and asks how lucy should refer to you | `lucy preflight`, from m1 (LUCY-OPS-001, LUCY-OPS-002) |

## The owner's display title

Inside lucy the human role is always `owner`. What lucy shows that person is a display title,
"Owner" unless configured otherwise, and an optional name. Both live in the `[you]` section of
`config.toml` ([configuration reference](EXTENSIONS.md#3-lucys-settings-configtoml)):

```toml
[you]
title = "Commander"   # how lucy refers to you; default "Owner"
name = "Ada"          # optional; how lucy addresses you
```

`lucy preflight` will ask for both when it lands (m1); until then, edit the file. The title is
resolved only when text is rendered for the owner: CLI and TUI output, the dashboard, chat replies
through MCP, notifications and holding messages ("holding: waiting on the Commander"). Events,
policy decisions, reasons and API fields keep the id `owner`, so changing the title never changes
a record. `lucy status` shows the current setting as `you:`.

## Choices recorded here

- **Holding, not "require-owner".** A decision that waits on the owner has the outcome `holding`,
  matching the term above. Together with `cleared` and `denied` it is rule set version 3 and API
  schema 1.1.0 (LUCY-POL-006).
- **Postures** are `owner` (the default: merges only on the owner's word), `green` and `client`
  (the client confirms, the owner relays it) (LUCY-POL-005).
- **Requirement area codes stay.** IDs are never renamed, so the owner surface keeps the area code
  `CAP` (LUCY-CAP-001 to LUCY-CAP-004).
- **Plain technical words stay** where they are precise: `main` for a default branch or Go's
  `package main`, `org`, `project`, `task` and `worker` in code.
