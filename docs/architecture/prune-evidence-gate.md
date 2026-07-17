---
type: Process
title: "Prune-Evidence Gate — no deletion claim without executable proof"
description: The gate that prevents a consolidation/pruning proposal from recommending deletion of live code. A "dead code / unused / never-adopted" claim must attach executable evidence from scripts/prune-guard.sh (import graph + tests + reachability), or it is rejected. Generalizes to "code-claim => executable evidence" (code-truth).
tags: [architecture, process, consolidation, dead-code, verification, code-truth, tooling]
timestamp: 2026-07-17T00:00:00Z
---

# Prune-Evidence Gate — no deletion claim without executable proof

> **Origin (the mistake this prevents):** a consolidation ("poda") proposal recommended
> **deleting `internal/routing` as "dead code — nothing imports it,"** copied verbatim from a
> stale line in [`runtime-as-built.md`](runtime-as-built.md) §6. The truth, found by one
> `git grep`: 6 importers, **100% test coverage**, wired in the composition root, publicly
> re-exported as `TenantRoutingPolicy`, and reachable from `main` per the `deadcode` analyzer.
> Deleting it would have broken the build and removed a **constitutional isolation seam**. The
> class of error — asserting a fact about code from a *document read* instead of the *code* — also
> produced the arena strawmen, the Letta "nonexistent API" claim, and "Genkit has no memory
> primitive." (See [`consolidation-proposal.md`](consolidation-proposal.md) for the proposal whose
> `internal/routing` recommendation this gate rejects.)

## The rule (always active for prune/consolidation work)

**A "delete this code / it's dead / unused / never adopted" claim MUST attach executable evidence,
or it is rejected.** Docs and memory are hypotheses; **code + tests + `deadcode` are the truth.**

Before ANY delete/remove/prune recommendation ships (in a proposal, an ADR, a commit, or an agent
report), run:

```bash
scripts/prune-guard.sh <pkg-dir>      # e.g. scripts/prune-guard.sh internal/routing
```

It auto-**BLOCKS** (exit 3) if the package shows any live signal:

1. **Importers** outside the package (`git grep` — tests count: a test that exercises it is live).
2. **Own test coverage** > 0 (`go test -cover`).
3. **Wired** in the composition root (`aisotropy/app.go` / `cmd/aisotropyd/`).
4. **Publicly re-exported** on the `aisotropy/` façade.
5. **Reachable from `main`** per `golang.org/x/tools/cmd/deadcode` (the authoritative Go
   reachability analyzer — a "dead code" claim must cite its output, not a doc). Install with
   `go install golang.org/x/tools/cmd/deadcode@latest`.

Only when **all five are negative** may a prune be *considered* — and even then, `go build ./... &&
go test ./...` must pass with the code removed before committing.

**Proven:** `scripts/prune-guard.sh internal/routing` and `internal/primitives` both return
🔴 BLOCKED with 5 live signals — exactly the recommendations the raw proposal would have shipped.

## The discipline this generalizes (code-truth)

- **code-claim ⇒ executable evidence.** Any assertion of "unused / inert / tested / broken /
  never-adopted" is verified against the code (`git grep`, `go test -cover`, `deadcode`, `go vet`)
  or it is not made. This is the doc-truth discipline extended to **code-truth**.
- **Resolve names to import paths.** The routing miss conflated `internal/routing` (tenant-tier
  routing, live) with the model-selection gateway. Verify against the **exact import path**, never a
  fuzzy name match.
- **Mandatory adversarial verify phase.** A consolidation proposal's delete/demote candidates are
  each independently re-verified (a `propose → verify` gate) BEFORE the proposal is presented — not
  as a post-hoc rescue. A single candidate that a source doc calls "dead" but `prune-guard` blocks
  invalidates trusting that doc for the rest; re-verify all.
- **Fix the upstream doc too.** When a doc (e.g. `runtime-as-built.md` §6) is the *source* of a
  false code-claim, correct the doc — the stale premise otherwise re-poisons the next proposal.

## "Demote" (docs-only) is fine; "delete" needs the gate

`prune-guard` gates **deletion**. A **docs-only demote** — relabelling a live-but-open-loop feature
as experimental/dormant in the docs (e.g. `internal/primitives`, whose write half runs but whose
rewritten output is never read back into a prompt) — touches no code and is not gated. But the
demote's *rationale* still obeys code-truth: do not call reachable, tested code "dead."
