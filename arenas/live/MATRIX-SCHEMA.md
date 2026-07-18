---
type: Reference
title: "matrix.json — the full-matrix coverage manifest, schema + convention"
description: Defines the per-adapter matrix.json shape the live-arena runner reads to enforce and render full lane × form × dimension coverage.
tags: [arenas, live, matrix, schema, coverage]
---

# matrix.json — schema + convention

This document is the machine-checkable contract behind the standing premise in
`arenas/live/FULL-MATRIX.md`: every `(adapter × dimension × model-lane × exec-form)`
cell is either a real result or an explicit `infeasible:<reason>` — never a silent
blank. `matrix.json` is the artifact that carries that claim, and
`arenas/live/runner` (`matrix.go`) is the code that checks it.

## File location

Each adapter ships exactly one manifest at:

```
arenas/live/<fw>/matrix.json
```

`<fw>` is the adapter directory name (e.g. `aisotropy`, `eino`, `langgraph`), and
MUST match the `"framework"` field inside the file.

## Shape

```json
{
  "framework": "<fw>",
  "dimensions": {
    "<dim>": {
      "stub": {
        "native": "pass|fail|infeasible:<reason>|pending",
        "docker": "pass|fail|infeasible:<reason>|pending",
        "wasm":   "pass|fail|infeasible:<reason>|pending"
      },
      "real": {
        "native": "pass|fail|infeasible:<reason>|pending",
        "docker": "pass|fail|infeasible:<reason>|pending",
        "wasm":   "pass|fail|infeasible:<reason>|pending"
      }
    }
  }
}
```

- `<dim>` is one dimension the adapter implements — a mode name (`qa`, `rag`,
  `tools`, `memory`, `memory_scoped`, `isolation_leak`, `structured`, `stream`,
  `observability`, `crash_recovery`, `extensibility`, `middleware`, `wasm_tool`,
  …). The dimension set is per-adapter — an adapter declares only the
  dimensions it actually implements, not the union across all seven adapters.
- Every dimension MUST carry both lanes (`stub` and `real`) and all three forms
  (`native`, `docker`, `wasm`) — six cells per dimension, no exceptions. A form
  that cannot run for that adapter/dimension is not omitted; it is marked
  `infeasible:<reason>` (see below).

## Status vocabulary

| Status | Meaning |
|---|---|
| `pass` | The cell ran and the result was a pass (dimension-specific pass condition — see the adapter's own mode/dimension definition). |
| `fail` | The cell ran and the result was a fail. |
| `infeasible:<reason>` | The cell cannot run for a structural reason (e.g. `infeasible:WASI has no sockets — the framework's HTTP model client cannot run in-sandbox`). The reason is the cell's content — one line, no trailing punctuation requirement, human-readable. |
| `pending` | Not yet filled in. This is a placeholder status ONLY — it must never survive into a manifest an agent claims is complete. The runner's completeness gate (`arenas/live/runner/matrix.go`) treats any `pending` cell as a hard failure. |

No other strings are valid. `infeasible` without a `:<reason>` suffix, empty
strings, `"n/a"`, `"skip"`, `"todo"`, `"unknown"`, etc. are all rejected by the
gate as invalid status values — they are exactly the kind of silent-blank the
full-matrix premise exists to prevent. If a status is genuinely unknown, it is
still `pending` (declared-incomplete), never invented as `pass`.

## Feasibility ground truth (from `FULL-MATRIX.md`)

- **wasm** is feasible only where the runtime executes under WASI (no host
  sockets, no threads-by-default). Only aisotropy's wazero tool-sandbox
  qualifies today; every networked framework (langgraph, llamaindex,
  pydantic-ai, mastra, ai-sdk, genkit-go, eino) is
  `infeasible:"WASI has no sockets — the framework's HTTP model client cannot
  run in-sandbox"` for every dimension.
- **docker** is feasible for every adapter that ships a Dockerfile — the
  bounded pass is footprint + a smoke run per dimension.
- **stub** is feasible for every adapter (each has a mock/fake model + in-memory
  stores) — it is a wiring/smoke lane, not a quality lane.

Feasibility is a code-truth claim, not an assumption: verify against the
adapter's actual run.sh / run-docker.sh / Dockerfile / model-client code before
writing `infeasible`, per `arenas/live/FULL-MATRIX.md` §"Feasibility is
code-truth, not assumption".

## What fills a matrix.json

1. **Scaffold** (this task's deliverable): `arenas/live/scaffold-matrix.sh
   <adapter-dir>` discovers the dimensions an adapter implements and writes a
   skeleton with every cell `pending`. This is the STARTING point — it makes
   zero feasibility or correctness claims.
2. **Fill** (a follow-up, per-adapter task): an agent with real access to run
   that adapter replaces each `pending` cell with `pass`, `fail`, or
   `infeasible:<reason>`, backed by an actual run or a code-grounded
   feasibility check.
3. **Gate**: `arenas/live/runner` (`go run . …`) loads every
   `arenas/live/<fw>/matrix.json` it finds and fails the run if any cell is
   still `pending` or carries an invalid status string. Use
   `-skip-matrix-gate` to opt out of the gate for a partial/manual run (the
   matrix section of `RESULTS.md` still renders, gaps and all).

## Rendering

`RESULTS.md` renders a per-adapter grid: dimension rows × `(lane, form)`
columns, with `infeasible:<reason>` cells showing the reason inline (truncated
for table width) so a gap is visually obvious, not just machine-checked. See
`writeMatrixSection` in `arenas/live/runner/results.go`.

## Non-goals of this document

This schema does not define HOW a dimension's pass/fail condition is computed
— that is dimension-specific and lives with the adapter's own mode contract
(`arenas/live/SPEC.md`, `arenas/live/runner/dimensions.go`, each adapter's
README). This document defines only the manifest shape and the completeness
contract the runner enforces.
