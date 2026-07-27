# ADR-0074: `capabilities-and-gaps.md` stops being a dispatch source — a claims registry re-derived from the code

**Status:** Accepted (2026-07-27 — implementation landed with the ADR:
`scripts/gap-claims-guard.sh`, `manifests/capability-claims.yaml`, header note in
`docs/architecture/capabilities-and-gaps.md`). Every number and command quoted below is real
output from the command named alongside it.
**Related:** [`.claude/rules/prune-evidence-gate.md`](../../.claude/rules/prune-evidence-gate.md)
(the code-truth discipline this generalizes), `scripts/prune-guard.sh` (the deletion-claim twin),
`scripts/k8s-chart-guard.sh` / `scripts/arena-determinism-guard.sh` (the fail-closed guard family),
[ADR-0040](0040-runtime-config-file-env-precedence.md) + [ADR-0072](0072-runtime-config-refuses-secrets.md)
(the feature one burned dispatch tried to rebuild), [ADR-0071](0071-k8s-deploy-validation-lanes.md)
(the other), and the verification report
[`docs/06-Daily/reports/gap-claims-verification-2026-07-27.md`](../06-Daily/reports/gap-claims-verification-2026-07-27.md).

## Context

`docs/architecture/capabilities-and-gaps.md` was written on 2026-07-17 as an honest, verified
snapshot — "every row verified against the code, not aspirational". On 2026-07-27 it was used for
exactly what it looks built for: **dispatching build work**. That is where it failed.

The first item dispatched off it — "close the config gap: no single runtime config file, env-only" —
had **already shipped ten days earlier**, on the doc's own timestamp date (`bfeca15`, ADR-0040). The
agent would have rebuilt an existing feature and allocated a duplicate ADR. That prompted a
read-only re-verification of all 12 dispatched claims against the code rather than against the
document's own assertion. Result:

| | |
|---|---|
| claims re-verified | 12 |
| **wrong or materially overstated** | **5 (42%)** |
| fully stale | 1 (k8s/Helm — "zero manifests, zero charts") |
| partially stale | 3 (benchmark executed; bitrate crate; sleep-cycle crate) |
| half stale | 1 (routing seam "not adopted" — a stale copy of the line the `internal/routing` prune incident already killed) |
| still true | 7 |
| **dispatches burned rebuilding existing work** | **2** (runtime config file `bfeca15`; Helm chart `3d39c44`) |

Two of the five described work that landed **before the document's own timestamp**. One SDD change
(`arena-benchmark-execution`) rested on a premise that was simply false — the cross-framework
wall-clock benchmark ran on 2026-07-23 across 9 frameworks × {native, docker}. Two more had to be
re-scoped to a fraction of their dispatched size.

The failure class is named and already has a rule in this repo: **asserting a fact about the code
from a document read instead of from the code**
([`prune-evidence-gate.md`](../../.claude/rules/prune-evidence-gate.md) — the `internal/routing`
"dead code, nothing imports it" incident, where one `git grep` found 6 importers and 100% coverage).
The existing rule covers **deletion** claims and is enforced by `scripts/prune-guard.sh`. Nothing
covered **capability** claims, and capability claims rot faster: a deletion claim is wrong the day it
is written or never, while a capability claim is *correct when written and becomes false silently*,
as the shipping code moves underneath it. A document that reads as authoritative and rots invisibly
is worse than no document — it converts absent information into confident misinformation, and it did
so with enough authority to fund work twice.

The 2026-07-27 correction block added to the doc fixes the config row. It does not fix the
mechanism. The next row will rot the same way.

## Decision

**The volatile subset of `capabilities-and-gaps.md` is re-derived from the code by a guard, and the
document no longer holds those facts — it references claim ids.**

1. **A machine-readable claims registry: `manifests/capability-claims.yaml`.** Each entry is
   `{id, claim, requires, check, expect}` (+ `incident` / `evidence` provenance). The `check` is one
   read-only, offline, sub-second shell command that prints exactly one value; `expect` is an
   operator (`eq|ne|ge|le|contains|not_contains|matches`) and a value. 15 claims seeded — the five
   that were wrong on 2026-07-27, plus the still-true ones from the same report, plus the two real
   remaining gaps it isolated.

2. **The guard: `scripts/gap-claims-guard.sh`.** It runs every check and compares. It **does not
   parse the document's prose.** Regex-over-markdown was considered and rejected: it produces false
   failures, and a guard people learn to ignore is worse than none — the same reason the arena's
   primary score is deterministic by construction rather than LLM-judged
   (`.claude/rules/arena-deterministic-judging.md`).

3. **Registry ↔ doc linkage is the only coupling.** The guard asserts, by exact string match, that
   every registered claim id appears in the document. A claim cannot be registered without the doc
   pointing at it, and a doc row cannot be deleted while its claim is still registered. Editing a
   volatile row without updating its registry entry makes the guard go red — that is the point.

4. **Fail-closed, per the family discipline: a guard that cannot evaluate must never absolve.** A
   missing `requires:` path, a check command not found (exit 127), a check that prints nothing, an
   uncomparable value, a malformed registry, or no YAML reader at all → **BLOCKED, exit 2**, never a
   pass. Blocked **dominates** drifted: a partially-evaluated run must not report a complete verdict.
   This is the direct lesson of `prune-guard.sh` returning green for live code because it could not
   see inside a nested module.

5. **Exit codes follow the repo guard convention:** `0` clean / `1` a claim drifted / `2` cannot
   evaluate.

6. **Scope is bounded and the exclusions are written down.** Only claims that are **volatile** (they
   can change without anyone touching the doc) **and mechanically re-derivable** are registered. The
   registry's `out_of_scope` block names five excluded classes with reasons: positioning rows
   (Karpathy lineage, agents.md, openspec.dev/ADR-0039, SDD/ADR-0041 — these change by ADR, not by
   code drift); semantic code properties a grep cannot distinguish from their opposite (in-scan
   tenant filtering vs. a post-filter; redaction-before-memory) which belong to tests and the
   constitutional gates; measured numbers whose re-derivation costs money or minutes (WER, recall@k,
   p50/p95 values — the guard checks that **the run exists and is recorded**, never re-derives the
   values); upstream `net/http` behaviour behind the shutdown window; and the "Open decisions"
   section, which is not code-derived by definition. **The guard's silence on an excluded row is not
   a claim about that row**, and the verdict text says so.

## Consequences

**Verified at landing** (`bash scripts/gap-claims-guard.sh`, 0.46s wall clock):

```
checks: 16 ok / 0 drifted / 0 blocked
VERDICT: 🟢 every volatile claim re-derived from the code and matches.
```

All 15 claims re-derive correctly at HEAD, plus the linkage check. Notable values the document can
no longer silently contradict: 34 tracked files under `deploy/{helm,k8s}`; 3 tracked runtime-config
artifacts; 18 real benchmark data rows; 14 `//go:build manual` test files and **0** scripts or
workflows that invoke them; **0** cargo stages in `ci.yml`; 0 concurrency primitives inside
`hydrateSystemPrompt`; 0 `Reflect` references in `chat_stream.go`; 0 real-provider clients in the
shim's test suite.

**Mutation-tested, not merely asserted** — four injected defects, each turning it red, each restored
(mutations applied to throwaway copies via `--manifest`, so the tracked registry was never touched):

| Injected defect | Result | Exit |
|---|---|---|
| A claim drifts (registry says 0 k8s files, code has 34) | `DRIFTED k8s-manifests-shipped got 34, registry says eq 0` | **1** |
| A `requires:` path is renamed away | `BLOCKED hydration-sequential required path(s) missing` | **2** |
| A check's command does not exist | `BLOCKED ocr-empty-content-sentinel check command not found (exit 127)` | **2** |
| No YAML reader on `PATH` | `CANNOT EVALUATE — ZERO claims were evaluated. This is NOT a pass.` | **2** |

Both readers were exercised: python3+PyYAML (primary) and the `yq`+`jq` fallback (16 ok / exit 0
with PyYAML unavailable), so the fallback is not untested dead code.

**What this changes in practice.** `capabilities-and-gaps.md` may still be read as an overview, but
**work is not dispatched off a row until the guard is green** — the doc's new header note says so and
gives the command. When the guard reports DRIFTED, the fix is to correct the doc row *and* the
registry entry together, and to re-scope any work already in flight that was scoped from the stale
row. The check command in the registry **is** the re-derivation, so a dispatcher never has to trust
prose again.

**Honest boundaries.**
- **Coverage is a subset, by design.** 15 claims do not cover the whole document; the excluded
  classes are excluded permanently, not as debt. A green guard means "the machine-checkable rows are
  true", never "the document is true".
- **A registry entry can itself be wrong.** The guard proves *code matches registry*, not *registry
  matches intent*. The seeded expectations were each derived from the 2026-07-27 verification
  report's code-level evidence rather than from the document, which is the strongest available
  starting point — but a future author who edits both the doc row and the registry entry to the same
  wrong value gets a green. Nothing mechanical can close that; the linkage check only forces the two
  to move together.
- **Thresholds are deliberately loose where the exact number is noise** (`ge 30` k8s files, `ge 18`
  benchmark rows, `ge 10` manual test files). This trades sensitivity for the property that matters
  more: the guard fires on *direction* changes (the surface disappeared, the benchmark stopped
  running) and not on routine churn. A guard that cries wolf on every added file gets disabled.
- **It is not wired into a pre-commit or CI gate.** It is a dispatch-time and doc-edit-time tool,
  invoked by a human or an agent about to trust a row. Wiring it into `scripts/pre-push-gate.sh` is a
  deliberate follow-on, not an oversight: the doc legitimately lags code by hours, and blocking a
  push on that lag would train people to bypass the guard.
- **Two real gaps are now *registered as claims*, not as prose**: `no-lane-invokes-manual-tests`
  (14 manual-tagged test files, 0 lanes invoking them) and `rust-workers-not-built-in-ci` (0 cargo
  stages). When either is closed, the guard goes red and forces the doc to be updated — which is
  precisely the behaviour that was missing.

**As shipped:** `scripts/gap-claims-guard.sh`, `manifests/capability-claims.yaml`,
`docs/architecture/capabilities-and-gaps.md` (header note + claim-id table only; the rows are
otherwise unchanged).
