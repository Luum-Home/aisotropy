---
type: Compliance
title: "EU AI Act + sector (HIPAA / PCI-DSS) readiness — obligation → control → status"
description: A code-grounded readiness assessment mapping aisotropy's runtime controls against the EU AI Act 2026 obligations and, briefly, HIPAA and PCI-DSS for domain adopters. Clarifies which obligations fall on aisotropy-the-runtime versus the adopter-the-deployer, and gives an honest SUPPORTS / PARTIAL / GAP / OUT-OF-SCOPE matrix. This is a READINESS assessment, not a claim of compliance.
tags: [compliance, eu-ai-act, gdpr, hipaa, pci-dss, readiness, audit, honesty]
timestamp: 2026-07-17T00:00:00Z
---

# EU AI Act + sector readiness — obligation → control → status

> **Scope and disclaimer.** This document assesses **readiness**, not compliance. Compliance is a
> property of a *deployed system operated by a legal entity*, established by that entity against its
> own use case and jurisdiction — it cannot be asserted by an infrastructure component. Nothing here
> is legal advice. Every regulatory claim cites an official or reputable source URL; every aisotropy
> claim is grounded in a file or ADR in this repository. Where aisotropy does something, it is marked
> **SUPPORTS**; where it half-does it, **PARTIAL**; where it does not, **GAP**; where the duty is
> structurally the adopter's, **OUT-OF-SCOPE (adopter)**.

---

## 0. The load-bearing question: what IS aisotropy under the AI Act?

The EU AI Act assigns obligations by **role**, not by software. The three roles that matter here
([Article 3 definitions](https://artificialintelligenceact.eu/article/3/);
[provider vs deployer guide](https://tryaicompliance.com/en/guides/provider-vs-deployer)):

- **Provider** — develops an AI system (or a GPAI model) and places it on the market / puts it into
  service under its own name.
- **Deployer** — uses an AI system under its own authority in the course of its activity.
- **GPAI (general-purpose AI model) provider** — places a general-purpose *model* on the market.

**aisotropy ships no model.** The LLM is brought by the adopter through a provider-agnostic seam
(`internal/llm`, [ADR-0009](../decisions/0009-provider-agnostic-llm-layer.md); the multi-provider
registry selects OpenRouter/OpenAI/Anthropic/stub, [runtime-as-built.md §1](../architecture/runtime-as-built.md)).
Therefore:

| Role | Who holds it | Why |
|---|---|---|
| **GPAI model provider** | The upstream model vendor (OpenAI, Anthropic, …) the adopter points aisotropy at | aisotropy neither trains nor distributes a model. It is not a GPAI provider. Confirmed by the "no model shipped" architecture ([ADR-0009](../decisions/0009-provider-agnostic-llm-layer.md)). |
| **Provider of the AI *system*** | **The adopter** who wraps aisotropy into a product offered under their own name | "If you wrap the API into a product or service you offer to customers under your own name, you are typically the provider of that AI system" ([GPAI roles guide](https://www.deepinspect.ai/blog/eu-ai-act-providers-vs-deployers-obligations)). |
| **Deployer** | **The adopter** (often the same entity) operating the system for end-users | Deployer duties (Article 26, Article 50 §3–4) attach to whoever runs the system for exposed persons. |
| **AI-system component / tooling supplier** | **aisotropy-the-runtime** | aisotropy is infrastructure a provider builds *on*. It does not place an AI system on the market; it supplies controls the provider/deployer uses to meet *their* obligations. |

**Consequence for this whole document:** almost every substantive AI Act *obligation* legally lands
on the **adopter** (as provider and/or deployer). aisotropy's job is to make those obligations
**cheap to discharge** — to *help* rather than *hinder*. So the useful question is not "is aisotropy
compliant" (it cannot be — it is not the regulated actor) but **"what does an adopter get for free,
and what must they still build themselves?"** That is what the matrix below answers.

### When aisotropy's runtime could pull the adopter into the *high-risk* regime

The heavy AI Act machinery (Articles 9–15: risk management, data governance, record-keeping, human
oversight, accuracy/robustness) only binds systems classified **high-risk** under
[Article 6](https://artificialintelligenceact.eu/article/6/) +
[Annex III](https://artificialintelligenceact.eu/annex/3/) (e.g. biometrics, critical
infrastructure, employment, essential services, law enforcement). A general chatbot built on
aisotropy is typically **not** high-risk — it carries only the **Article 50 transparency** duty. An
adopter who deploys aisotropy into an Annex III use case (e.g. a hiring assistant) inherits the full
high-risk stack. This document maps the high-risk requirements too, because aisotropy's controls
either help or hinder them **if** the adopter lands there.

---

## 1. AI Act application timeline (what applies, when)

Source: [EU AI Act implementation timeline](https://artificialintelligenceact.eu/implementation-timeline/);
[AI Office GPAI guidance](https://digital-strategy.ec.europa.eu/en/policies/guidelines-gpai-providers).

| Date | What applies |
|---|---|
| **2 Feb 2025** | [Article 5](https://artificialintelligenceact.eu/high-level-summary/) prohibited practices + [Article 4](https://artificialintelligenceact.eu/article/4/) AI-literacy duty — **already in force**, all risk levels. |
| **2 Aug 2025** | GPAI **model provider** obligations enter application (binds the upstream model vendor, not aisotropy). |
| **2 Aug 2026** | [Article 50 transparency](https://artificialintelligenceact.eu/transparency-rules-article-50/) obligations apply; Commission may enforce GPAI provider obligations (incl. fines). **This is the near-term date that touches an aisotropy-based chatbot.** |
| **2 Dec 2026** | Grace-period end for the machine-readable **marking** of generative-AI output already on the market before 2 Aug 2026 (AI Omnibus provisional agreement, May 2026 — [DataGuard timeline](https://www.dataguard.com/eu-ai-act/timeline)). |
| **2 Dec 2027** | High-risk (Annex III) obligations — moved from 2 Aug 2026 by the May 2026 Digital Omnibus ([DeepInspect](https://www.deepinspect.ai/blog/eu-ai-act-providers-vs-deployers-obligations)). Article 50 transparency **kept its 2 Aug 2026 date**. |

**Timeline note:** the exact post-Omnibus dates were still settling as of mid-2026; treat the
high-risk dates as directional and re-verify against the official timeline before relying on them.

---

## 2. The obligation → aisotropy-control → status matrix

Legend: **SUPPORTS** = a real, code-grounded control materially helps the adopter meet the duty ·
**PARTIAL** = a control exists but has a named residual/gap · **GAP** = no control, adopter must
build it · **OUT-OF-SCOPE (adopter)** = the duty is structurally the adopter's and no runtime
control is expected.

### 2a. Article 50 — Transparency (applies 2 Aug 2026; the near-term one)

| # | Obligation (who) | aisotropy control | Status |
|---|---|---|---|
| 50.1 | **Provider:** users must be told they are interacting with an AI ([Art. 50 §1](https://artificialintelligenceact.eu/article/50/)) | No runtime control. The disclosure lives in the adopter's UI/first-interaction surface; aisotropy exposes only `POST /v1/.../chat*` JSON/SSE ([runtime-as-built.md §1](../architecture/runtime-as-built.md)) — it does not render an end-user UI. | **OUT-OF-SCOPE (adopter)** |
| 50.2 | **Provider:** synthetic audio/image/video/**text** output marked "machine-readable … detectable as artificially generated" ([Art. 50 §2](https://artificialintelligenceact.eu/article/50/)) | No output watermarking / C2PA / provenance-marking in the response path. `ChatOutput`/SSE `token` events carry raw model text; the only output-side processing is the opt-in toxicity/leak guardrail ([ai-layer-guardrails.md §5](../security/ai-layer-guardrails.md)). Text watermarking is not implemented. | **GAP** |
| 50.3 | **Deployer:** inform persons exposed to emotion/biometric recognition | aisotropy runs no emotion/biometric recognition. N/A unless the adopter adds it. | **OUT-OF-SCOPE (adopter)** |
| 50.4 | **Deployer:** label deepfakes / AI-generated public-interest content | No media generation in the runtime. | **OUT-OF-SCOPE (adopter)** |

**Verdict for §50:** the single **actionable near-term gap** aisotropy could help close is **50.2
(machine-readable text marking)** — a provider-side output-marking seam (analogous to the existing
opt-in output-guardrail seam) would let an adopter mark generated text without hand-rolling it. The
50.1 "you're talking to AI" disclosure is genuinely the adopter's UI responsibility.

### 2b. Article 12 — Record-keeping & traceability (high-risk; the runtime's strongest suit)

Article 12 requires high-risk systems to **automatically log events over their lifetime** for
traceability and post-market monitoring, and the logs must be **tamper-resistant** and retained
([Art. 12](https://artificialintelligenceact.eu/article/12/);
[Articles 12–13 explainer](https://aigovernancedesk.com/eu-ai-act-articles-12-13-decision-traceability/)).

| Sub-duty | aisotropy control | Status |
|---|---|---|
| Automatic, durable event logging of who accessed what, when | `internal/audit` — an **always-on** access-audit sink (slog by default, durable JSONL via `AISOTROPY_AUDIT_LOG`), one `Event` per served `/v1` request with identity + request metadata, fail-closed if a requested durable log can't be written ([ADR-0047](../decisions/0047-durable-access-audit-log.md)). | **SUPPORTS** |
| Logs **tamper-resistant** | Opt-in `HashChainedSink` (`AISOTROPY_AUDIT_HASHCHAIN`): each record commits to the prior record's SHA-256 digest, with a `VerifyChain` verifier + `audit-verify` CLI, so edits/removals/reorders of past records are **detectable** ([ADR-0056](../decisions/0056-tamper-evident-audit-hash-chain.md)). | **PARTIAL** |
| Retention of logs | Sink is retained (durable file), but retention **period/rotation/WORM** is the operator's responsibility — the runtime does not enforce a retention policy ([ADR-0047](../decisions/0047-durable-access-audit-log.md), "Retention … are operator responsibilities"). | **PARTIAL** |
| Event **content** captures the decision trail | The audit Event is **identity + request metadata only** — deliberately **no prompt, model output, or model/version** per the privacy contract ([ADR-0047](../decisions/0047-durable-access-audit-log.md)). Good for access traceability; it is **not** a decision/inference log (inputs→outputs→model version) that Article 12 traceability for high-risk contemplates. | **GAP** (for high-risk decision traceability) |

**Honest residual on tamper-evidence:** the hash chain is tamper-**evident, not tamper-proof** — an
attacker with write access can recompute the chain forward or truncate the tail; closing that needs a
WORM substrate or external head-anchoring, named as the further follow-on
([ADR-0056 "Honest boundary"](../decisions/0056-tamper-evident-audit-hash-chain.md)). Do not
represent the audit log as tamper-proof to an auditor.

### 2c. Article 10 — Data & data governance (high-risk)

Article 10 governs **training/validation/testing datasets** — provenance, quality, representativeness
([Art. 10 summary](https://www.dataiku.com/stories/blog/eu-ai-act-high-risk-requirements)).

| Sub-duty | aisotropy control | Status |
|---|---|---|
| Training-data governance (relevance, representativeness, bias, provenance) | aisotropy **does not train the model** (BYO model, [ADR-0009](../decisions/0009-provider-agnostic-llm-layer.md)). Training-data governance is the GPAI provider's + adopter's duty. | **OUT-OF-SCOPE (adopter)** |
| Governance of **operational/retrieval** data (tenant isolation, no cross-tenant leakage) | Isolation-by-construction: every store operation is scoped by `(tenant, user[, session])`; ~146 `NewScope`/`NewTenantScope` call sites enforced by the `scopecheck` analyzer, per-tier `ScopeEraser`, tenant-tier physical routing ([ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md), [ADR-0007](../decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md), [runtime-as-built.md §4](../architecture/runtime-as-built.md)). Qdrant filtering is payload-filtered during the HNSW scan, not post-filtered ([constitutional-gates](../../.claude/rules/constitutional-gates.md)). | **SUPPORTS** |
| Data-subject rights over stored data (feeds GDPR, adjacent to data governance) | `App.EraseSubject` fans out GDPR Art.17 erasure across every memory tier (facts, semantic all-kinds, working memory) with an `EraseReport` audit record; scoped-never-wildcard, idempotent ([ADR-0046](../decisions/0046-subject-level-erasure-gdpr-art17.md)). | **SUPPORTS** |
| At-rest confidentiality of operational data | Working-memory tier: opt-in BadgerDB AES (`AISOTROPY_DATA_ENCRYPTION_KEY`, [ADR-0044](../decisions/0044-working-memory-at-rest-encryption.md)). **Facts SQLite tier: no app-layer encryption** — the pure-Go driver has no SQLCipher; operator disk/volume encryption is the accepted compensating control ([ADR-0045](../decisions/0045-facts-tier-at-rest-encryption-posture.md)). | **PARTIAL** |

### 2d. Article 14 — Human oversight (high-risk)

Article 14: the system must be **designed to enable** human oversight; the deployer assigns oversight
to trained individuals ([Art. 14 summary](https://www.dataiku.com/stories/blog/eu-ai-act-high-risk-requirements)).

| Sub-duty | aisotropy control | Status |
|---|---|---|
| System designed so a human can intervene/stop | Immediate context-cancellation: a cancelled client request stops the upstream LLM call token-by-token, partial output not persisted ([runtime-as-built.md §2 drain loop](../architecture/runtime-as-built.md); [constitutional-gates](../../.claude/rules/constitutional-gates.md) "streaming must respect ctx cancellation immediately"). Scope-gated tool dispatch: a hallucinated/injected tool name can never execute; only scope-permitted tools enter the prompt ([runtime-as-built.md §5](../architecture/runtime-as-built.md)). Opt-in LLM-as-judge output check ([ai-layer-guardrails.md §4](../security/ai-layer-guardrails.md)). | **SUPPORTS** (mechanisms) |
| Oversight **process** — assigned, trained humans; review workflows; override UI | Organizational, not a runtime property; the adopter builds the review UI and assigns oversight. | **OUT-OF-SCOPE (adopter)** |

### 2e. Article 15 / safety adjacent — robustness, accuracy, cybersecurity (high-risk)

| Sub-duty | aisotropy control | Status |
|---|---|---|
| Resilience / graceful degradation | Circuit breaker (5 fails → fast-fail 30s), per-tenant token-bucket quota checked **before** any LLM spend, quota-pressure model degradation ([runtime-as-built.md §5](../architecture/runtime-as-built.md); [constitutional-gates](../../.claude/rules/constitutional-gates.md) quota gate). | **SUPPORTS** |
| Prompt-injection / adversarial-input resistance | Untrusted-context fence around all retrieved data, scan-on-ingest for stored/indirect injection, injection blocking ON by default, opt-in multilingual NLI guardrail model ([ai-layer-guardrails.md](../security/ai-layer-guardrails.md), [ADR-0052](../decisions/0052-multilingual-guardrail-model.md)). | **PARTIAL** (fence + heuristics are hardening, not a guarantee; the doc says so explicitly) |
| Structured-output correctness | Fail-closed reflection loop on schema violation (non-streaming route, 502 after bounded retries) ([runtime-as-built.md §2](../architecture/runtime-as-built.md); [constitutional-gates](../../.claude/rules/constitutional-gates.md)). | **SUPPORTS** |
| Transport security for credentials | Optional plaintext rejection (`AISOTROPY_REQUIRE_TLS`), default-closed ops endpoints behind `AISOTROPY_OPS_TOKEN`, HS256/RS256/JWKS auth with algorithm-confusion defence ([ADR-0025](../decisions/0025-security-hardening-authn-ops.md), [ADR-0024](../decisions/0024-authenticated-identity-seam.md)). | **SUPPORTS** |
| Accuracy metrics / benchmarking of the deployed system | Not the runtime's to assert; the multilingual guardrail accuracy itself is explicitly "PoC — not yet benchmarked" ([ADR-0052](../decisions/0052-multilingual-guardrail-model.md)). | **OUT-OF-SCOPE (adopter)** |

### 2f. Cross-cutting: identity/authentication as the trust root

Every control above assumes the `(tenant, user)` it isolates is **authenticated**, not self-asserted.
The authenticated-identity seam (must-agree bind of verified principal to path, default-deny unless
`NoAuth()` is explicit, verified scopes gate tools) makes isolation by-construction **end-to-end**
when a real Authenticator is configured ([ADR-0024](../decisions/0024-authenticated-identity-seam.md)).
**Residual:** the shipped binary boots in `NoAuth` mode if `AISOTROPY_JWT_SECRET` (or an RS256/JWKS
var) is unset — a loud-warned but real "trusted path identity, no credential check" posture
([services-config](../../.claude/rules/services-config.md)). An adopter MUST configure auth for any
non-local deployment or every downstream isolation/traceability claim rests on caller-asserted
identity. **Status: SUPPORTS (mechanism) / adopter-must-configure.**

### 2g. Transparency of the runtime's own honesty (a soft asset, not a legal control)

aisotropy maintains code-grounded "honest reality" ledgers — [runtime-as-built.md](../architecture/runtime-as-built.md),
[reality-ledger.md](../architecture/reality-ledger.md), and the code-truth / prune-evidence gate
([.claude/rules/prune-evidence-gate.md](../../.claude/rules/prune-evidence-gate.md)) — that
distinguish REAL vs dormant vs aspirational capabilities and forbid asserting a security property the
code does not deliver (e.g. the refusal to fake a SQLite `PRAGMA key`, [ADR-0045](../decisions/0045-facts-tier-at-rest-encryption-posture.md)).
This is **not** an AI Act transparency obligation (those are about disclosure to *end-users*), but it
is exactly the posture an **auditor** wants: claims that are evidence-backed and residuals that are
named rather than hidden. It makes an audit *cheaper*, it does not *satisfy* any article. **Status:
audit-hygiene asset, not a mapped obligation.**

---

## 3. Ranked gaps (what an adopter must add)

Ranked by proximity of the deadline × effort the runtime could plausibly absorb.

1. **[HIGH — deadline 2 Aug 2026] Article 50.2 machine-readable output marking.** No text
   watermarking/provenance in the response path (§2a). For any generative deployment on the EU
   market this is a near-term, provider-side duty. *Runtime could help:* an output-marking seam
   mirroring the existing opt-in output-guardrail seam. Today: **adopter builds it entirely.**
2. **[HIGH] Article 50.1 "you are talking to AI" disclosure.** Structurally the adopter's UI duty,
   but it is a hard requirement and the runtime offers nothing toward it (correctly — it renders no
   UI). Flagged so no adopter assumes the runtime handles it.
3. **[MEDIUM] Decision/inference traceability (Article 12 for high-risk).** The access-audit log
   records *who accessed what, when* — not *input → output → model version* per inference. A
   high-risk adopter needs an inference-level log the runtime does not produce; the access log is
   necessary but not sufficient (§2b).
4. **[MEDIUM] Tamper-**proof** log retention.** Hash-chain is tamper-*evident* only; regimes wanting
   tamper-*proofing* need the WORM/external-anchor follow-on ([ADR-0056](../decisions/0056-tamper-evident-audit-hash-chain.md)),
   plus an operator-enforced retention/rotation policy the runtime does not provide.
5. **[MEDIUM] Facts-tier at-rest encryption.** Working memory has opt-in AES; the facts SQLite tier
   relies on operator disk encryption ([ADR-0045](../decisions/0045-facts-tier-at-rest-encryption-posture.md)).
   For HIPAA/PCI (below) an adopter must ensure full-disk/volume encryption or a CGO SQLCipher build.
6. **[MEDIUM] Auth is opt-in at the binary.** `NoAuth` boot is possible; the adopter must configure
   HS256/RS256/JWKS for every downstream isolation/traceability guarantee to hold (§2f).
7. **[LOW] Article 4 AI-literacy & Article 5 prohibited-practice screening.** Organizational duties
   already in force since Feb 2025 ([Art. 4](https://artificialintelligenceact.eu/article/4/)); no
   runtime control expected, listed for completeness so the adopter does not overlook them.

---

## 4. Sector notes (brief, proportionate)

These bind an adopter **only if** they process the relevant data class. aisotropy holds no PHI or
cardholder data by design — it holds whatever the adopter routes through it.

### 4a. HIPAA (only if the adopter handles PHI)

The 2025-proposed Security Rule update removes the "addressable vs required" distinction, making
**encryption of ePHI at rest and in transit, MFA, and audit logging mandatory**; OCR's enforcement
posture means organizations should treat these as current
([HIPAA 2025 changes](https://www.kiteworks.com/hipaa-compliance/hipaa-compliance-requirements/)). A
**BAA** is mandatory between a covered entity and any business associate touching PHI, and strong
BAAs require encryption at rest/in transit, access controls, and audit logging
([HIPAA BAA guide](https://www.hipaajournal.com/hipaa-business-associate-agreement/)).

| HIPAA need | aisotropy posture | Status |
|---|---|---|
| Audit logging of access to ePHI | Access-audit log + hash-chain ([ADR-0047](../decisions/0047-durable-access-audit-log.md)/[ADR-0056](../decisions/0056-tamper-evident-audit-hash-chain.md)) | **SUPPORTS** |
| Access controls / authN | Authenticated-identity seam + isolation-by-construction ([ADR-0024](../decisions/0024-authenticated-identity-seam.md)/[ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md)) | **SUPPORTS** |
| Encryption **at rest** of ePHI | Working memory: opt-in AES. **Facts SQLite: app-layer GAP**, disk-encryption compensating control only ([ADR-0045](../decisions/0045-facts-tier-at-rest-encryption-posture.md)) | **PARTIAL — known gap** |
| Encryption **in transit** | Optional TLS enforcement; typically terminated at the adopter's proxy ([ADR-0025](../decisions/0025-security-hardening-authn-ops.md)) | **PARTIAL (adopter-configured)** |
| BAA with the **model vendor** | Out of aisotropy's control — the adopter needs a BAA with whatever LLM API they point the BYO seam at, and must confirm that vendor will sign one and not train on PHI | **OUT-OF-SCOPE (adopter)** |

Bottom line: aisotropy gives an adopter the audit + access-control + isolation half of a HIPAA
technical-safeguards story; the adopter must close the **facts-tier at-rest encryption gap** (disk
encryption or SQLCipher build), enforce TLS, and — critically — get a **BAA with the model vendor**
and confirm no-training-on-PHI. Do not route PHI to a keyless stub or a non-BAA model endpoint.

### 4b. PCI-DSS 4.0.1 (only if the adopter handles cardholder data)

Under PCI-DSS 4.0.1 (new requirements mandatory since 31 Mar 2025), cardholder data must be
**rendered unreadable before being written to disk**, MFA is mandatory for all access to cardholder
data, and protection extends to data in transit and in use
([PCI-DSS 4.0.1 2026 guide](https://www.upguard.com/blog/pci-compliance);
[PCI encryption requirements](https://www.thoropass.com/blog/pci-dss-encryption-requirements)).

**Strong recommendation: keep cardholder data out of aisotropy entirely.** The runtime is a
conversational memory system; PANs have no reason to enter prompts or memory tiers. If they might,
the input-guardrail PII/secret redaction masks some patterns but is explicitly **secrets + basic
identifiers only** and fail-open — **it does not reliably catch PANs** and must not be relied on as a
PCI control ([ai-layer-guardrails.md §6](../security/ai-layer-guardrails.md)). What aisotropy offers
that *is* PCI-relevant: MFA-capable auth (JWT seam), access-audit logging, and tenant isolation. What
it lacks for storing cardholder data: the "render-unreadable-before-disk" guarantee on the facts tier
(same [ADR-0045](../decisions/0045-facts-tier-at-rest-encryption-posture.md) gap). Treat any
cardholder data reaching a memory tier as a **scope-expanding event** the adopter should design out.

---

## 5. Honest verdict — what a deployer gets for free vs must add

**An adopter building on aisotropy gets, for free and code-grounded:** multi-tenant **isolation by
construction** (the differentiator — ~146 enforced scope sites, tenant-physical routing, GDPR Art.17
subject erasure across all tiers), an **always-on access-audit trail** with opt-in cryptographic
**tamper-evidence**, an **authenticated-identity seam** that makes isolation end-to-end, **pre-spend
quota + circuit-breaker** resilience, **context-cancellation** as a human-oversight stop mechanism,
**prompt-injection hardening** (fence + scan-on-ingest + optional multilingual model), and an
unusually **honest self-documentation** posture that makes an audit cheaper because residuals are
named, not hidden. Mapped to the AI Act, that is a strong **Article 12 (record-keeping)**, **Article
10 (operational data governance)**, and **Article 15 (robustness/security)** starting position for a
would-be high-risk deployer — and the isolation/audit/auth trio is a genuine head-start on **HIPAA
technical safeguards** too. **What the adopter must still add themselves:** the entire **Article 50
end-user transparency** surface (the "you're talking to AI" disclosure *and* the machine-readable
output marking — the near-term 2 Aug 2026 duties the runtime does nothing toward today), **inference-
level decision traceability** for a high-risk use case (the audit log records access, not
input→output→model-version), **tamper-proof/WORM retention** if evidence is not enough, **facts-tier
at-rest encryption** (a known, documented gap), a **configured authenticator** (the binary can boot
open), the **organizational** duties entirely (AI literacy, human-oversight process, risk-management
system, conformity assessment), and — for HIPAA/PCI — a **BAA with the model vendor** and a decision
to keep PHI/PAN out of the memory tiers. Net: aisotropy is a credible **infrastructure substrate**
that discharges the *technical/traceability/isolation* fraction of an adopter's obligations honestly;
it is **not, and does not claim to be, a compliant AI system** — the regulated roles (provider,
deployer) and their heaviest duties remain the adopter's.

---

## Sources

Regulatory:
- [EU AI Act — Article 3 (definitions)](https://artificialintelligenceact.eu/article/3/)
- [EU AI Act — Article 4 (AI literacy)](https://artificialintelligenceact.eu/article/4/)
- [EU AI Act — Article 6 (high-risk classification)](https://artificialintelligenceact.eu/article/6/) · [Annex III](https://artificialintelligenceact.eu/annex/3/)
- [EU AI Act — Article 10 / high-risk requirements (Dataiku)](https://www.dataiku.com/stories/blog/eu-ai-act-high-risk-requirements)
- [EU AI Act — Article 12 (record-keeping)](https://artificialintelligenceact.eu/article/12/) · [Articles 12–13 traceability explainer](https://aigovernancedesk.com/eu-ai-act-articles-12-13-decision-traceability/)
- [EU AI Act — Article 50 (transparency)](https://artificialintelligenceact.eu/article/50/) · [Practical guide to Article 50](https://artificialintelligenceact.eu/transparency-rules-article-50/)
- [EU AI Act — implementation timeline](https://artificialintelligenceact.eu/implementation-timeline/) · [DataGuard timeline](https://www.dataguard.com/eu-ai-act/timeline)
- [AI Office — GPAI provider guidance](https://digital-strategy.ec.europa.eu/en/policies/guidelines-gpai-providers)
- [Provider vs deployer roles (DeepInspect)](https://www.deepinspect.ai/blog/eu-ai-act-providers-vs-deployers-obligations) · [tryaicompliance](https://tryaicompliance.com/en/guides/provider-vs-deployer)
- [HIPAA 2025 Security Rule changes (Kiteworks)](https://www.kiteworks.com/hipaa-compliance/hipaa-compliance-requirements/) · [HIPAA BAA guide (HIPAA Journal)](https://www.hipaajournal.com/hipaa-business-associate-agreement/)
- [PCI-DSS 4.0.1 2026 guide (UpGuard)](https://www.upguard.com/blog/pci-compliance) · [PCI encryption requirements (Thoropass)](https://www.thoropass.com/blog/pci-dss-encryption-requirements)

aisotropy (repo-grounded):
- ADRs: [0007](../decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md), [0009](../decisions/0009-provider-agnostic-llm-layer.md), [0011](../decisions/0011-tenant-scoped-statement-builder.md), [0024](../decisions/0024-authenticated-identity-seam.md), [0025](../decisions/0025-security-hardening-authn-ops.md), [0044](../decisions/0044-working-memory-at-rest-encryption.md), [0045](../decisions/0045-facts-tier-at-rest-encryption-posture.md), [0046](../decisions/0046-subject-level-erasure-gdpr-art17.md), [0047](../decisions/0047-durable-access-audit-log.md), [0052](../decisions/0052-multilingual-guardrail-model.md), [0056](../decisions/0056-tamper-evident-audit-hash-chain.md)
- Architecture: [runtime-as-built.md](../architecture/runtime-as-built.md), [reality-ledger.md](../architecture/reality-ledger.md), [multi-tenancy.md](../architecture/multi-tenancy.md)
- Security: [ai-layer-guardrails.md](../security/ai-layer-guardrails.md)
- Rules: [constitutional-gates](../../.claude/rules/constitutional-gates.md), [services-config](../../.claude/rules/services-config.md), [prune-evidence-gate](../../.claude/rules/prune-evidence-gate.md)

---

TRUST_REPORT: SCORE=78 STATUS=MEDIUM EVIDENCE=4 UNCERTAINTIES=5
---
Score: 78/100

WHAT I VERIFIED:
  - Read the full text of ADR-0044/0045/0046/0047/0052/0056 and ADR-0024, plus runtime-as-built.md (§1–6) and ai-layer-guardrails.md directly from the repo — every aisotropy control claim in the matrix cites a file/ADR I actually read.
  - Confirmed the compliance dir did not exist before (`ls`) and created only `docs/compliance/`; no code touched, nothing committed (task constraints honored).
  - Cross-checked EU AI Act dates and Article 50/12/10/14/4/5 obligations against official/reputable sources (artificialintelligenceact.eu article pages, EC digital-strategy, AI Office guidance) via WebSearch + one WebFetch of the Article 50 page.
  - Grounded the "no model shipped / BYO LLM" role determination in ADR-0009 + the runtime-as-built provider registry.

UNSURE ABOUT:
  - Post-Digital-Omnibus (May 2026) dates are still settling; I flagged the high-risk 2 Dec 2027 date as directional. The Article 50 grace period (2 Dec 2026) and enforcement dates come from secondary summaries, not the consolidated legal text — an adopter should re-verify against the Official Journal.
  - The provider/deployer role allocation is my reasoned reading of the Act's role taxonomy applied to "infrastructure a deployer builds on"; the Act has no explicit "runtime component supplier" role, and Article 25 (responsibilities along the value chain) could pull a component supplier in under some framings — I did not deep-dive Article 25.
  - I did not read the full runtime-as-built.md (lines 488–600 were truncated) or the reality-ledger.md / multi-tenancy.md end-to-end; the "honesty ledger" and isolation claims rest on the sections I did read plus ADR text.
  - Whether the input-guardrail redaction catches any PAN patterns at all — I asserted it does NOT reliably, based on the ai-layer-guardrails "secrets + basic identifiers, fail-open" scope; I did not read redact.go itself.
  - "~146 NewScope call sites" is quoted from ADR-0024/0011 prose, not independently counted via grep.

HUMAN SHOULD CHECK:
  - Have counsel confirm the provider/deployer/component-supplier role allocation (§0) and the Article 25 value-chain question before any external use of this framing.
  - Re-verify the 2026–2027 AI Act dates against the consolidated text post-Omnibus.
  - If PHI/PAN is in play, the two load-bearing adopter actions are the model-vendor BAA and the facts-tier encryption gap — validate both against the live deployment config.
