---
type: Reference
title: "Capabilities & gaps — honest, verified snapshot"
description: A grounded state-of-the-art of what aisotropy actually does today vs. what is a gap, across observability, RAG, memory tiers, guardrails, deployment (k8s), configuration formats, standards interop (agents.md/MCP/SDD), the Karpathy LLM-OS lineage, and benchmarks. Every row is verified against the code/repo, not aspirational. Companion to the reality-ledger (per-feature REAL/DORMANT/ASPIRATIONAL) and the consolidation proposal.
tags: [architecture, capabilities, gaps, honesty, deployment, k8s, config, observability, rag, guardrails, benchmarks, standards, karpathy]
timestamp: 2026-07-17T00:00:00Z
---

# Capabilities & gaps — honest, verified snapshot

This document answers, in one place and honestly, "how does aisotropy handle X, and does it
support Y?" for the dimensions operators actually ask about. Each row was **verified against the
code/repo** during the 2026-07-17 audit — not taken from marketing. It separates **REAL** (built +
exercised), **PARTIAL** (present but bounded/gated), and **GAP** (not there).

It is a companion to:
- [`reality-ledger.md`](reality-ledger.md) — per-feature REAL / DORMANT / ASPIRATIONAL.
- [`runtime-as-built.md`](runtime-as-built.md) — the code-grounded "what actually boots".
- [`consolidation-proposal.md`](consolidation-proposal.md) — the surface-pruning recommendation.

## Summary table

| Dimension | Status | Detail (verified) |
|---|---|---|
| **Telemetry / observability** | ✅ REAL (strength) | OTel `gen_ai` semantic-convention spans with `tenant_id`/`user_id` **native on the serving span** (`internal/llm.WithOTel`; export via `OTEL_EXPORTER_OTLP_ENDPOINT`, no-op default). Per-tenant cost meter at `GET /costs`. Liveness `GET /healthz` + readiness `GET /health` (tier probes, no LLM spend). Bits-per-token north-star (`internal/bitrate`, CODE-ONLY, gated on `AISOTROPY_BITRATE_BIN`). |
| **RAG** | ✅ REAL | `internal/semantic`: in-process brute-force cosine default; Qdrant gRPC via `QDRANT_HOST` (CODE-ONLY until provisioned). `tenant_id`/`user_id` filter applied **during** the HNSW/cosine scan (not a post-filter → not a leak). Multi-kind vectors (Document/Fact/Turn, ADR-0017). BYO embedder via `AISOTROPY_EMBED_URL` (`HashEmbedder` for dev). |
| **Memory tiers** | ✅ REAL | Four stores by access temperature: BadgerDB working memory (µs, `internal/memory`) → SQLite+FTS5 / networked rqlite facts (ms, `internal/facts`) → Qdrant / in-process cosine semantic (vector, `internal/semantic`) → DataFusion sleep-cycle (offline batch, `crates/sleep-cycle`, DORMANT unless `AISOTROPY_SLEEPCYCLE_BIN`). Every read/write scoped by `(tenant, user[, session, product])`. |
| **Guardrails** | ✅ REAL | `internal/guardrails` via `api.Guard`: input PII/secret **redaction** (the redacted text is what all downstream tiers + the model see — a raw secret never reaches memory), injection/prompt-leak **detection**, output toxicity/leak checks. Detection always on; **blocking opt-in** (`AISOTROPY_GUARDRAILS_BLOCK_INJECTION`/`_BLOCK_OUTPUT`/`_LLM_JUDGE`). Runs pre-LLM (`checkInput`) and post-LLM (`checkOutput`), incl. the streaming buffer-then-check path. |
| **Karpathy / LLM-OS lineage** | ✅ EXPLICIT | The whole framing is the Karpathy "LLM-OS" analogy, cited in `README.md`, `docs/architecture/overview.md`, `docs/index.md`: LLM = CPU, context window = RAM, memory hierarchy by access temperature, `context.Context` = process descriptor isolating a tenant across the call chain. |
| **agents.md** | ✅ SUPPORTED (dev-time) | Root [`AGENTS.md`](../../AGENTS.md) in the [agents.md](https://agents.md/) open format — context for AI **coding** agents working on the repo. ADR-0022 positions agents.md + Agent Skills (`SKILL.md`, via `cmd/skill-import`) + OKF + MCP as **import/packaging surfaces**, mapped to typed primitives + literal personas (never hidden runtime prompt-injection, per the anti-magic gate ADR-0002). **Note:** `AGENTS.md` is dev-time context, NOT a runtime bot-definition format — a consumer adds primitives via Go + MCP + wiring-YAML. |
| **MCP (runtime tools)** | ✅ REAL | MCP-backed tool execution is a first-class import surface (ADR-0016 lever #2; `internal/plugin` KindMCP manifests). |
| **Configuration** | 🟡 PARTIAL | Runtime config = **environment variables** (`AISOTROPY_*`, see `aisotropy/config.go` + `.claude/rules/services-config.md`). Plugins/primitives = **YAML manifests** (goccy/go-yaml; KindMCP/go-import/wasm). Skills = **Markdown** (`SKILL.md` → generated Go via `cmd/skill-import`). The struct-as-contract emits **JSON Schema**. **GAPS:** no TOML; no single runtime config **file** (yaml/toml) — env-only. |
| **Benchmarks** | 🟡 PARTIAL (honest) | [`arenas/BENCHMARKS.md`](../../arenas/BENCHMARKS.md): Class 1 = aisotropy's own **framework overhead** (model held constant as the keyless stub → every ns is the framework: identity→ctx, quota gate, hydration, tool gating, contract). Class 2 = **cost-governance effectiveness** (a token-counting stub proves the pre-spend quota gate + response cache cut spend). The cross-framework wall-clock table is a **documented reproducible method, NOT executed**. |
| **Deployment — Docker** | ✅ REAL | Distroless `Dockerfile`; reference `docker-compose.yml` (aisotropyd + real networked rqlite:4001 + Qdrant:6333/6334) + `docker-compose.sleepcycle.yml` overlay (ADR-0016 lever #3). Bare binary falls back to in-process defaults. |
| **Deployment — Kubernetes / Helm** | ❌ GAP | **Zero k8s manifests, zero Helm charts** in the repo. The "kubernetes" mentions are code comments (k8s named as a *possible* `SandboxProvider` backend you implement yourself). The single Go binary + Distroless image **is** k8s-deployable, but no `Deployment`/`StatefulSet`/`values.yaml`/HPA ships — orchestration is currently DIY. |
| **PDF ingestion** | ❌ OUT BY DESIGN | `aisotropy/knowledge.go`: document `Content` is plain text; "Parsing (PDF/HTML/etc.) is the caller's responsibility." PDF/HTML extraction is BYO. (The `cmd/skill-import` "pdf-form-filler" is only an example skill *name*, not runtime PDF ingestion.) |
| **openspec.dev** | ❌ NOT SUPPORTED | No references in the repo. |
| **Spec-Driven Development (SDD)** | 🟡 DEV-PROCESS ONLY | SDD is used to **build** aisotropy (the agent tooling under `.claude/`), not a capability aisotropy offers to consumers. aisotropy-the-product does not "support SDD" as a feature. |

## Known bounded limitations (honest)

- **Graceful-shutdown best-effort window.** `App.Run`'s `server.Shutdown(shutdownCtx)` uses a bounded
  grace period; the background-worker join (`waitWorkers`) and `WorkingMemory.Close` are correctly
  ordered after it. But `http.Server` does not cancel in-flight request contexts, so a *pathological*
  SSE handler still writing to working memory past the grace period could race `BadgerDB.Close()`
  ("DB Closed"). This is the **same deliberate best-effort posture** documented for workers
  (`aisotropy/app.go` `waitWorkers` comment), not a latent bug; closing it fully would mean wiring
  request-context cancellation into shutdown. Verified clean otherwise by the 2026-07-17
  concurrency/`-race` audit (no other triggerable races beyond the already-fixed worker-join one).

## Open decisions (operator's call)

1. **Key-brokering positioning.** Is `RunTask` key-brokering a **co-equal second pillar** of the core
   or a **flagship optional module** (it needs Docker, ~2,550 separable LOC)? The README currently
   leads with it *and* the runtime is built around a different headline (tenant isolation-by-construction
   + tiered memory) — two competing "the differentiator" sections. Under analysis. See
   [`consolidation-proposal.md`](consolidation-proposal.md).
2. **Worthwhile gaps to close (on-thesis consolidation):**
   - **k8s manifests + Helm chart** — completes the deploy story for the multi-tenant/enterprise pitch;
     the binary is already deployable, this just ships the manifests.
   - **Docker footprint/weight benchmark** — the honest, verifiable number the arenas lack: image
     size / cold-start / RSS / dependency-tree weight per framework, run in a container. Do it for the
     **flagship few**, not all 12 arenas (consistent with the consolidation proposal's "12 → 3").
3. **Conscious skips (scope-creep unless real demand):** PDF ingestion (BYO is fine), TOML config
   (env + YAML cover it), openspec.dev, SDD-as-a-product-feature.

## How the pieces fit at request time

See [`runtime-as-built.md`](runtime-as-built.md) for the exact as-built lifecycle. In one line:
`Huma validates the struct-contract → identity into ctx → pre-spend quota gate → guardrails (input
redaction) → memory hydration (working + facts FTS5 + semantic RAG, tenant-scoped) → LLM (reflection
loop) → guardrails (output) → cost meter + OTel span (tenant-native) → SSE with ctx-cancel → async
transactional-outbox consolidation`.
