---
type: Roadmap note
title: "Adoption learnings — the first external build on Aisotropy"
description: What the first real third-party build on Aisotropy validated and the generic gaps it revealed. Every roadmap item here is evidence-driven — it maps to something a real dev had to hand-roll — not speculative. The adopter and their product are kept anonymous by request; only the generic patterns are recorded.
tags: [roadmap, adoption, dx, learnings, guardrails, frontend, scaffold]
timestamp: 2026-07-16T00:00:00Z
---

# Adoption learnings — the first external build

A third party forked Aisotropy and built a real, domain-specific application on it: a
compliance-sensitive, knowledge-grounded assistant (a concept demo, not a production system). We
reviewed it read-only to learn — we do **not** copy their code (it is their IP) and we do **not**
name the adopter or their product here. We extract only the *generic patterns and gaps*.

This is the first real signal that the "who is the buyer / does the surface hold" question can be
answered with evidence instead of argument. It should shape the roadmap.

## What it validated (design decisions confirmed by a real consumer)

- **The public façade holds.** They used `aisotropy.New`, `WithPersona`, `WithTypedTool` (several),
  `WithLLM`, `WithRateLimit`, `WithAddr`, `TenantFromContext`, `Document`, `IngestTenantKnowledge`,
  `App.Run` — and imported `internal/…` **zero times**. A complex domain app built entirely on the
  sealed public surface validates the dual-surface design (ADR-0013).
- **Tenant-wide knowledge (ADR-0019)** with a real use case: one approved corpus ingested once at
  tenant scope, queried by every user.
- **The anti-magic boundary landed.** Their own code comments the domain-policy layer as *"the part
  aisotropy deliberately does NOT provide (it's domain policy)."* They knew exactly where the
  framework stops and their domain starts — and did not fight it. That is a design win, not a gap.

## What it revealed (generic gaps — evidence-driven)

They wrote several hundred LOC to stand the app up. The **domain** parts (their corpus content,
persona, tool logic, policy classification) are legitimately theirs. The parts below are **plumbing
every next dev will rewrite** — so the framework should absorb them.

| Gap (what they hand-rolled) | Evidence | Generic fix | Status |
|---|---|---|---|
| Their own frontend: an embedded HTML page + static serve + a reverse-proxy with `FlushInterval = -1` | ~72 LOC + the SSE-buffering gotcha | (a) the AG-UI / Vercel-AI-SDK **stream adapters** (use a React copilot framework); (b) a built-in **`WithDevUI()`** — a minimal chat page + streaming plumbing so you get a browser UI without writing any of it | (a) **in flight**; (b) **proposed** |
| Policy-before-answer wrapped in a **custom LLM client** via `WithLLM` | ~113 LOC | **`WithGuardrailCheck`** (shipped, ADR-0033) — plug an input/output check instead of wrapping the LLM client. Gap is **discoverability**, not capability | **shipped**; needs a discoverability doc/example |
| Several hundred LOC to reach a running app from a blank `main.go` | the whole app | **`aisotropy new <app>`** scaffold — generate a running skeleton (wiring + persona stub + one tool + corpus stub + `WithDevUI`) so a dev starts from a working domain app and customizes | **proposed** |
| Corpus mapped inline to `[]aisotropy.Document` | their corpus layer | Minor: an `IngestDirectory(path)` convenience for markdown/text files | low priority (they did not suffer) |

## Concrete feature the adopter surfaced

Their code notes a wish: an *approved-claims validator on the model OUTPUT*. This is a **generic,
valuable** capability (any RAG app wants "never assert what is not in your knowledge base") — and it
is **implementable today** via the existing output-guardrail seam (`internal/guardrails/output.go`'s
`OutputCheck`, plugged through `WithGuardrailCheck`). Roadmap:

- Document the pattern (a corpus-grounded `OutputCheck`).
- Optionally ship a **generic grounded-output check** helper: given the tenant's ingested corpus,
  flag/refuse response claims not supported by retrieved context. Generic (not domain policy), so it
  belongs in the framework as an opt-in check.

## Pattern to document (our own code, not theirs)

Their app's shape — **persona + corpus/RAG + typed tools + a guardrail seam + a web frontend** — is a
better "realistic app" template than `examples/helpdesk`. Worth a generic, first-party
**"building a domain-grounded assistant"** guide/example, written from scratch as a neutral
knowledge-grounded support assistant, so the next dev has the whole shape in one place.

## The adoption metric this gives us

The honest DX metric, made concrete: **how many of those hundreds of LOC could the dev have not
written?** With `WithDevUI` (the frontend plumbing), the guardrail seam (most of the client wrapper),
and a scaffold (most of the wiring), a dev starts from **~100 LOC of pure domain** instead of several
hundred of domain + plumbing. That is the adoption curve bending, and it is now measurable against a
real build.
