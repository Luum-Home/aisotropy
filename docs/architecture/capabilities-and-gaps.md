---
type: Reference
title: "Capabilities & gaps — honest, verified snapshot"
description: A grounded state-of-the-art of what aisotropy actually does today vs. what is a gap, across observability, RAG, memory tiers, guardrails, deployment (k8s), configuration formats, standards interop (agents.md/MCP/SDD), the Karpathy LLM-OS lineage, and benchmarks. Every row is verified against the code/repo, not aspirational. Companion to the reality-ledger (per-feature REAL/DORMANT/ASPIRATIONAL) and the consolidation proposal.
tags: [architecture, capabilities, gaps, honesty, deployment, k8s, config, observability, rag, guardrails, benchmarks, standards, karpathy]
timestamp: 2026-07-17T00:00:00Z
---

# Capabilities & gaps — honest, verified snapshot

> **Update addendum (2026-07-23).** The original 2026-07-17 rows below are unchanged; the
> "2026-07-23 additions" table after the summary covers what landed since — the OpenAI-compat
> shim + wire-adapter surface, the voice test lane, the ADR-0068 observability layers, the
> prompt-injection defense tiers, and the retrieval/answer-quality eval harnesses — each row
> re-verified against code/commits on 2026-07-23.

> **Correction (2026-07-27) — the "Configuration" row was STALE, not merely outdated.** It was
> published claiming `GAPS: no TOML; no single runtime config file (yaml/toml) — env-only`, but
> the YAML+TOML runtime config file had **already landed the same day** (`bfeca15`, 2026-07-17
> 10:49, ADR-0040) — and the row survived a later doc edit on 2026-07-24 without being caught.
> The row is now ✅ REAL and re-verified against `aisotropy/runtimeconfig.go` +
> `aisotropy/runtimeconfig_test.go`, not against this document's own prior claim. Filed as a
> doc-truth defect: a *capabilities* doc that under-claims a shipped feature is the same class of
> error as one that over-claims an unshipped one — both were read as truth by a later reader
> (here, an agent tasked to "close the config gap", which would have rebuilt an existing feature
> and allocated a duplicate ADR). Verified this pass: the config file exists, is wired into
> `startRuntime`, and is covered by 20 tests.

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
| **Configuration** | ✅ REAL | Runtime config = **environment variables** (`AISOTROPY_*`) **and an optional runtime config FILE** in **YAML or TOML** (`aisotropy/runtimeconfig.go`, ADR-0040, shipped `bfeca15` 2026-07-17). `RuntimeConfig` mirrors the env surface one-for-one (34 knobs, no new schema); precedence is **env > file > default**, implemented by materializing file values only into variables not already present, so **no file ⇒ nothing materialized ⇒ byte-identical boot** (proven by `TestLoadAndMaterialize_AbsentFileMutatesNothing`, a full-environment snapshot equality check). Resolution: `WithConfigFile(path)` → `AISOTROPY_CONFIG_FILE` → `./aisotropy.runtime.{yaml,yml,toml}`. Documented reference: **`aisotropy.runtime.example.yaml`** (repo root; every key commented out, so it is inert until opted into — guarded by `TestExampleRuntimeConfig_IsRealAndInert`, which also fails if it documents a knob the struct lacks). Plugins/primitives = **YAML manifests** (goccy/go-yaml; KindMCP/go-import/wasm). Skills = **Markdown** (`SKILL.md` → generated Go via `cmd/skill-import`). The struct-as-contract emits **JSON Schema**. **Bounded, by design:** secrets are **REFUSED** from the file (ADR-0072, superseding ADR-0040 §5): a file populating any of the five secret-bearing keys fails to load with `ErrSecretInConfigFile` and the runtime does not start — the value never reaches the caller, the environment, or a log line. Rationale: the repo is public, so an accidental `git add` of a filled-in config file is an irreversible disclosure; `.gitignore` is advisory, a refusing loader is structural. Guarded by `TestLoadRuntimeConfigFile_RefusesSecrets` (per key, YAML+TOML, value never echoed) and `TestSecretishEnvKeys_AreClassified`, a reflection-derived drift guard requiring every secret-*looking* env key to be registered or allowlisted with a reason. Secrets go in the environment or a secret manager; unknown keys are **lenient** (ignored, so one file can span mixed versions during a rollout) while malformed syntax and wrong value types **fail closed**; materialization mutates **process-global** env, so it is a startup-time seam, not designed for many `App`s loading different files concurrently in one process. |
| **Benchmarks** | 🟡 PARTIAL (honest) | [`arenas/BENCHMARKS.md`](../../arenas/BENCHMARKS.md): Class 1 = aisotropy's own **framework overhead** (model held constant as the keyless stub → every ns is the framework: identity→ctx, quota gate, hydration, tool gating, contract). Class 2 = **cost-governance effectiveness** (a token-counting stub proves the pre-spend quota gate + response cache cut spend). The cross-framework wall-clock table is a **documented reproducible method, NOT executed**. |
| **Deployment — Docker** | ✅ REAL | Distroless `Dockerfile`; reference `docker-compose.yml` (aisotropyd + real networked rqlite:4001 + Qdrant:6333/6334) + `docker-compose.sleepcycle.yml` overlay (ADR-0016 lever #3). Bare binary falls back to in-process defaults. |
| **Deployment — Kubernetes / Helm** | ✅ REAL (validated offline; real-cluster proof is a one-off, not a standing lane) | **Two hand-maintained, first-class surfaces ship.** Helm chart at `deploy/helm/aisotropy/` (Deployment, Service, ConfigMap, Secret, ServiceAccount, HPA, Ingress, NetworkPolicy, PVC, + rqlite/Qdrant StatefulSets for the networked tiers; `values.yaml` minimal-by-default, `values-prod.yaml` hardened) and raw Kustomize at `deploy/k8s/` (base + `networked-tiers`/`prod` overlays) for no-Helm workflows. Liveness `/healthz` + readiness `/health` are wired to the real endpoints (`aisotropy/app.go:1127` and `:1190`); resource requests/limits are laptop-sized; the chart references a Secret and never templates a credential in the prod posture; `autoscaling/v2` HPA is off by default and hard-fails without `resources.requests`. One-command local bring-up: `deploy/local/up.sh` (auto-detects Kind/k3d). **Validation ([ADR-0071](../decisions/0071-k8s-deploy-validation-lanes.md)):** `scripts/k8s-chart-guard.sh` is the standing gate — deterministic and fully offline (~2s, no cluster/build/network), rendering 10 postures across BOTH surfaces and asserting 146 checks (probe paths, no secret literal outside a `kind: Secret`, resources, pinned image tag, HPA/`replicas` ownership, pod hardening, plus negative tests that the chart REFUSES unsafe auth combinations). Mutation-tested, not merely asserted. **Honest deltas:** object schemas are **not** checked here (`kubeconform`/`kubeval` not installed — the guard reports an explicit SKIP, never a pass); the opt-in real-cluster lane (`scripts/k8s-cluster-lane.sh`, four gates incl. `resource-guard`) has **not** been executed to completion in-repo, so "it boots on a real cluster" rests on the one-off manual Kind verification recorded in commit `3d39c44`, not on a repeatable lane; the offline gate validates declarative correctness and cannot prove the image starts. The `SandboxProvider` k8s backend remains a doc sketch, unrelated to this deployment surface. |
| **PDF / HTML ingestion** | ✅ REAL | `App.IngestFile` (`aisotropy/ingest_file.go`) extracts text from a PDF or HTML document and feeds it through the SAME scoped `IngestKnowledge` chunk→embed→store pipeline — same `(tenant, user)` isolation, no new unscoped path. PDF via `github.com/ledongthuc/pdf` (BSD-3), HTML via `golang.org/x/net/html` (BSD-3); both permissive per the license gate (never `unidoc/unipdf`, AGPL). Extraction is a swappable seam (`WithExtractor`), mirroring BYO-embedder. **Bounded:** built-in extraction reads the embedded text layer only — a scanned/image-only PDF yields `ErrEmptyContent` and needs a BYO OCR `Extractor`. `IngestKnowledge` (plain text in) is unchanged and additive. |
| **openspec.dev** | ❌ NOT SUPPORTED (deliberate, [ADR-0039](../decisions/0039-openspec-dev-spec-driven-development.md)) | OpenSpec is a **dev-time** spec-driven-development tool for AI coding assistants (markdown `openspec/specs` + `openspec/changes`, MIT, ~61k★) — it slots into ADR-0022's **AGENTS.md (dev-time) layer**, not a runtime import surface. Its artifacts map to no aisotropy primitive the generic knowledge path (ADR-0018) doesn't already cover, so no importer is built; and the `docs/decisions/` ADR log + `sdd-*` harness skills already provide in-repo SDD. Deferred with explicit revisit triggers (see ADR-0039). |
| **Spec-Driven Development (SDD)** | 🟡 DEV-PROCESS ONLY (deliberate, [ADR-0041](../decisions/0041-sdd-as-consumer-feature.md)) | SDD is used to **build** aisotropy (the agent tooling under `.claude/`), not a capability aisotropy offers to consumers. aisotropy-the-product does not "support SDD" as a feature — ADR-0041 deferred it: the interpretations that have value are already served by the ADR-0016 wiring `Config`, the AGENTS.md dev layer, or app composition, and the novel one (behavior-in-spec) is forbidden by ADR-0002. |

## 2026-07-23 additions (verified against code/commits, same REAL/PARTIAL/GAP discipline)

| Dimension | Status | Detail (verified 2026-07-23) |
|---|---|---|
| **OpenAI-compat shim + wire-adapter surface** | ✅ REAL (tested) | Three protocol adapters over the single `StreamCore` pipeline: Vercel AI SDK (`internal/api/aisdk.go`), AG-UI/CopilotKit (`internal/api/agui.go`), and the OpenAI Chat-Completions shim `POST /v1/chat/completions` (`internal/api/openai_compat.go`, [ADR-0066](../decisions/0066-openai-compat-shim-voice-gateway.md)). The shim is the one route outside the tenant/user prefix — identity exclusively from verified JWT claims (principal-derived mode of `BindAuthMiddleware`); a NoAuth server still 401s it by construction. 11-test suite incl. adversarial identity + in-repo client round-trip (`internal/api/openai_compat_test.go`); quota rejection maps to 429/`rate_limit_error` pre-spend (commit `dcab1ac`). **Bounded:** text-only v1 floor — no tool calls, no `/v1/responses`, no `GET /v1/models` (ADR-0066 non-goals). |
| **Voice (PoC + test lane)** | ✅ REAL (opt-in lane, measured) | Zero audio code in the core; `examples/voice-pipecat/` builds the STT→LLM→TTS sandwich over the shim (Pipecat BSD-2 + faster-whisper MIT + Kokoro-FastAPI Apache-2.0). Hardware-free test lane per [ADR-0069](../decisions/0069-voice-testing-lane-wer-roundtrip.md): TTS→STT round-trip gated on WER (measured 0.000–0.100 at calibration; threshold 0.20 = 2× headroom), pinned WAV fixtures, file-based Pipecat pipeline test — executed end-to-end 2026-07-23, **16/16 passed** against the real binary. Measured latency: barge-in-to-silence median 0.1 ms; TTFAB median 3543 ms on the Kokoro **CPU** image (the TTS container is the bottleneck; the shim leg is ~3 ms). **Bounded:** lane is operator-run (`VOICE_LANE=real`), never per-PR CI; `bot.py` mic/speaker path needs real hardware and has not been run. |
| **Observability — three signals (ADR-0068)** | 🟡 PARTIAL | Target architecture accepted 2026-07-23 ([ADR-0068](../decisions/0068-observability-architecture-three-signals.md)). **Landed: Layers 1–4 (code)** — typed tag vocabulary (`internal/o11y/keys`) + trace↔log correlation with boundary auto-attribution (`internal/o11y/handler.go`, `internal/api/o11y_bridge.go`, commit `0da9ce6`); HTTP tracing decorator (`internal/o11y/roundtripper.go`) turning every outbound HTTP call into a child span; OTLP-push MeterProvider with RED instruments per layer (`internal/o11y/metrics.go`). **Not yet implemented: OTLP logs fan-out** (`otelslog` bridge → OTLP logs) and the **masking-package unification** (structural-attribute allowlist applied to both spans and logs). Today a trace shows child spans per HTTP call and RED metrics are collected, but logs still lack OTLP fan-out. Local OTLP viewer decided: Aspire Dashboard, MIT ([ADR-0065](../decisions/0065-local-otel-backend-aspire-dashboard.md); overlay at `docs/examples/observability/`). |
| **Prompt-injection defense (ADR-0057)** | 🟡 PARTIAL | Heuristic injection **detection always on** in the request path (`internal/guardrails/injection.go`; blocking opt-in). Tier 1: warm mDeBERTa guardrail daemon (`crates/guardrail-model --serve` + Go `DaemonScanner`) — measured p50 ~124 ms/call warm vs ~0.7–2.2 s cold, 10/12 on a hand-labeled multilingual smoke set; explicitly additive/fail-open, NOT a standalone gate. Tier 2: dual-LLM quarantine seam (`internal/guardrails/quarantine.go`) with a structural proof it cannot emit a tool invocation — **built + tested but NOT wired into the agent loop** (adoption is a named follow-on in [ADR-0057](../decisions/0057-prompt-injection-defense-daemon-and-quarantine.md)). |
| **Retrieval quality eval (ADR-0058)** | ✅ REAL (measured, generic gold set) | `bench/rag-quality`: XQuAD EN/ES parallel gold set through the real e5 embedder + tenant-scoped semantic store — measured recall@{1,5,10}, MRR@10, nDCG@10; real-e5 macro-avg R@1=0.92 / nDCG@10=0.97, cross-lingual R@1=0.89 (vs a 0.03 hash-embedder floor). **Honest gap (from [ADR-0058](../decisions/0058-retrieval-quality-eval-measured-recall.md) itself):** a generic public benchmark is not aisotropy's multi-tenant conversational-memory RAG; a domain gold set is the next step. |
| **Answer quality eval (ADR-0059)** | ✅ REAL (harness; operator-run) | `bench/answer-quality`: runs each question through the FULL turn pipeline (`App.Turn` — isolation, quota, guardrails, hydration, reflection, cost) and grades with a provider-agnostic LLM judge (correctness/faithfulness/helpfulness + refusal/hallucination/latency/token spend), bounded spend caps. **Honest gap (from [ADR-0059](../decisions/0059-answer-quality-eval-end-to-end.md)):** the bundled generic set is a wiring smoke; only the operator's own domain Q&A yields production-meaningful numbers. Note this is an offline eval harness — the live arena's PRIMARY score stays deterministic per `docs/architecture/arena-deterministic-judging.md`. |

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
   (env + YAML cover it), openspec.dev (now formalized as a defer decision,
   [ADR-0039](../decisions/0039-openspec-dev-spec-driven-development.md)), SDD-as-a-product-feature.

## How the pieces fit at request time

See [`runtime-as-built.md`](runtime-as-built.md) for the exact as-built lifecycle. In one line:
`Huma validates the struct-contract → identity into ctx → pre-spend quota gate → guardrails (input
redaction) → memory hydration (working + facts FTS5 + semantic RAG, tenant-scoped) → LLM (reflection
loop) → guardrails (output) → cost meter + OTel span (tenant-native) → SSE with ctx-cancel → async
transactional-outbox consolidation`.
