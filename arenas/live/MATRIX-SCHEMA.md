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
  "candidate": false,
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
| `infeasible:<reason>` | The cell cannot run for a structural reason (e.g. `infeasible:Go's wasip1 target compiles net.Dial but has no sock_connect, so the model client fails at runtime`). The reason is the cell's content — one line, human-readable, and it must name a CHECKABLE cause. **Never cite "WASI has no sockets": that has been false since WASI 0.2 (January 2024).** WASI 0.2 (January 2024) already ships `wasi:sockets` and `wasi:http/outgoing-handler`, and WASI 0.3.0 landed 2026-06-11 — so the blocker is never "WASI cannot do networking". It is per-framework: whether that adapter's FULL dependency tree can be built into, and run as, a WASI guest. |
| `pending` | Not yet filled in. This is a placeholder status ONLY — it must never survive into a manifest an agent claims is complete. The runner's completeness gate (`arenas/live/runner/matrix.go`) treats any `pending` cell as a hard failure — UNLESS the manifest is a `candidate` (see below). |

## `candidate` — a WORK-IN-PROGRESS adapter

An optional top-level `"candidate": true` marks an adapter whose matrix is
legitimately incomplete because verifying some cells is blocked on something
**external** to the code: a `real`-lane cell that needs the provider while the
free-tier quota is exhausted, or a `docker`/native-image build not yet exercised.

A candidate is **excluded from the completeness gate** (its `pending` cells do
NOT fail the run), but this is NOT a silent omission — `gateMatrices`' caller
logs every candidate loudly (`WARNING: N WIP candidate adapter(s) excluded …`)
and RESULTS.md renders its cells labeled `CANDIDATE`, never as passes. This is
the honest middle path between **faking a `pass`** on an unverified cell (a lie)
and **dropping the adapter entirely** (hides real work): it preserves the
no-blank-cells law's INTENT — no SILENT gaps — while letting a genuine WIP land.

Clear the flag (delete it, or set `false`) once every cell is a real result; the
adapter then re-enters the normal completeness gate. Default is `false` (absent =
false): a normal, complete adapter never sets it.

No other strings are valid. `infeasible` without a `:<reason>` suffix, empty
strings, `"n/a"`, `"skip"`, `"todo"`, `"unknown"`, etc. are all rejected by the
gate as invalid status values — they are exactly the kind of silent-blank the
full-matrix premise exists to prevent. If a status is genuinely unknown, it is
still `pending` (declared-incomplete), never invented as `pass`.

## Feasibility ground truth (from `FULL-MATRIX.md`)

- **wasm** feasibility is a question about the ADAPTER'S DEPENDENCY TREE, not
  about WASI's capabilities. WASI 0.2 (January 2024) ships `wasi:sockets` and
  `wasi:http/outgoing-handler`; WASI 0.3.0 landed 2026-06-11 with native async.
  A cell that blames "WASI has no sockets" is asserting something false about a
  public standard — and using it to say a competitor's product cannot do
  something it can.

  What IS checkable, per framework: whether that adapter's full dependency tree
  compiles to a WASI target and RUNS there. Those are different questions, and
  the second is usually the real one. Measured 2026-07-28: a Go program calling
  `net.Dial` compiles cleanly under `GOOS=wasip1` and then fails at runtime under
  wazero (`dial tcp: lookup … Connection reset by peer`), because Preview 1
  defines no `sock_connect`. So "it does not build" is the wrong evidence for Go;
  "it builds and cannot dial" is the right one.

  Each framework's reason must name its own blocker with something a reader can
  re-run or cite. Two worked examples: eino fails to build at all
  (`modernc.org/sqlite` → `modernc.org/libc` excludes every file under wasip1),
  while genkit-go builds cleanly and is blocked only at runtime.

  aisotropy's own two `wasm` passes sit on a DIFFERENT AXIS and the distinction
  must stay visible: aisotropy is the wasm HOST (a native binary embedding
  wazero to sandbox tool execution, itself using Preview 1), while the
  competitors are graded as wasm GUESTS. Host-embeds-sandbox and
  framework-runs-as-guest are not the same question, `wasm_tool` is declared by
  only one of nine adapters, and the honest reading of "only aisotropy passes
  wasm" is partly that only aisotropy declared the dimension it wins.
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
