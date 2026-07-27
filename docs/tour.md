---
type: Guide
title: "The Capability Tour"
description: A 12-stop guided tour of Aisotropy's capabilities, each stop grounded in a runnable command and the test, arena result, measured number, or ADR that proves it — including the dimensions where Aisotropy does not win.
tags: [tour, capabilities, evidence, arena, onboarding, adoption]
timestamp: 2026-07-23T00:00:00Z
---

# The Capability Tour

This is a walk through what Aisotropy actually does, with proof at every stop. The house rule
(inherited from the repo's evidence discipline): **a capability claim without a command, a test,
a measured number, or an ADR is not made here.** Every stop has three parts — *What* (a mechanism,
not an adjective), *See it yourself* (a runnable command; most run keyless against the stub LLM),
and *The evidence* (where the proof lives). Stops that show a loss are included on purpose:
a tour that only shows wins is marketing, and this repo audits itself with the same severity it
applies to competitors ([symmetric rigor](architecture/capability-absence-claims.md)).

If you're brand new, read [Getting Started](getting-started.md) first (the 2-minute keyless
agent); come back here to see the substrate underneath it.

**Prerequisites for the Go commands:** a clone of the repo and Go 1.26+ (pinned via
`.go-version`). No API key, no Docker, no Python — exceptions are flagged per stop.

---

## Stop 1 — The struct is the single contract

**What.** One Go struct drives three things at once: HTTP validation, the generated OpenAPI 3.1
schema, and the shape enforced on the LLM's output. There is no second schema to drift — change
the struct and all three surfaces change together, which is why a JSON-tag change is treated as a
breaking-change gate.

**See it yourself** (keyless — the stub LLM answers):

```bash
go run ./cmd/aisotropyd &
curl -s localhost:8080/openapi.json | head -40   # the schema Huma generated from the structs
```

**The evidence.**
- [`docs/vision/thesis.md`](vision/thesis.md) §"The struct as the single source of truth" — the stance.
- [ADR-0002](decisions/0002-anti-framework-magic-struct-as-contract.md) — the anti-magic decision behind it.
- [`runtime-as-built.md`](architecture/runtime-as-built.md) §1 — the exhaustive, code-grounded route table
  (every `/v1` route is a Huma operation whose input/output structs *are* the contract).

## Stop 2 — Multi-tenant isolation by construction

**What.** `TenantID`/`UserID` enter `context.Context` once at the boundary and scope every memory
tier below; there is no consumer-facing way to construct a cross-tenant read. Over HTTP, a
verified JWT principal must **agree byte-for-byte with the path identity** or the request is 403
(`internal/api/authn.go:110`) — and with no authenticator configured the server is deny-all, not
trust-the-path.

**See it yourself** (keyless; boots the real HTTP surface with two tenants sharing a `session_id`
and asserts the second tenant can never see the first one's secret, across working, facts, and
semantic memory, sequential and concurrent):

```bash
go test -run 'TestIsolation.*NoCrossTenantLeak' ./aisotropy -v
```

**The evidence.**
- [`aisotropy/isolation_e2e_test.go`](../aisotropy/isolation_e2e_test.go) — the black-box proof above.
- The constitutional gate: tenant identity travels only via ctx, and Qdrant filtering happens
  *during* the HNSW scan, never as a post-filter ([multi-tenancy](architecture/multi-tenancy.md)).
- The live arena's `isolation_leak` dimension: aisotropy's own suite is 8/8 with no leak
  ([`arenas/live/RESULTS.md`](../arenas/live/RESULTS.md) §"Scoped memory & tenant isolation") —
  and the same table honestly notes the competitor gradation (scoping *parameters* vs. scoping
  *by construction*; see the L0–L3 ladder in
  [`arena-benchmark-methodology.md`](architecture/arena-benchmark-methodology.md)).
- The README's headline claim is backed by a reproduced cross-tenant leak in a rival's
  convention-based setup: [`arenas/genkit-go`](../arenas/genkit-go).

## Stop 3 — Tiered memory by access temperature

**What.** Four stores, ordered like a memory hierarchy: BadgerDB working memory (µs, in-process)
→ SQLite+FTS5 / rqlite facts (ms) → Qdrant / in-process cosine semantic vectors → an offline
DataFusion "sleep-cycle" consolidator. Writes flow tier-down through a transactional outbox
(message + consolidation intent commit atomically), and every read/write is scoped by
`(tenant, user[, session, product])`.

**See it yourself** (keyless — all three tiers with their in-process defaults):

```bash
go test ./internal/memory ./internal/facts ./internal/semantic
```

**The evidence.**
- [`memory-tiers.md`](architecture/memory-tiers.md) — the architecture; the outbox is in
  [`runtime-as-built.md`](architecture/runtime-as-built.md).
- Live arena, real model: `memory_cross_session` 2/2, `memory_semantic_recall` 2/2 (a paraphrase
  recall sharing *zero* content tokens with the needle), `memory_consolidation` 2/2
  ([`arenas/live/RESULTS.md`](../arenas/live/RESULTS.md), primary deterministic table).
- Honest caveat, copied from the arena itself: those three cells are served by the *semantic tier*
  (ingest → e5 → vector recall) — the dedicated conversational path (working memory → outbox →
  consolidation) does not yet do semantic recall on the non-streaming chat route; that gap is
  disclosed in RESULTS.md and tracked as a product follow-up.

## Stop 4 — Quota before spend + circuit breaker

**What.** The per-tenant token-bucket check runs *before* any LLM call — a tenant over its limit
is rejected (or degraded to a cheaper model under pressure) without spending a single token
(`internal/api/stream_core.go:163-175`). On the OpenAI-compat shim the rejection maps to
`429` + `rate_limit_error`, the shape stock OpenAI SDKs key their backoff on.

**See it yourself** (keyless):

```bash
# A tenant cannot exceed its bucket under concurrent load (the constitutional test):
go test -run TestLimiterConcurrentCannotExceedBurst ./internal/memory -v
# The shim maps a drained bucket to 429/rate_limit_error, pre-spend:
go test -run TestOpenAICompat_QuotaRejectionIs429RateLimit ./internal/api -v
```

**The evidence.**
- `stream_core.go:163-175` — the gate sits above the model call in the one shared pipeline.
- Commit `dcab1ac` — "quota rejection on the OpenAI shim is 429/rate_limit_error (was 500)",
  with the drain-the-bucket transcript in its message.
- Quota-pressure degrade to a cheap model: `stream_core.go:247-260`
  ([granular control](architecture/granular-control.md) covers the circuit breaker and cost meter
  around it).

## Stop 5 — Streaming + cancellation = barge-in

**What.** SSE token streaming where a client disconnect cancels the upstream LLM call immediately
(`stream_core.go:278-289`) — token spend stops at the producer, and the partial turn is
deliberately not persisted. This is not just polish: it is what makes voice interruption
("barge-in") work, and it measured at **0.1 ms median** cancel-to-silence through the voice PoC.

**See it yourself** (keyless):

```bash
go test -run TestChatStream_ClientCancellationStopsStream ./internal/api -v
# or feel it: stream a reply and hit Ctrl-C mid-stream
go run ./cmd/aisotropyd &
curl -N -X POST localhost:8080/v1/tenant/acme/user/alice/chat/stream \
  -H 'content-type: application/json' \
  -d '{"prompt":"hello","product_id":"tour","session_id":"s1"}'
```

**The evidence.**
- `internal/api/stream_core.go:278-289` — cancellation semantics, in the code comments.
- Constitutional gate: "a cancelled client request must stop the upstream LLM call".
- Measured: barge-in-to-silence median **0.1 ms** (5 runs, 2026-07-23) via
  [`examples/voice-pipecat/measure_bargein.py`](../examples/voice-pipecat/README.md) — the
  command, not just the number, ships in the PoC.

## Stop 6 — Guardrails + the fail-closed reflection loop

**What.** Input PII/secret redaction always runs — the redacted text is what reaches the model
*and* every memory tier, so a raw secret never lands anywhere. Injection/output detection is
always on; blocking is opt-in. Separately, structured-output routes fail **closed**: a persistent
schema violation after bounded reflection retries is a 502, never a silent fallback to
unstructured text.

**See it yourself** (keyless):

```bash
go test -run TestGuardrails ./internal/api -v            # redaction, injection, output blocking
go test -run 'TestAsk_.*Returns502' ./internal/api -v    # upstream failure -> 502, never empty 200
```

**The evidence.**
- [`internal/api/guard.go`](../internal/api/guard.go) + `internal/guardrails` — posture documented
  in the file header (annotate+log by default, block by env toggle).
- The chat route's 502-on-schema-violation contract: `internal/api/chat.go:307-313` and the
  [route table](architecture/runtime-as-built.md).
- [ADR-0070](decisions/0070-structured-output-rider-safeguard-fix.md) — a real bug this machinery
  caught and fixed: the structured-output rider's *placement* suppressed refusals under
  adversarial prompts (see Stop 11's safeguard score for the honest before/after).

## Stop 7 — One pipeline, three wire costumes

**What.** `StreamCore` (`internal/api/stream_core.go:137`) is the single streaming pipeline;
three protocol adapters re-encode it without duplicating it: the **Vercel AI SDK** UI-message
stream (`aisdk.go`), **AG-UI/CopilotKit** (`agui.go`), and the **OpenAI Chat-Completions shim**
(`openai_compat.go`, ADR-0066). Because they share one body, isolation, quota, guardrails, and
cost accounting hold on all of them by construction — and one `base_url` change points Pipecat,
Open WebUI, or LibreChat at Aisotropy with zero client modification.

**See it yourself** (keyless — 11 shim tests, including the round-trip through this repo's *own*
OpenAI client and the adversarial identity suite):

```bash
go test -run TestOpenAICompat ./internal/api -v
```

**The evidence.**
- [ADR-0066](decisions/0066-openai-compat-shim-voice-gateway.md) — the shim decision, including
  the principal-derived identity mode: the shim route has no tenant path params, so identity comes
  *only* from verified JWT claims; a NoAuth server still 401s it **by construction**.
- [`internal/api/openai_compat_test.go`](../internal/api/openai_compat_test.go) —
  `NoJWT_401`, `NoAuthServer_Still401`, `BodyCannotInfluenceIdentity`, `RoundTripInRepoClient`,
  golden stream framing.
- The [route table](architecture/runtime-as-built.md) rows for `chat/aisdk`, `chat/agui`, and
  `/v1/chat/completions`.

## Stop 8 — Voice: the sandwich, with a WER-gated test lane

**What.** The industry voice sandwich (STT → LLM → TTS) needs only an OpenAI-compatible backend,
so Aisotropy speaks with **zero audio code in the core**: Pipecat + faster-whisper + Kokoro point
at the Stop-7 shim with a tenant JWT as the api_key. The novel part is the *test lane*: TTS → STT
round-trips gated on word error rate, pinned WAV fixtures, and a file-based pipeline test — no
microphone, no speaker, no human ear.

**See it yourself** (Python 3.13; the deterministic floor runs anywhere with zero downloads):

```bash
cd examples/voice-pipecat
pip install -r requirements-test.txt
pytest tests/ -q            # lane off: 8 passed, 8 skipped (pure-stdlib WER unit tests)
# full lane (opt-in; needs the Kokoro container + ~74MB whisper model):
VOICE_LANE=real pytest tests/ -q
```

**The evidence.**
- [ADR-0069](decisions/0069-voice-testing-lane-wer-roundtrip.md) — measured WER table
  (**0.000–0.100** across six calibration sentences; worst error was "nine" → "9", formatting not
  intelligibility), and the threshold derivation (2× headroom, the `passQAThreshold` pattern).
- [`examples/voice-pipecat/README.md`](../examples/voice-pipecat/README.md) §"Honest status" —
  the full lane executed end-to-end 2026-07-23: **16/16 passed in 64s** against the real binary.
- Measured latency, stated honestly: median TTFAB **3543 ms** with the Kokoro **CPU** image —
  and the runtime side (the shim) contributes ~**3 ms** of it. The bottleneck is the TTS
  container's first audio byte, named as such; production voice needs the GPU image or streaming
  TTS. Landscape + license table:
  [`voice-pipeline-landscape-2026-07-23.md`](research/voice-pipeline-landscape-2026-07-23.md).

## Stop 9 — Key-brokering: `RunTask` and the key that never enters the sandbox

**What.** An untrusted agentic coding task runs in a throwaway container that gets the agent's
*hands* (shell, file writes, git) but never its *wallet*: the provider key stays host-side, and
`TaskRequest` has no field to pass one — by design. Opt-in behind `-tags keybroker` so the
default binary carries no Docker intent.

**See it yourself** (this one needs Docker — it proves the invariant against a real container):

```bash
go test -tags keybroker -run TestRunTask_BrokerInvariant_KeyNeverEntersContainer ./internal/taskagent -v
# asserts: `docker exec printenv` never shows the key, while the task still completes and commits
```

**The evidence.**
- [`internal/taskagent/runtask_test.go:85`](../internal/taskagent/runtask_test.go) — the invariant test.
- [ADR-0036](decisions/0036-sandboxed-task-agent-key-brokering.md) — the decision.
- [`arenas/key-brokering`](../arenas/key-brokering/README.md) — the honest situating: host-side
  inference with a sandboxed toolbelt is the standard "sandbox-as-tool" pattern (table stakes
  across E2B/Daytona/Modal); Aisotropy's narrower, real edge is against coding-CLI-in-a-box
  orchestrators that must inject the credential into the box.

## Stop 10 — Observability: OTel `gen_ai` spans, `/costs`, `/health` — and what's not built yet

**What.** OTLP trace export (env-gated, no-op by default) with `gen_ai.*` semantic-convention
spans carrying `tenant_id`/`user_id` natively; a per-tenant cost meter at `GET /costs`
(default-closed behind an ops token); liveness (`/healthz`) and readiness (`/health`) probes that
deliberately never call the LLM — polling must not spend tokens.

**See it yourself** (keyless):

```bash
go run ./cmd/aisotropyd &
curl -s localhost:8080/health          # readiness: tier probes, 200 ok / 503 degraded
curl -s localhost:8080/costs           # 503 — default-closed until AISOTROPY_OPS_TOKEN is set (ADR-0025)
# local trace viewing: docs/examples/observability/docker-compose.observability.yml (Aspire, ADR-0065)
```

**The evidence.**
- [`runtime-as-built.md`](architecture/runtime-as-built.md) route table — `/health`, `/costs`,
  the ops gate, and the "no LLM probe" rationale.
- [ADR-0065](decisions/0065-local-otel-backend-aspire-dashboard.md) — the local OTLP viewer is the
  Aspire Dashboard (MIT), chosen over AGPL/heavy alternatives; overlay at
  `docs/examples/observability/`.
- [ADR-0068](decisions/0068-observability-architecture-three-signals.md) — the three-signals
  target architecture. Honest status: **Layers 1–2 landed** (typed tag vocabulary in
  `internal/o11y/keys` + trace↔log correlation, commit `0da9ce6`); Layer 3 (downstream client
  child-spans) and Layer 4 (logs over OTLP + metrics) are accepted but **not yet implemented** —
  today a trace shows one or two spans where a request performs a dozen operations, exactly as the
  ADR's own context section says.
- [ADR-0071](decisions/0071-k8s-deploy-validation-lanes.md) — those same two probes are what the
  shipped Helm chart and Kustomize manifests wire as liveness/readiness, and
  `scripts/k8s-chart-guard.sh` asserts it offline in ~1 s across both surfaces (a probe pointing at
  a path the binary does not serve is a silent outage). Honest boundary in the same ADR: object
  schemas go unchecked without `kubeconform`, and the real-cluster lane is opt-in and was not run.

## Stop 11 — The arena: how Aisotropy measures itself against 8 rivals (and where it loses)

**What.** The [arena programme](../arenas/README.md) is the evidence base for every competitive
claim: a 12-framework adversarial isolation matrix, plus a **live arena**
([`arenas/live/`](../arenas/live/SPEC.md)) where 9 frameworks run the *same real model*
(`agnes-2.0-flash`) on the same tasks — so any difference in a column is the framework's, not the
model's. Two structural principles keep it honest:

1. **Deterministic judging.** The primary quality score is a pure function per dimension
   (`arenas/live/runner/scoring` — no LLM, no network, no randomness), because an LLM judging its
   own model family scored everything a flat ~5.0 and discriminated nothing. The LLM-judge exists
   only as a secondary, opt-in (`-llm-judge`), self-judge-caveated signal.
   See [`arena-deterministic-judging.md`](architecture/arena-deterministic-judging.md).
2. **No blank cells.** Every `(adapter × dimension × lane × form)` cell is a real result or an
   explicit `infeasible:<reason>` — silent omission is treated as an arena defect. Machine-checked
   via each adapter's `matrix.json` ([`MATRIX-SCHEMA.md`](../arenas/live/MATRIX-SCHEMA.md);
   coverage law in [`SPEC.md`](../arenas/live/SPEC.md)).

**See it yourself** (keyless — the guard that enforces principle 1, purity + 500-run
byte-identical reproducibility):

```bash
bash scripts/arena-determinism-guard.sh
```

**The evidence — including the losses.** From the 2026-07-23 run of
[`arenas/live/RESULTS.md`](../arenas/live/RESULTS.md), deterministic primary table:

- **Wins:** `rag` 16/16 (hit@1 11/11, trap-refusal 5/5), `tools` 5/5 correct,
  `memory_cross_session` / `memory_semantic_recall` / `memory_consolidation` all 2/2 — the
  semantic-recall cell is one only 3 of 9 frameworks passed. `isolation_leak`: no leak, 8/8 suite.
- **Losses and warts, verbatim from the same table:**
  - `safeguard` **5/7 (0.8)** — beaten by mastra (7/7) and eino/genkit-go (6/7). The root cause
    was found and fixed after the run ([ADR-0070](decisions/0070-structured-output-rider-safeguard-fix.md):
    rider placement; a real A/B shows 7/7 post-fix, n=1), but the published board says 5/7 until a
    clean re-run says otherwise.
  - **Framework overhead:** on identical qa work Aisotropy sends **+28% input tokens** (355 vs the
    277 leanest — that delta is its per-turn memory hydration, a feature doing work, but the cost
    is real), and its native TTFT (**22173 ms**) and warm p50 (2234 ms vs eino's 667 ms) are far
    from the front of the pack.
  - `memory` (single-session replay mode): **all 8 runs errored** this pass — an infrastructure
    signal, not a quality score, and RESULTS.md refuses to convert it into either a pass or a fail.
  - **Docker image 135.6 MiB** vs eino's 9.1 MiB and genkit-go's 7.8 MiB.
- Also honest by construction: `qa`/`structured`/`stream` are labeled smoke tests in RESULTS.md
  itself (the model is held constant, so they cannot discriminate frameworks), and the aisotropy
  memory adapter's call-count token asymmetry is disclosed as a contract property, not hidden.

## Stop 12 — The evidence culture is itself a feature

**What.** The guards that produced everything above are executable and refuse to absolve: a
deletion claim must survive `prune-guard` (five live-signal probes; a guard that *cannot* evaluate
exits BLOCKED rather than green), arena scoring must survive the determinism guard, heavy local
lanes must survive a machine resource guard, and voice must survive WER thresholds calibrated on
measured runs. Docs that state volatile facts cite the command that produced them.

**See it yourself:**

```bash
scripts/prune-guard.sh internal/routing     # exits 3 BLOCKED: importers + coverage + wired + exported
scripts/resource-guard.sh                   # is this machine fit for a heavy lane right now?
bash scripts/arena-determinism-guard.sh     # purity + reproducibility of the arena's primary score
scripts/dormant-lanes.sh                    # status of the dormant paths — spends nothing
```

The last one is the same discipline pointed at *dormancy*: the Rust workers and the networked
memory tiers had code and had tests, but nothing invoked them. `scripts/dormant-lanes.sh --stage
all --yes` builds both Rust workers, runs the `-tags manual` live tests against the real binaries,
stands up an isolated rqlite + Qdrant, runs the real-service tests, and tears it all down —
each heavy step under a **declared, measured** RAM budget. See
[Dormant lanes](deployment/dormant-lanes.md).

**The evidence.**
- [`prune-evidence-gate.md`](architecture/prune-evidence-gate.md) — including the origin story:
  the guard exists because a doc-sourced "dead code" claim nearly deleted a live isolation seam,
  and because the guard itself once returned a false green on a nested module (it now fails
  closed).
- [`machine-resource-guard.md`](architecture/machine-resource-guard.md) — reject-before-spending,
  applied to the developer's own machine.
- [`capability-absence-claims.md`](architecture/capability-absence-claims.md) and
  [`dependency-reuse-first.md`](architecture/dependency-reuse-first.md) — the rules that force
  ecosystem sweeps before "they lack X" and source reading before "we need to build X".
- The [reality ledger](architecture/reality-ledger.md) — the standing REAL / DORMANT /
  ASPIRATIONAL classification this tour's claims defer to.

---

## Where to go next

- [Getting Started](getting-started.md) — the 30-second pitch and the keyless 2-minute agent.
- [The Tutorial](tutorial/index.md) — a 10-rung ladder, each rung a runnable example.
- [Capabilities & gaps](architecture/capabilities-and-gaps.md) — the honest REAL / PARTIAL / GAP
  snapshot this tour summarizes.
- [The knowledge base index](index.md) — vision, architecture, components, ADRs, research bets.
