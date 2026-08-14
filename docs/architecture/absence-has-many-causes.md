---
type: Concept
title: "Absence has many causes — how to probe a service before blaming it"
description: >-
  A probe that collapses "refused", "timed out", "unauthorized" and "answered correctly" into one
  boolean cannot support a conclusion that names one of them. This is the rule behind the arena's
  service gate, the three times it was violated anyway, and the preflight that makes the right
  probe the cheap one. Written after the third violation was committed by an agent that had the
  rule available and never read it.
tags: [architecture, arena, diagnosis, evidence, probes, operations]
timestamp: 2026-08-14T00:00:00Z
---

# Absence has many causes — how to probe a service before blaming it

> **The rule.** Refused, timed-out, unauthorized and answered-correctly are **four different
> facts**, and only one of them means "not there". A probe that cannot tell them apart cannot
> support a conclusion that names one of them. **Absence has many causes; presence has one.**

This is not new. It is written, correctly and at length, in
[`arenas/live/runner/main.go`](../../arenas/live/runner/main.go)'s `probeResult` — the arena's
service gate classifies every probe into those four states and refuses to reduce one to a boolean.
That gate works. It ran on 2026-08-14 and printed `service gate: Qdrant (REST) OK`.

The reason this document exists is that **the rule has been violated three times anyway**, and the
third time by an agent that had it available in that comment and never opened the file.

## The three violations

| when | the conclusion drawn | what was actually true |
|---|---|---|
| 2026-07-28 | "Qdrant is down" — a 40-minute board killed | Three probes had merely **timed out** on a machine at 93% swap. Minutes later on an idle machine the same commands returned docker OK, port open, HTTP **401**. The service had been answering the whole time. |
| 2026-07-28 | (same session) the 401 body was never seen | It said `Must provide an API key`. `curl -o /dev/null` had kept the status code and discarded the sentence that disproved the conclusion. |
| 2026-08-14 | "the infra is not up" → then "re-running is not viable" | Both were wrong, and each was reached the same way. See below. |

### The 2026-08-14 violation, in detail, because it is the instructive one

An agent investigating why a published board showed aisotropy at 90.4% error ran:

```bash
curl -s -o /dev/null -w '%{http_code}' http://localhost:6333/healthz   # -> 200
```

and reported the service healthy. It was not usable: Qdrant's `/healthz` answers **without
auth** while every other endpoint requires the key, so a `200` there is true and says nothing
about whether a run can proceed. The seeder then failed eight times with
`401 Must provide an API key` — the same body, discarded by the same flag, for the same reason,
**in a repository whose own source comment describes that exact failure.**

The same agent had already declared, minutes earlier, that re-running was "not viable", citing
`swap 96%` and `resource-guard.sh` exit 1. Both readings were wrong:

- `exit 1` is **DEGRADED**, not BLOCKED. BLOCKED is exit **3**.
- Swap is a **lagging** indicator — `scripts/resource-guard.sh` prints it labelled
  `(informative)` and its own comment warns of the false BLOCKED it produces. The primary signal,
  `sys-free`, read **38%** against thresholds of ≤8 (blocked) and ≤20 (degraded), and load per
  core was 0.41 on 12 cores.
- The agent had run the guard with its output sent to `/dev/null`, keeping only the exit code —
  discarding the line that explains the verdict, which is the same mistake as `-o /dev/null` one
  level up.

Docker then started both services and they answered on the **first** attempt.

## Why a correct rule in the right words still failed

It was in a Go comment, 1,671 lines into `main.go`, inside the gate that already enforces it.
Nobody reads that file to answer "is the infra up?" — they run `curl`. **A rule only reaches the
person about to break it if it lives where they are looking**, which for an operator or an agent
is: a script they run, a rule file loaded at startup, or a failure message.

Hence `scripts/arena-preflight.sh`, which makes the correct probe the cheap one.

## The preflight

```bash
bash scripts/arena-preflight.sh
```

It probes what a run actually needs and prints the discriminating detail every time:

| check | why not the obvious endpoint |
|---|---|
| Qdrant `/collections` **with the key** | `/healthz` answers unauthenticated — it cannot distinguish usable from refusing |
| rqlite authenticated `SELECT 1` | `/status` is served to the anonymous user so the container healthcheck works; `/db/*` is not |
| embedder — a **real vector** | a 200 on `/health` proves the process booted, not that the model loaded |
| model quota — one minimal call | cheaper than discovering a 429 mid-board |
| `resource-guard.sh` by **exit code**, 0/1/3 | DEGRADED is "run sequentially", not "do not run" |

Its verdicts are `OK`, `AUTH`, `REFUSED`, `INCONCLUSIVE`, `BAD` — never a bare boolean. `AUTH`
prints the body. `INCONCLUSIVE` states in the message that a timeout is **not** proof of absence.

Falsified before being trusted: with the key removed it reports
`AUTH — HTTP 401 Must provide an API key` (not "down"); against an empty port it reports
`REFUSED — nothing is listening ... this IS proof of absence`.

## Consequences for anyone reading a published board

Two things follow, and both were learned the hard way on 2026-08-14:

1. **A board can be published with the infra broken.** The 2026-07-28 board reports aisotropy at
   90.4% error across 8 of 10 modes. aisotropy fails CLOSED without its tiers while several
   competitors degrade to in-memory stores, so a run without infra destroys one framework and
   leaves the others' numbers looking fine. Check the error column before reading the scores.
2. **The raw records are ignored by design, the archiver exists, and it had never been run.**
   `arenas/live/RESULTS.records.jsonl` is gitignored deliberately (size, and the runner *appends*,
   so a small debugging run mixes into a finished board — 780 Agnes records were destroyed that way
   on 2026-07-28). The `.gitignore` comment names the remedy:
   [`scripts/arena-post-board.sh`](../../scripts/arena-post-board.sh) archives the file read-only
   **as its first action**.

   On 2026-08-14, `arenas/live/.records-archive/` did not exist. The safeguard had been written and
   never invoked, so the records examined that day were overwritten by the investigation's own
   re-run and it is **not proven** which run they came from. The `connection refused` lines quoted
   in the table above are therefore evidence that *some* run met a dead rqlite — not proof that the
   published board's run did.

   This is the `lo-escrito-no-se-aplica-solo` shape: the tool existed, the reasoning was written
   down, and neither ran. **Archive before re-running, or the board is unauditable after the fact.**

## Relationship to sibling rules

- [`causal-claims-about-runs`](../../.claude/rules/causal-claims-about-runs.md) — the general
  form: state the disconfirming observation before asserting a cause. This document is its most
  expensive special case.
- `prune-evidence-gate` — "a green from a tool that could not run its checks is absence of
  evidence, not evidence of absence." The converse also holds: a **red** can be a mis-calibrated
  threshold, which is what `swap 96%` was.
- `verify-before-execute` — the state is the truth, the plan is a hypothesis.
