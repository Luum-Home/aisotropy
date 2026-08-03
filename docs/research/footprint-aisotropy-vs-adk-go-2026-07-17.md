---
type: Research
title: "Footprint analysis: aisotropy vs ADK-Go (the Go control) — is bigger than the bare loop good or bad?"
description: >
  Footprint analysis comparing aisotropy against ADK-Go as the Go control, asking whether being bigger than the bare loop is good or bad.
tags: [research, footprint, adk-go, benchmark, comparison]
timestamp: 2026-07-17T00:00:00Z
---

# Footprint analysis: aisotropy vs ADK-Go (the Go control) — is "bigger than the bare loop" good or bad?

**Date:** 2026-07-17
**Data source:** `arenas/BENCHMARKS.md` §"Class 3 — Framework footprint (REAL, run-verified)" +
`bench/footprint/` (`bash bench/footprint/run.sh`, raw in `bench/footprint/results.txt`), committed
run-verified in `6798a62`.
**Question (operator):** the Go control **ADK-Go** ships a *smaller* image/RSS than aisotropy because
aisotropy boots its full runtime (memory/gates/tiers). **Is that good or bad?**
**Short answer:** it is **expected and mostly good** — the control makes the comparison *more* honest,
not less — with one caveat (pay-for-what-you-don't-use) that the code already mitigates where it matters.

## The measured data

Every row **boots and serves HTTP 200 on `/healthz`** (an image that doesn't run doesn't count).

| Framework | Variant | Language / packaging | Image (MiB) | Idle RSS (MiB) | Cold-start ms (med) |
|---|---|---|---:|---:|---:|
| **aisotropy** | production `Dockerfile` | Go / distroless static | **12.4** | **8.8** | **262** |
| **ADK-Go** *(control)* | static → distroless | Go / distroless static | **5.9** | **3.5** | **310** |
| LangGraph | idiomatic | Python / `python:3.12-slim` | 61.3 | 47.0 | 583 |
| LangGraph | optimized | Python / alpine + prune | 31.3 | 54.1 | 1930 |
| Mastra | idiomatic | TypeScript / `node:22-slim` | 126.5 | 121.2 | 514 |
| Mastra | optimized | TypeScript / alpine + esbuild | 56.9 | 105.5 | 562 |

Delta aisotropy vs ADK-Go: **+6.5 MiB image, +5.3 MiB idle RSS** (aisotropy is ~2.1× the control on
both). Delta aisotropy vs the interpreted frameworks: **~5–10× smaller image, ~5–14× smaller RSS**.

## Why "bigger than ADK-Go" is expected — not apples-to-apples

ADK-Go is a **bare agent loop**: at idle it boots essentially nothing beyond the framework. aisotropy
is an **LLM-OS**: at idle it stands up the working-memory tier (BadgerDB), the constitutional gates
(quota token-bucket + circuit breaker), and the Huma syscall/contract layer. So the +6.5 MiB / +5.3 MiB
is **the cost of the OS layer itself** — not bloat around the same functionality. Comparing aisotropy to
ADK-Go is not "same product, heavier"; it is "**bare loop vs full runtime**," a different product
category.

The absolute numbers keep this honest: **12.4 MiB / 8.8 MiB RSS is featherweight**. The gap that an
operator actually pays for is against the interpreted frameworks (47–121 MiB RSS), and there aisotropy
wins by an order of magnitude — including the structural finding that **their idle RSS barely moves even
when the image is optimized** (LangGraph 47→54 MiB, Mastra 121→106 MiB: the CPython/Node heap is fixed
regardless of image size). You can shrink an *image*; you cannot shrink a loaded interpreter + its
dependency graph.

## Why the control is a GOOD thing (honesty, not a liability)

Having a same-language (Go/distroless) control is what makes the footprint story credible instead of a
cherry-pick:

1. It **isolates the OS tax**: bare Go floor = 5.9 MiB; full aisotropy runtime = 12.4 MiB ⇒ "the OS
   costs +6.5 MiB," stated plainly.
2. It shows **both Go entrants crush the interpreted frameworks**, so the win is attributable to the
   *language/packaging* discipline, not to omitting features.
3. It **pre-empts the "you built them a bad image" rebuttal**: the control proves the harness is fair to
   Go, and `arenas/BENCHMARKS.md` additionally measures each interpreted framework's *official*
   recommended packaging.

Hiding the control would make aisotropy look artificially good; showing it makes the whole table
trustworthy.

## The caveat — where it *would* be bad — and why it mostly isn't (code-truth)

It would be **bad** if aisotropy paid for capabilities a given deployment does not use — i.e. if every
tier and background worker booted eagerly regardless of config (the "everything-on-by-default"
anti-pattern). The code already applies *pay-for-what-you-use* where it counts:

- **Semantic tier is LAZY.** `ensureSemantic` is `semanticOnce.Do`-guarded and only initializes on the
  first RAG/knowledge call (`aisotropy/app.go:490` → `aisotropy/knowledge.go`); a deployment that never
  does vector retrieval never pays for it.
- **The DataFusion sleep-cycle worker is OPT-IN.** It starts only when `AISOTROPY_SLEEPCYCLE_BIN` is set
  (`aisotropy/app.go:606`); dormant by default.
- **The bitrate sampler/coordinator is OPT-IN** (`AISOTROPY_BITRATE_BIN`); no route, no worker, no cost
  when unset.

So the **always-on** cost is just: the in-memory working-memory tier (lightweight BadgerDB), the gates
(cheap in-memory structs), and the HTTP stack. That is the 8.8 MiB idle RSS — proportional to what it
delivers, not dead weight.

## Where you could still tighten it (marginal ROI)

A **lean-boot mode** that also lazily initializes (or skips) the working-memory tier for *stateless*
deployments — a bot with no cross-turn memory does not need BadgerDB at boot — would pull idle RSS
toward the ADK-Go floor (~3.5 MiB). Worth it only if a target market is memory-less edge functions; for
the multi-tenant server aisotropy is built for, **8.8 MiB is already small enough that the ROI is
marginal**. The number that matters commercially is the 5–14× gap vs the interpreted frameworks, not the
~5 MiB gap vs a bare Go loop.

## Bottom line + framing guidance

**It is fine — arguably good.** The +6.5 MiB over a bare-Go control is the expected, justified cost of
being an OS rather than a loop, already optimized with lazy/opt-in init where it matters, and the control
makes the benchmark *more* honest.

**Framing recommendation for any external pitch:** present **ADK-Go as the "bare-Go floor," not as a
rival.** Then the reading is unambiguous — "this is what shipping raw Go costs (5.9 MiB); this is the OS
tax (+6.5 MiB); and both are featherweight next to the 61–127 MiB interpreted stacks." Positioned that
way, the control's smaller size is a point *in aisotropy's favor* (it proves the OS tax is tiny), never a
weakness.

## References

- Data: `arenas/BENCHMARKS.md` §Class 3; harness `bench/footprint/`; commit `6798a62`.
- Lazy/opt-in evidence: `aisotropy/app.go:490` (ensureSemantic), `aisotropy/knowledge.go` (`semanticOnce.Do`),
  `aisotropy/app.go:606` (sleep-cycle opt-in).
- Positioning context: `docs/architecture/overview.md` (the "specialized tools by access temperature"
  thesis), `arenas/live/SPEC.md` (the live-arena honesty discipline).
