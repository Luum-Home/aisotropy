# Arena Full-Matrix Coverage — a standing PREMISE (never re-ask)

> **Operator directive (2026-07-17, non-negotiable):** anything built for the live arena
> (`arenas/live/`) is covered across the **full cartesian matrix** by construction. This is a
> **premise, not a question** — an agent building an arena artifact does NOT ask the operator whether
> to cover a lane/form/dimension; it covers all feasible cells and **declares the infeasible ones with
> a reason**. The operator explicitly accepted the cost and does not want this decision re-litigated.

## The three axes (every arena artifact spans all of them)

| Axis | Values |
|---|---|
| **Model lane** | `stub` (mock model + in-memory stores — offline, CI/free, validates wiring) **AND** `real` (agnes-2.0-flash + real infra — the live numbers) |
| **Execution form** | `native` **AND** `docker` **AND** `wasm` (where the runtime can run under WASI) |
| **Dimension** | every mode the adapter implements: `qa`/`rag`/`tools`/`memory`/`memory_scoped`/`isolation_leak`/`structured`/`stream`/`observability`/`crash_recovery`/`extensibility`/`middleware`/… |

The valued matrix = **lane × form × dimension**, per adapter. A new dimension (e.g. `middleware`) is
not "done" until it exists in **both lanes** and **all feasible forms**.

## The no-blank-cells law (honest-arena, extended to the matrix)

Every `(adapter × dimension × lane × form)` cell is **either**:
1. a **real result** (a run that PASSes/FAILs/scored), **or**
2. an explicit **`infeasible: <one-line reason>`** in the adapter's matrix manifest.

A **silently missing** cell is an arena defect — it reads as "covered" when it is not. Silent omission
is the exact failure class the prune-evidence and honest-arena disciplines exist to prevent: a claim of
coverage without executable evidence. **Declare infeasibility; never omit.**

### Feasibility is code-truth, not assumption

- **wasm**: feasible only where the runtime executes under **WASI** (no host sockets, no threads-by-default).
  aisotropy's wasm is its **wazero tool-sandbox** (feasible — it is the differentiator). Networked
  frameworks (langgraph/llamaindex/pydantic/mastra/ai-sdk/genkit-go/eino) are **`infeasible: WASI has no
  sockets — the framework's HTTP model client cannot run in-sandbox`** — that reason IS the cell's content.
- **docker**: feasible for all (each adapter ships a Dockerfile); the bounded pass is footprint + a smoke
  run per dimension.
- **stub**: feasible for all (every framework has a mock/fake model + in-memory stores). Stub is a
  **wiring/smoke** lane — quality is NOT scored on canned answers; it proves the cell runs offline.

## The matrix manifest (the enforcement artifact)

Each adapter ships `arenas/live/<fw>/matrix.json` declaring, per dimension, the status of each
`(lane, form)` cell: `pass` | `fail` | `infeasible:<reason>` | `pending`. The runner reads every
manifest and **fails the arena report if any cell is left `pending`** (a blank) — completeness is
machine-checked, not eyeballed. `RESULTS.md` renders the matrix so a gap is visible, with each
infeasible cell showing its reason inline.

**The one honest exception — `"candidate": true`:** a WIP adapter whose incompleteness is blocked
on something *external* to the code (a `real`-lane cell needing the provider while the free-tier
quota is exhausted, or a `docker`/native-image build not yet run) may carry a top-level
`"candidate": true`. A candidate is **excluded from the `pending`-gate**, but this is NOT the
silent gap the law forbids: the runner logs every candidate loudly and `RESULTS.md` labels its
cells `CANDIDATE`, never `pass`. This preserves the law's INTENT (no *silent* coverage claim)
while letting a genuine WIP land instead of forcing either a **faked `pass`** (a lie) or **dropping
the adapter** (hides real work). Clear the flag once every cell is a real result. See
[`MATRIX-SCHEMA.md`](MATRIX-SCHEMA.md) §`candidate`.

> **Reconciled 2026-08-05.** The paragraph above lived only in the agent-facing projection at
> `.claude/rules/arena-full-matrix.md`, which is gitignored and therefore reaches no clone. That is
> the worst half to lose: without it the law reads as absolute, and an adapter blocked on an
> exhausted provider quota has exactly two ways out, both bad — fake a `pass`, or drop the adapter
> and hide real work. The escape hatch IS the honesty mechanism, so it belongs where the law is.

## What an agent MUST do when it touches the arena

When building or modifying ANY arena artifact (a new dimension, a new adapter, a fix):
1. Implement it in **both** model lanes (`stub` + `real`).
2. Make it run in **native** + **docker**; declare **wasm** feasible-and-implemented or
   `infeasible:<reason>`.
3. Update `matrix.json` so **no cell is `pending`** — every cell is a result or an infeasibility.
4. Verify: the stub lane runs **offline** (zero network); the real lane reports **provider** tokens
   (or `-1`, never fabricated); docker builds and smokes.
5. Do **not** ask the operator whether to cover the matrix. If a cell is genuinely infeasible, encode
   the reason and move on. Only escalate if a reason is *uncertain* (e.g. "does framework X's model
   client actually need sockets?") — resolve that by reading the code, not by asking.

## Why this is a project primitive, not a one-off

The arena's whole value is being **apples-to-apples and honest**. A partially-filled matrix silently
biases the comparison (a tool looks "untested" where a rival looks "covered"). Making full-matrix
coverage a **standing premise inherited by every agent** removes the per-task negotiation and guarantees
every arena result is read against a complete, gap-declared grid.

## Contextual Trigger

- Always active for any work under `arenas/live/` (new dimension, new adapter, adapter fix, runner change).
- Referenced by `arenas/live/SPEC.md` (the arena contract) as the coverage law.
