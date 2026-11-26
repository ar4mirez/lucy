---
id: milestones
title: Milestones
status: active
version: 1
owner: ar4mirez
requirements: []
---

# Milestones

lucy is planned by implementation order and token cost, never by calendar dates
([D2](../decisions/D2-first-milestone.md)). A milestone is done when its exit tests pass, not on a
day. Token ranges are processed tokens (input plus output, about 98% cache reads), from the design
report's measured unit of 25-55M per PR.

| Order | Brief | Goal | PRs | Tokens, processed |
|---|---|---|---|---|
| m0 | [m0.md](m0.md) | Foundation: API, event store, policy engine, spec model, simulation, corpus harness, signed releases through mise, security baseline | 37-57 | 0.9-3.1B |
| m1 | [m1.md](m1.md) | ar4mirez end to end: shadow mode, then takeover with rollback | 145-215 | 3.6-11.8B |
| m2 | [m2.md](m2.md) | Full replacement, part 1: every org on lucy | 48-78 | 1.2-4.3B |
| m3 | [m3.md](m3.md) | Full replacement, part 2: captain surface; firstmate retired | 29-46 | 0.7-2.5B |
| m4 | [m4.md](m4.md) | Breadth, priced per item | 5-15 per item | 0.1-0.8B per item |

## Brief shape

Every brief has front matter whose `requirements` list is exactly the IDs that land in it, then these
sections:

1. **Goal**: one paragraph.
2. **Scope**: the components, in build order, each with its requirement IDs.
3. **Out of scope**: what waits for a later milestone.
4. **Exit tests**: observable events, each naming the requirement IDs it proves.
5. **Security**: the STRIDE rows and checklist items this milestone closes.
6. **Open questions**: anything that needs a decision before or during the milestone.

The m0 brief also tracks its slices, one PR series each. Work a milestone in the order its brief
lists; each step leaves the tree releasable.
