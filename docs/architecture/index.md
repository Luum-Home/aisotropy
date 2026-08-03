---
type: Section Index
title: Architecture
description: Aisotropy's technical design — the LLM OS model, request lifecycle, tiered memory, consistency, multi-tenancy, granular control, and observability.
tags: [architecture]
timestamp: 2026-07-08T00:00:00Z
---

# Architecture

## Documents

- [What Aisotropy is (and is not)](what-aisotropy-is.md) — Taxonomy: a multi-tenant agent **runtime**, not an import-me library and not a single bot. **Read this first if you're unsure what category of thing this repo is.**
- [Reality ledger](reality-ledger.md) — **The canonical status node: what is REAL (runs by default) vs CODE-ONLY (dormant behind env vars) vs ASPIRATIONAL. Read this before treating any capability as live.**
- [Adoption surface as built](adoption-surface.md) — The Phase 8 public `aisotropy` package + builder (ADR-0013): how a third party builds a bot without forking.
- [Overview](overview.md) — The full stack, the "LLM OS" model, and the responsibility map.
- [Request lifecycle](request-lifecycle.md) — What happens from HTTP to SSE and the sleep cycle (the *aspirational* lifecycle).
- [Runtime as built](runtime-as-built.md) — Code-grounded: what `aisotropyd` actually wires and runs, with a debt ledger. The honest counterpart to the request lifecycle.
- [Phases 3–7 as built](phases-3-7-as-built.md) — Constitutional-gate → test-coverage matrix + honest debt ledger.
- [Data model](data-model.md) — The unified schema reference: identity keys, HTTP contract structs, and per-tier storage shapes (facts, semantic `Point`/`Kind`, working-memory keys, outbox). The struct is the single source of truth.
- [API reference](api-reference.md) — Every endpoint: method, path, request/response/error, and SSE semantics.
- [Tiered memory](memory-tiers.md) — The three-tier memory system + batch. **The heart of the design.**
- [Consistency](consistency.md) — Transactional Outbox, sleep cycle, idempotency, reconciliation. **The keystone of the design.**
- [Multi-tenancy](multi-tenancy.md) — Isolation via `ctx` at every layer.
- [Threat model](threat-model.md) — The security model for the isolation moat: trust boundary, threats, real-vs-aspirational mitigations, and the real-vs-opt-in in-process authentication posture (ADR-0024/0025).
- [Granular control](granular-control.md) — Security × cost × behavior.
- [Observability](observability.md) — OTel Semantic Conventions for LLMs.
- [Session integration](session-integration.md) — Connection map between the runtime build and its verification layer.
- [Model provider](model-provider.md) — The model-provider abstraction and how a provider is selected/configured.
- [Observability design](observability-design.md) — The arena's 1-span finding reframed, a live P0 privacy fix, and the proposed span tree.
- [AI-product readiness map](ai-product-readiness.md) — Automated-test coverage vs. the canon: what is proven by tests versus asserted.
- [Cognitive-features reality audit (2026-07-17)](cognitive-features-reality-2026-07-17.md) — A dated audit of which cognitive features are real vs dormant vs aspirational.
- [Capabilities & gaps](capabilities-and-gaps.md) — The honest, verified snapshot of what works and what is still a gap.
- [Product evaluation harness](product-evaluation.md) — The product-evaluation harness and what it measures.
- [Consolidation / pruning proposal](consolidation-proposal.md) — Toward a smaller defensible core: what to keep, demote, or cut.
- [Sandbox providers](sandbox-providers.md) — The pluggable sandbox provider seam (Docker/Podman/…) for untrusted task execution.
- [Positioning — key-brokering](positioning-key-brokering.md) — Key-brokering as co-equal pillar vs. flagship optional module, and the grounding for that call.
- [Machine resource guard](machine-resource-guard.md) — The pre-spend guard that checks real machine state before heavy local lanes.
- [Task branch strategy & prompt trust](task-branch-strategy-trust.md) — The branch strategy and the prompt-trust model behind agentic task execution.
- [License audit](license-audit.md) — The Go dependency + compose-image license audit against the permissive-only policy.
- [Rust dependency license audit](rust-dependency-licenses.md) — The license audit for `crates/sleep-cycle` Rust dependencies.

### Standing premises (code-truth guards, projected into the KB)

- [Prune-Evidence Gate](prune-evidence-gate.md) — No deletion claim without executable proof.
- [Dependency-Reuse-First (gollem-first)](dependency-reuse-first.md) — Inventory the dependency's own surface before building above it.
- [Capability-Absence Claims](capability-absence-claims.md) — "They lack X" needs an ecosystem sweep, not core-only reading.
- [Causal Claims About Runs](causal-claims-about-runs.md) — Falsify before you assert a cause for what a run did.

## Arena / competitive evaluation

The live arena (`arenas/live/`) benchmarks aisotropy apples-to-apples against competing
agent frameworks. These docs define its methodology, fairness protocol, per-dimension scoring,
and the forensic write-ups behind specific board results.

- [Arena benchmark methodology](arena-benchmark-methodology.md) — How cold-start, latency, TTFT, RSS and tokens are actually measured.
- [Arena fairness protocol](arena-fairness-protocol.md) — The anti-bias audit for a self-authored benchmark.
- [Arena infrastructure parity](arena-infrastructure-parity.md) — Is the comparison fair on real infra?
- [Arena deterministic judging](arena-deterministic-judging.md) — The primary score is a pure function, not an LLM.
- [Arena QA/memory answer-key scoring](arena-qa-answer-key-scoring.md) — Scoring against answer keys, not verbosity.
- [Arena tenancy levels](arena-tenancy-levels.md) — The L0–L3 tenancy ladder and the corrected "no concept of a tenant" claim.
- [Arena memory analysis](arena-memory-analysis.md) — Is aisotropy's memory better or worse than competitors, with reason.
- [Arena memory extensions](arena-memory-extensions.md) — The ecosystem test for cross-session, semantic, and consolidated memory.
- [Arena memory forensics](arena-memory-forensics.md) — How a 6/8 board score was misdiagnosed, then correctly diagnosed.
- [Arena adapter artifacts](arena-adapter-artifacts.md) — Four times the harness lied about the product, and how the artifacts caught it.
- [Arena safeguard dimension](arena-safeguard-dimension.md) — Adversarial-prompt behavior as a scored dimension.
- [Arena wasm-tool dimension](arena-wasm-tool-dimension.md) — What the ecosystem sweep found for in-sandbox WASM tools.
- [Arena idiomaticity & DX](arena-idiomaticity-and-dx.md) — Idiomaticity audit plus a deterministic developer-experience metric.
- [Arena authorship lens sweep](arena-authorship-lens-sweep.md) — The authorship lens applied across the adapters.
- [Arena integration lane ceiling](arena-integration-lane-ceiling.md) — Why 6/8 is a pass, not a failure.
- [Arena rate-limiting](arena-rate-limiting.md) — Surviving the agnes free-tier 429 under concurrency.
- [Arena agnes quota runbook](arena-agnes-quota-runbook.md) — Running the live arena against agnes without exhausting the quota.

## One-sentence mental map

> Huma validates the contract → hydrates tiered memory in parallel → Gollem assembles surgical
> context and runs the LLM with a reflection loop → SSE to the user → an asynchronous sleep cycle
> consolidates memory. Everything isolated via `context.Context`, everything observable per token.
