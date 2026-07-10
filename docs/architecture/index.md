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
- [Tiered memory](memory-tiers.md) — The three-tier memory system + batch. **The heart of the design.**
- [Consistency](consistency.md) — Transactional Outbox, sleep cycle, idempotency, reconciliation. **The keystone of the design.**
- [Multi-tenancy](multi-tenancy.md) — Isolation via `ctx` at every layer.
- [Threat model](threat-model.md) — The security model for the isolation moat: trust boundary, threats, real-vs-aspirational mitigations, and the honest "identity is not authenticated yet" gap.
- [Granular control](granular-control.md) — Security × cost × behavior.
- [Observability](observability.md) — OTel Semantic Conventions for LLMs.
- [Session integration](session-integration.md) — Connection map between the runtime build and its verification layer.

## One-sentence mental map

> Huma validates the contract → hydrates tiered memory in parallel → Gollem assembles surgical
> context and runs the LLM with a reflection loop → SSE to the user → an asynchronous sleep cycle
> consolidates memory. Everything isolated via `context.Context`, everything observable per token.
