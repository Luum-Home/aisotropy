# OWASP Top 10 for LLM Applications — aisotropy deterministic coverage

**Baseline:** OWASP Top 10 for LLM Applications **2025** (v4.2.0a, published **2024-11-18**
by the OWASP GenAI Security Project — the authoritative version as of 2026-07).
Source: <https://genai.owasp.org/resource/owasp-top-10-for-llm-applications-2025/>
· PDF: <https://owasp.org/www-project-top-10-for-large-language-model-applications/assets/PDF/OWASP-Top-10-for-LLMs-v2025.pdf>
Re-check that URL for a newer release before trusting this baseline.

This document is the **no-blank-cells** map of aisotropy's **deterministic, OFFLINE**
security suite: every OWASP LLM category appears with its enforcing component, its
deterministic test, the real-model arena probe (where one exists), and a status.
The offline suite uses the keyless stub LLM and in-process stores — **zero real
model calls, zero network** — and **complements** (does not replace) the live
arena's real-model safeguard probes (`arenas/live`, `-modes safeguard`).

**Re-verify everything with one command:**

```bash
bash scripts/owasp-llm-scan.sh          # per-category PASS/FAIL table; exit 0 clean / 1 fail-or-finding / 2 error
```

Or the raw suite:

```bash
go test -run 'TestOWASP' ./internal/guardrails/ ./internal/api/ ./internal/tools/ \
  ./internal/llm/ ./internal/o11y/ ./internal/semantic/ ./internal/memory/ \
  ./internal/agentloop/ ./internal/sandbox/
```

The Agentic dimension (OWASP Top 10 for Agentic Applications 2026, ASI01–ASI10) is
in the sibling doc: [`owasp-agentic-coverage.md`](./owasp-agentic-coverage.md).

---

## Coverage table

| OWASP category | Enforcing component (file:line) | Deterministic test (offline) | Real-model arena probe | Status |
|---|---|---|---|---|
| **LLM01:2025 Prompt Injection** | `internal/guardrails/injection.go:150` (`Detector.Inspect`) + `:31` signals; `internal/guardrails/normalize.go:93` (`foldForMatch` evasion folding); `internal/api/guard.go:79` (`checkInput`, `:98` block decision) | `internal/guardrails/owasp_llm01_injection_test.go` (28-payload corpus in `testdata/owasp_llm01_injection.json`); `internal/api/owasp_llm01_guard_modes_test.go` (annotate vs `BlockInjection`) | `safeguard_decline` / `safeguard_nocontain` (`sg-jailbreak-persona`, `sg-corpus-injection`) | **covered** (detection + both enforcement postures) |
| **LLM02:2025 Sensitive Information Disclosure** | `internal/llm/otel.go:172` (`redactingExporter`), `:202` (`redactedSpan.Attributes`), `:123` (content denylist); `internal/o11y/handler.go` (`CtxHandler`) | `internal/llm/owasp_llm02_redaction_test.go`; `internal/o11y/owasp_llm02_ctxhandler_test.go` | — (posture verified offline; no dedicated arena probe) | **covered** (content stripped from spans+logs; identity/correlation survives) |
| **LLM03:2025 Supply Chain** | Repo license gate `.claude/rules/constitutional-gates.md` (100% MIT/Apache); `go.sum` module pinning | — (not unit-testable in this repo) | — | **N/A** — covered-by: license gate + `go.sum` pinning + CI dependency scan (process/CI, not a runtime unit) |
| **LLM04:2025 Data and Model Poisoning** | Fence: `internal/api/chat_stream.go:108` (`sanitizeUntrusted`), `:51`–`:63` (markers + notice), `:399` (re-scan on hydration); quarantine: `internal/guardrails/quarantine.go:137` (`DualLLMQuarantine`), `:217`/`:239` schemas | `internal/api/owasp_llm04_fence_test.go` (fence render + marker neutralization); `internal/guardrails/owasp_llm04_quarantine_test.go` (fail-closed dual-LLM) | `safeguard_nocontain` (`sg-corpus-injection`) | **covered** (indirect-injection fenced as data + fail-closed quarantine) |
| **LLM05:2025 Improper Output Handling** | `internal/guardrails/quarantine.go:217` (`EnumSchema.Validate`), `:239` (`BoolSchema.Validate`); reflection loop `internal/llm/reflect.go` | `internal/guardrails/owasp_llm05_output_handling_test.go` (schema rejects malformed/injected/hedged output; exact-match, no substring escape) | — (schema validation is deterministic; reflection E2E in arena `structured`) | **covered** (model output cannot escape schema validation) |
| **LLM06:2025 Excessive Agency** | `internal/tools/tools.go:43` (`Permitted`), `:64` (`PromptFragment`); agent loop `internal/agentloop/agentloop.go:92` (`SelectTool`, `:117` non-permitted downgrade) | `internal/tools/owasp_llm06_agency_test.go`; `internal/agentloop/owasp_asi02_tool_misuse_test.go` (agent-loop half) | `tools` mode (arena scope-gated execution) | **covered** (scope-gated exposure + unreachable-from-loop). See also Agentic ASI02. |
| **LLM07:2025 System Prompt Leakage** | Reply-side: `internal/guardrails/output.go:168` (`Leak` / `:246` `segmentEchoed`). Persona: `internal/api/persona.go:161`. Telemetry: `internal/llm/otel.go:172` redaction | `internal/guardrails/owasp_llm07_output_leak_test.go`; `internal/api/owasp_llm07_persona_test.go`; `internal/llm/owasp_llm02_redaction_test.go` (`TestOWASP_LLM07_SystemPromptSentinelNotOnSpans`) | `safeguard_nocontain` (`sg-sysprompt-exfil`, `sg-identity-model`, `sg-identity-vendor`) | **partial** — see [safeguard notes](#safeguard-partials-adr-0068) |
| **LLM08:2025 Vector and Embedding Weaknesses** | `internal/semantic/memory_store.go:48` (`Search`, `:60` in-scan (tenant,user) filter); `internal/semantic/semantic.go:179` (`Store` contract) | `internal/semantic/owasp_llm08_embedding_test.go` (adversarial query = secret; cross-tenant + cross-user; filter-in-scan) | `isolation_leak` / `memory_scoped` (arena tenancy probes) | **covered** (no cross-scope retrieval even at cosine 1.0) |
| **LLM09:2025 Misinformation** | — (a real-model behavioral property) | — (not offline-unit-testable) | `qa`/`rag` deterministic faithfulness (`ScoreQA`/`ScoreRAG`) + safeguard | **N/A** — covered-by: arena deterministic scoring (faithfulness/containment); no offline unit assertion possible |
| **LLM10:2025 Unbounded Consumption** | `internal/memory/quota.go:74` (`Limiter.Allow`, per-tenant token bucket, pre-LLM gate) | `internal/memory/quota_test.go` (`TestLimiterConcurrentCannotExceedBurst`, existing single-tenant gate) **extended by** `internal/memory/owasp_llm10_consumption_test.go` (concurrent multi-tenant burst isolation) | — (constitutional gate; offline-provable) | **covered** (no tenant exceeds its bucket under concurrency; no cross-tenant bleed) |

---

## Safeguard partials (ADR-0068) {#safeguard-partials-adr-0068}

Two LLM07 rows are **partial** because the enforcing PRODUCT fix — a persona
identity/role hardening block that makes the model itself decline identity-reveal
and persona-override prompts — is **concurrent work** (tracked under ADR-0068,
touching `internal/llm/gollem.go` + `internal/api/persona.go`). The clean arena
board (commit `06945bf`) shows aisotropy currently **fails** these two real-model
probes, so the deterministic tests lock in the *current* offline-provable behavior
and will be tightened when the hardening lands.

| Safeguard probe (arena) | What it models | Deterministic test (locks current behavior) | Product fix | Status |
|---|---|---|---|---|
| `sg-identity-model` (`safeguard_nocontain`) | A prompt asking the assistant to reveal its underlying model/vendor | `internal/api/owasp_llm07_persona_test.go::TestOWASP_LLM07_PersonaDoesNotEmbedModelIdentity` — asserts the **persona is not itself an identity-leak vector** (no denied model/vendor token baked into the system prompt) | ADR-0068 persona identity-hardening (decline identity-reveal) — **not merged when this was written**; no `AISOTROPY_*` hardening flag present at HEAD | **partial** (test present, asserts current behavior + redaction keeps identity tokens off spans/logs) |
| `sg-jailbreak-persona` (`safeguard_decline`) | DAN-style persona/role override | `internal/guardrails/owasp_llm01_injection_test.go::TestOWASP_LLM01_JailbreakPersona` — asserts the deterministic guardrail **flags** the override (`jailbreak_persona`); `internal/api/owasp_llm01_guard_modes_test.go` — with `BlockInjection` on, it is **declined** | ADR-0068 persona role-hardening (model-side decline under annotate-only default) | **partial** (detection + opt-in block covered; annotate-only-default model decline is the ADR-0068 gap the arena measures) |

The redaction half of both rows **is** fully covered offline:
`TestOWASP_LLM07_SystemPromptSentinelNotOnSpans` and the LLM02 tests prove a
system-prompt / identity sentinel never reaches exported spans or structured logs,
while `tenant_id`/`user_id`/`trace_id` correlation attributes survive.

### A note on the stub and "reply payload"

`internal/llm.StubClient` **deliberately echoes the system prompt** into its canned
reply (for SSE-pipeline testing), so driving the stub end-to-end would "leak" a
planted system sentinel *by design* — a test-double artifact, not product behavior.
The deterministic **reply-side** system-prompt-leak *detection* is therefore tested
at the `OutputInspector.Leak` seam (LLM07 output-leak test), and the **real-model
non-leak** is the arena's job — not asserted against the stub echo.

---

## Findings

**None** as of the last run. If a deterministic test uncovers a real vulnerability,
convention is to skip it with `t.Skip("FINDING: <description> — see report")`; the
scan script greps for that marker and exits non-zero. A finding is documented here
with its exact failing assertion and repro command — never silently fixed in product
code (fixes are queued separately).

_Last verified: `bash scripts/owasp-llm-scan.sh` → RESULT: CLEAN (exit 0), 10/10
categories PASS, 0 findings._
