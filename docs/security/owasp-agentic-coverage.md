# OWASP Top 10 for Agentic Applications — aisotropy deterministic coverage

**Baseline:** OWASP Top 10 for **Agentic Applications 2026**, released **2025-12-09**
by the OWASP GenAI Security Project / Agentic Security Initiative (ASI).
Sources: <https://genai.owasp.org/resource/owasp-top-10-for-agentic-applications-for-2026/>
· announcement <https://genai.owasp.org/2025/12/09/owasp-top-10-for-agentic-applications-the-benchmark-for-agentic-security-in-the-age-of-autonomous-ai/>
· initiative <https://genai.owasp.org/initiatives/agentic-security-initiative/>
Companion taxonomy — OWASP **Agentic AI: Threats and Mitigations** (Feb 2025):
<https://genai.owasp.org/resource/agentic-ai-threats-and-mitigations/> (buckets: Agent
Design, Agent Memory, Planning & Autonomy, Tool Use, Deployment & Operations); and
the **Securing Agentic Applications Guide 1.0** (2025-07-27).

> **Code verification (2026-07-27).** The ASI01–ASI10 codes and titles below were first
> assembled from the OWASP GenAI announcement + resource pages, and flagged as unverified
> because the canonical ranked PDF (<https://genai.owasp.org/download/52117>) is
> download-gated. They have since been **independently corroborated**: all ten codes and
> titles agree across multiple unaffiliated secondary sources published after the
> 2025-12-09 release (Palo Alto Networks, Auth0, Teleport, NeuralTrust, Modulos,
> Practical DevSecOps). The ordering and wording match on every entry.
>
> Remaining honest gap: **the canonical PDF itself has still not been read.** Agreement
> across many independent secondary sources is strong corroboration, not primary
> evidence — two official titles are recorded here in their fuller form ("Tool Misuse &
> Exploitation", "Unexpected Code Execution (RCE)") exactly because the secondary sources
> surfaced that detail. If these codes are ever used in an external or regulatory report,
> read the PDF first. For internal coverage mapping they are safe to rely on.

aisotropy is an **agentic runtime** (agent loop `internal/agentloop`, scope-gated
tools `internal/tools` + wazero WASM sandbox `internal/sandbox`, tiered memory,
multi-tenant by `context.Context`), so the LLM Apps Top 10 alone is insufficient.
This is the **no-blank-cells** map of the agent-specific surfaces. It shares the
same offline scan:

```bash
bash scripts/owasp-llm-scan.sh          # ASI02 + ASI05 run alongside the LLM categories
```

The LLM Apps Top 10 dimension is in [`owasp-llm-coverage.md`](./owasp-llm-coverage.md).

---

## Coverage table

| Agentic risk (2026) | aisotropy enforcing surface (file:line) | Deterministic test (offline) | Cross-ref | Status |
|---|---|---|---|---|
| **ASI01 — Agent Goal Hijack** | Untrusted-context fence + scan-on-hydration: `internal/api/chat_stream.go:108`/`:399`; injection detection `internal/guardrails/injection.go:150` | `internal/api/owasp_llm04_fence_test.go`; `internal/guardrails/owasp_llm01_injection_test.go` | ↔ LLM01 Prompt Injection, LLM04 Poisoning | **covered** (goal-hijack via injected context is fenced as data + detected) |
| **ASI02 — Tool Misuse & Exploitation** | Scope-gated registry `internal/tools/tools.go:43`; agent loop refuses a non-permitted tool `internal/agentloop/agentloop.go:117` | `internal/agentloop/owasp_asi02_tool_misuse_test.go`; `internal/tools/owasp_llm06_agency_test.go` | ↔ LLM06 Excessive Agency (scope-gating tests) | **covered** (a scoped-out tool is unreachable from the loop even if the model names it) |
| **ASI03 — Identity & Privilege Abuse** | `tenant_id`/`user_id` via `context.Context` (constitutional gate); per-tenant/user scope on every memory read (`internal/semantic/memory_store.go:60`, `internal/memory/key.go`); scope→tool gating | `internal/semantic/owasp_llm08_embedding_test.go` (cross-tenant/user); `internal/agentloop/owasp_asi02_tool_misuse_test.go` (privilege escalation via tool) | ↔ LLM08 (isolation), LLM06 (scope) | **covered** (no cross-scope data or capability escalation) |
| **ASI04 — Agentic Supply Chain Vulnerabilities** | License gate `.claude/rules/constitutional-gates.md`; `go.sum` pinning; WASM module bytes are explicit input (`internal/sandbox/wasm.go` `ComputeSpec.Module`) | — (not offline-unit-testable) | ↔ LLM03 Supply Chain | **N/A** — covered-by: license gate + module pinning + CI dependency scan (process/CI) |
| **ASI05 — Unexpected Code Execution (RCE)** | WASM tier is powerless by construction (no fork/exec/net/ambient FS); routing boundary `internal/sandbox/wasm.go:183` (`CanServe`); container hardening `internal/sandbox/hardening.go:25` (`--network=none`) | `internal/sandbox/owasp_asi05_sandbox_test.go` (host/net-needing tool refused on WASM tier; type-level not-a-Provider) | — | **covered** (checked mis-route refusal; deeper egress/DoS bounds in `wasm_test.go` + `internal/wasmtool`) |
| **ASI06 — Memory & Context Poisoning** | Fence over retrieved memory `internal/api/chat_stream.go:399`; per-scope memory isolation `internal/semantic/memory_store.go:60`; consolidation dedup (sleep cycle) | `internal/api/owasp_llm04_fence_test.go`; `internal/semantic/owasp_llm08_embedding_test.go` (cross-tenant recall) | ↔ LLM04 Poisoning, LLM08 Embedding | **covered** (poisoned memory fenced as data; no cross-tenant recall) |
| **ASI07 — Insecure Inter-Agent Communication** | Single in-process runtime; no autonomous agent-to-agent messaging bus today (tools are host-executed, not peer agents) | — | — | **N/A** — no multi-agent message channel exists in the runtime; reserved if A2A is added |
| **ASI08 — Cascading Failures** | Circuit breaker `internal/llm/breaker.go`; per-tenant quota `internal/memory/quota.go:74`; non-fatal memory-tier degradation (`hydrateSystemPrompt` degrades, never cascades) | `internal/memory/owasp_llm10_consumption_test.go` (bounded consumption); `internal/llm/breaker_test.go` (existing); `internal/api/hydration_degradation_test.go` (existing) | ↔ LLM10 Unbounded Consumption | **covered** (blast-radius bounded by quota + breaker + independent tier degradation) |
| **ASI09 — Human-Agent Trust Exploitation** | Output guardrails annotate/refuse `internal/api/guard.go`; leak/toxicity inspection `internal/guardrails/output.go` | `internal/guardrails/owasp_llm07_output_leak_test.go` (partial — deceptive/leaky output detection) | ↔ LLM07 | **partial** — deterministic leak/toxicity detection covered; genuine social-engineering resistance is a real-model property (arena `safeguard_decline`) |
| **ASI10 — Rogue Agents** | Bounded autonomy: `internal/agentloop/agentloop.go` `MaxSteps` cap; scope-gated tools; no self-modifying/self-spawning path | `internal/agentloop/owasp_asi02_tool_misuse_test.go` (loop cannot exceed granted authority) | ↔ LLM06, ASI02 | **partial** — capability containment covered deterministically; misalignment detection is a model-level property (out of unit scope) |

---

## How the two dimensions relate

- **Tool Misuse (ASI02) ↔ LLM06 Excessive Agency** — same scope-gating tests, viewed
  from the registry (LLM06) and the agent loop (ASI02).
- **Identity & Privilege Abuse (ASI03) ↔ LLM08 / multi-tenant isolation** — the
  cross-tenant/cross-user embedding tests are the shared evidence.
- **Memory & Context Poisoning (ASI06) ↔ LLM04 / LLM08** — the hydration fence and the
  cross-scope recall tests cover both.

Genuinely non-unit-testable agentic risks (ASI04 supply chain; ASI07 inter-agent comms
that do not exist in the runtime; the misalignment half of ASI09/ASI10) are declared
**N/A / partial with reason** above — never left blank.

_Last verified: `bash scripts/owasp-llm-scan.sh` → RESULT: CLEAN (exit 0); ASI02 + ASI05
PASS. ASI01/03/06/08 are covered by the cross-referenced LLM-dimension tests._
