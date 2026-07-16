---
type: ADR
title: "ADR-0033 — Open-core extension seams, tiered by isolation-criticality: open the policy seams, seal the isolation"
description: Opening a runtime's internals for "bring your own X" is where multi-tenant isolation usually leaks — a competitor lets you plug a memory backend that can read across tenants. This ADR sets the RULE that decides which internal seam is safe to expose raw and which is not, and it tiers them. TIER-1 (SAFE) are pure POLICY seams — decision/transform logic with no authority over WHICH tenant's data is touched: six type aliases (GuardrailCheck, Reranker, Summarizer, EvalJudge, ModelPolicy, TenantRoutingPolicy) plus the functional options that install them (WithGuardrailCheck / WithSummarizer / WithEvalJudge / WithModelPolicy / WithTenantRoutingPolicy / WithReranker) and the plugin loaders. Because none of them enforce tenant isolation (it is enforced downstream in the ctx-scoped stores, ADR-0011), a community implementation cannot leak cross-tenant no matter how wrong it is. HONEST per-seam status as the code shows it: all SIX are WIRED end-to-end (a supplied impl is actually invoked by the running pipeline, each proven by a spy test) — the Reranker, initially exported-stored-only, is now APPLIED on the semantic hydration path (streaming /chat/stream, /ask, and the bitrate hydrator) over an isolation-clean over-fetched candidate pool. TIER-2 (ISOLATION-CRITICAL) are the memory backends (facts.Store / semantic.Store / working memory, the tool Registry, the memory Scope and its constructors): DELIBERATELY NOT opened as RAW interfaces, because a raw store interface would let an embedder bypass the "every Scope is built by the runtime from ctx" invariant that makes isolation hold by construction. They are instead opened as the SEALED-DRIVER design: tenant-UNAWARE drivers (semantic.VectorBackend / facts.DocBackend) + runtime-owned SEALED scoping decorators that no plugin can bypass (namespace derived from the ctx-scoped Scope) + the internal/plugintest conformance + isolation-proof suite, installed via WithVectorBackend / WithFactsBackend — "bring your own backend, not your own isolation". The differentiator, stated plainly: competitors let you plug a backend that can leak; Aisotropy seals the isolation so a wrong backend still cannot.
tags: [decisions, adr, open-core, extension-seams, isolation, tiering, bring-your-own, sealed-driver, policy-seams, phase-9]
timestamp: 2026-07-16T00:00:00Z
---

# ADR-0033 — Open-core extension seams, tiered by isolation-criticality: open the policy seams, seal the isolation

## Status

✅ **Accepted — 2026-07-16.** Establishes the **rule** that decides which internal seam of the sealed
runtime is safe to expose as a freely-implementable "bring your own X" interface and which is not, and
**tiers** the seams by that rule. **Tier-1** (pure policy seams — SAFE) is opened as six
type aliases plus the functional options that install them, and the plugin loaders
([ADR-0032](0032-plugin-ecosystem.md)); all six are wired end-to-end (the Reranker, initially
exported-stored-only, is now applied on the semantic hydration path). **Tier-2** (the
isolation-critical memory backends) is **deliberately NOT** opened as raw interfaces; it is opened as
the **sealed-driver** design specified here — tenant-unaware drivers
(`semantic.VectorBackend` / `facts.DocBackend`) behind runtime-owned sealed scoping decorators plus the
`internal/plugintest` conformance suite, installed via `WithVectorBackend` / `WithFactsBackend`. This ADR **extends**
[ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md)'s sealed-kernel / composition-surface
split (it operationalizes *which side of the seal* each seam is on) and
[ADR-0013](0013-public-extension-surface-facade-vs-promote.md)'s façade (the aliases live in the same
façade contract), and is the sibling of [ADR-0032](0032-plugin-ecosystem.md) (the plugin ecosystem
whose tool contributions ride the Tier-1 surface).

Everything Tier-1 is **additive and default-safe**: every option is inert when unset, so `New()` with
none of them boots a byte-for-byte-identical runtime to the pre-extension composition root
([`aisotropy/extend_test.go`](../../aisotropy/extend_test.go)'s `TestNoOptionsRuntimeUnchanged`).

## Context

ADR-0016 committed to being **both** a sealed multi-tenant runtime and a composable framework, held
together by one load-bearing invariant: *a composed node receives an already-scoped `ctx` and never
constructs a `Scope`* (ADR-0011's `internal/tenancy.FromContext`). ADR-0013 chose a **façade** of
type aliases over promoting `internal/` wholesale, precisely because promotion "regresses isolation by
making `facts.Scope` / `ExportAll` public." Both ADRs drew the line abstractly. Neither answered the
operational question an extender actually hits: **for THIS internal interface, is exposing it as a
'bring your own' seam safe, or does it hand an embedder the ability to break tenant isolation?**

The stakes are asymmetric. Multi-tenant isolation is the product's spine (ADR-0007, ADR-0011), and
opening internals is exactly where competitors leak it: a framework that lets you "plug your own vector
store" typically hands you a `Store` interface whose methods take a raw query and return raw rows — the
plugin decides what to read, so a buggy or hostile plugin can read across tenants, and the isolation
guarantee silently becomes "as good as every backend anyone ever plugged in." That is the failure mode
this ADR exists to make structurally impossible.

The discriminating property is **who has authority over which tenant's data is touched**:

- A **policy seam** decides or transforms — *how to judge an output, how to reorder candidates, how to
  condense a summary, how to score a sample, which model to route to, which tier a tenant gets*. It has
  **no authority** over which tenant's data is read or written; it operates on data the runtime already
  scoped and handed it. A wrong policy produces a wrong *decision*, never a cross-tenant *read*.
- An **isolation-critical seam** is a backend the runtime reads/writes tenant data through — the facts
  store, the semantic store, working memory, the tool registry, and above all the `Scope` and its
  constructors. Exposed raw, it lets the plugin decide *what to read*, which is the authority that must
  never leave the runtime.

The open question: **codify that distinction into a rule, tier the concrete seams by it, ship the safe
tier honestly (wired-vs-exported stated per seam), and design the isolation-critical tier so it can be
opened LATER without ever handing out the read-authority.**

## Goals

- A **rule** an extender (and a reviewer) can apply to any internal interface: *is this a policy seam
  (no data-touch authority → safe to open raw) or an isolation-critical seam (owns which tenant's data
  is touched → never open raw)?*
- **Ship Tier-1** — the six policy aliases + their install options + the plugin loaders — as the
  genuine internal types the runtime consumes (façade contract, ADR-0013), so a consumer's value is the
  real seam, not a shadow copy.
- **Prove Tier-1 is invocation-real.** Each seam is proven **invoked by the running pipeline** by a spy
  implementation. All six are now wired — the Reranker (initially exported-stored-only) is applied on
  the semantic hydration path (streaming `/chat/stream`, `/ask`, bitrate hydrator) and proven invoked
  over the real `/ask` route (`TestWithRerankerInvoked`) and at the hydration seam
  (`TestHydrateSystemPrompt_AppliesReranker`).
- **Guarantee Tier-1 cannot leak isolation.** None of the six enforce tenant isolation — it is enforced
  downstream in the ctx-scoped stores — so a community implementation, however wrong, cannot produce a
  cross-tenant read.
- **Open Tier-2 as a sealed-driver design:** tenant-**unaware** backend drivers
  (`semantic.VectorBackend` / `facts.DocBackend`) + runtime-owned **sealed** scoping decorators no
  plugin can bypass + the `internal/plugintest` conformance suite, installed via `WithVectorBackend` /
  `WithFactsBackend`. "Bring your own backend, not your own isolation."

## Non-goals

- **NOT** opening the memory backends (facts / semantic / working-memory stores), the tool `Registry`,
  or the memory `Scope` / its constructors as **raw** interfaces — ever. They are the sealed kernel
  (ADR-0016 L0). The facts/semantic backends are opened only via the Tier-2 **sealed-driver** design
  (tenant-unaware driver + runtime-owned scoping decorator); the working-memory store, tool `Registry`,
  and `Scope` constructors remain runtime-owned with no consumer seam.
- The Reranker is **not** a silent exception: it was shipped exported-stored-only first and its
  non-wiring was asserted by test; it is **now wired** (applied on the semantic hydration path) and the
  test flipped to assert invocation. All six Tier-1 seams are wired end-to-end.
- **NOT** claiming the six are fully third-party-implementable from an EXTERNAL module today. Several
  method signatures reference internal-only payload types (`OutputCheckInput`/`Verdict`, `ScoredPoint`,
  `Message`, `Criterion`/`EvalSample`/`Score`, `Request`/`Decision`, `TenantTier`) that are **not
  re-exported yet** — a pending follow-up. Inside this module (and this repo's tests) all six are
  implementable; from an external module, the payload aliases are still owed.
- The Tier-2 drivers (`semantic.VectorBackend` / `facts.DocBackend`), their sealed scoping decorators
  (`NewVectorBackendStore` / `NewDocBackendStore`), and the `internal/plugintest` conformance +
  isolation-proof suite are **built and wired** (installed via `WithVectorBackend` / `WithFactsBackend`,
  threaded through `startRuntime` + the shared `ensureSemantic` build path). The facts driver shipped as
  `DocBackend` (the KV/document journal), not the placeholder name `KVBackend` used while specifying it.
- **NOT** changing the HTTP contract. Tier-1 is aliases + options over already-vendored internals; the
  Tier-2 drivers add no new dependency.

## Options considered

### How to decide which seam is safe to open

- **Open whatever an extender asks for (maximize pluggability).** Rejected — this is exactly how a
  competitor's "plug your own store" leaks isolation: the moment a raw backend interface is public, the
  isolation guarantee is only as strong as the weakest plugin anyone runs.
- **Open nothing; keep the runtime fully sealed.** Rejected — it forfeits the ADR-0016 framework axis
  and the ADR-0032 ecosystem. Policy seams are genuinely safe to open and genuinely valuable.
- **Tier by isolation-criticality: open policy seams raw, seal isolation-critical seams behind a
  runtime-mediated driver (CHOSEN).** The discriminator is *data-touch authority*. A policy seam that
  cannot decide which tenant's data is read is safe as a raw interface; a backend that can is not, and
  gets the sealed-driver treatment instead.

### Tier-1: how to expose the policy seams

- **New public interfaces the consumer implements against.** Rejected — a parallel public interface is a
  *shadow copy* of the internal one; the runtime would have to adapt between them, and they would drift.
- **Type aliases to the genuine internal interfaces (CHOSEN, ADR-0013 façade contract).** `GuardrailCheck`,
  `Reranker`, `Summarizer`, `EvalJudge`, `ModelPolicy`, `TenantRoutingPolicy`
  ([`aisotropy/extend.go`](../../aisotropy/extend.go)) are `=`-aliases of `guardrails.OutputCheck`,
  `semantic.Reranker`, `memory.Summarizer`, `eval.Judge`, `gateway.Policy`, `routing.Policy` — so a
  consumer's implementation **is** the exact value the runtime consumes, no adapter, no drift.

### Tier-2: how to open the isolation-critical backends (as shipped)

- **A raw `Store` interface (`Query`/`Upsert` taking arbitrary predicates).** Rejected — this is the
  leak: the plugin decides what to read, so isolation depends on the plugin getting scoping right.
- **A tenant-UNAWARE driver + a runtime-owned SEALED scoping decorator (CHOSEN, and shipped).**
  The plugin implements a driver that knows **nothing** about tenants (`semantic.VectorBackend` /
  `facts.DocBackend`: put/get/search over opaque, already-partitioned namespaces); the runtime wraps it
  in a **sealed** decorator (`NewVectorBackendStore` / `NewDocBackendStore`) that derives the namespace
  from `ctx` (ADR-0011) and that no plugin code can bypass or reach around. The `internal/plugintest`
  conformance + isolation-proof suite proves a driver honors the contract. The plugin brings storage;
  the runtime keeps isolation.

## Decision

### The rule

> **Open a seam raw iff it is a policy seam — decision/transform logic with no authority over which
> tenant's data is touched. If a seam can decide *what data is read or written*, it is
> isolation-critical and must never be a raw public interface; it gets a sealed, runtime-mediated
> driver instead.**

Tenant isolation is enforced **downstream**, in the ctx-scoped stores and the `Scope` constructors
(ADR-0011), *not* in any policy seam. That is precisely why the policy seams are safe: an implementation
cannot leak what it never had authority to touch.

### Tier-1 — the SAFE policy seams (shipped now)

Six aliases in [`aisotropy/extend.go`](../../aisotropy/extend.go), installed by functional options and
wired in [`aisotropy/app.go`](../../aisotropy/app.go)'s `startRuntime`. Per-seam status **exactly as
the code and [`extend_test.go`](../../aisotropy/extend_test.go) show it** (each wired seam is proven
*invoked by the real in-process `App.Turn → ChatCore` pipeline* by a spy that records the call):

| Seam (alias) | Option | Status | Where it runs / proof |
|---|---|---|---|
| `GuardrailCheck` (`guardrails.OutputCheck`) | `WithGuardrailCheck` | **WIRED** | Appended to the output inspector's checks; runs on every model response. Proven invoked, and with `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` on a Suspicious verdict replaces the reply with the safe refusal — `TestWithGuardrailCheckInvoked` (both invocation and block-output effect). |
| `Summarizer` (`memory.Summarizer`) | `WithSummarizer` | **WIRED** | Replaces the built-in `memory.LLMSummarizer` in `newWorkingMemory`; condenses the working-memory summary buffer. Proven invoked by overflowing the active window — `TestWithSummarizerInvoked`. |
| `EvalJudge` (`eval.Judge`) | `WithEvalJudge` | **WIRED** | Attached as the guardrails output judge **regardless** of the `AISOTROPY_GUARDRAILS_LLM_JUDGE` env toggle; scores every reply. Proven invoked — `TestWithEvalJudgeInvoked`. |
| `ModelPolicy` (`gateway.Policy`) | `WithModelPolicy` | **WIRED** | Chained **FIRST** in the model-selection gateway (ahead of the built-in override/image/cost rules); consulted on every turn, and a concrete `Target` wins while an abstention falls through. Proven consulted — `TestWithModelPolicyInvoked`. |
| `TenantRoutingPolicy` (`routing.Policy`) | `WithTenantRoutingPolicy` | **WIRED (active only with dedicated tenants)** | Replaces the static `AISOTROPY_DEDICATED_TENANTS` allowlist as the tier Router's policy. The Router is consulted only when a routing store exists (i.e. a dedicated-tenant allowlist is configured); with no dedicated tenants it is never consulted. Proven consulted under a configured allowlist — `TestWithTenantRoutingPolicyInvoked`. |
| `Reranker` (`semantic.Reranker`) | `WithReranker` | **WIRED** | Threaded onto the runtime (`rt.reranker`) and consulted by the semantic hydration path (`internal/api` `hydrateSystemPrompt` → `searchSemanticContext`) on the streaming `/chat/stream` and stateless `/ask` routes and in the bitrate hydrator: when set it over-fetches an isolation-clean candidate pool and `Rerank` reorders/trims it to the injection window (ADR-0006/ADR-0033), running strictly **after** the per-scope isolation filter so it can never widen scope. Proven invoked over the real `/ask` route — `TestWithRerankerInvoked` (aisotropy) — and at the hydration seam — `TestHydrateSystemPrompt_AppliesReranker` (internal/api). Note: the working-memory-centric non-streaming `/chat`/`Turn` path has no semantic-RAG rerank stage, so a `Reranker` affects the streaming/ask/bitrate paths, not plain `Turn`. |

Plus the **plugin loaders** `WithPlugin` / `WithPluginDir` (ADR-0032), wired and proven to merge a
plugin-declared, scope-gated tool into the runtime tool registry
(`TestWithPluginDirContributesTool`, `TestWithPluginFileContributesTool`).

**Why Tier-1 cannot leak isolation.** None of the six enforce tenant isolation — every one operates on
data the runtime already scoped from `ctx`, or produces a pure decision (a model target, a tier, a
verdict, a reranked order, a condensed summary, a score). A hostile or buggy implementation yields a
wrong *decision*, never a cross-tenant *read*. This is the safety that makes opening them raw
acceptable.

**Honest limitation (external implementability).** All six are *wired* and all six are
implementable **inside this module and its tests today**. But full implementation from an **external**
module still needs the payload types their signatures reference (`OutputCheckInput`/`Verdict`,
`ScoredPoint`, `Message`, `Criterion`/`EvalSample`/`Score`, `Request`/`Decision`, `TenantTier`)
re-exported as aliases — a pending follow-up documented in `extend.go`. "Aliased + wired" is not yet
"externally implementable"; this ADR does not conflate them.

### Tier-2 — the ISOLATION-CRITICAL backends (opened by sealed driver, never raw)

The facts store (`facts.Store`) and the semantic store (`semantic.Store`) are **not** aliased into the
extension surface as raw interfaces, and working memory, the tool `Registry`, and the memory `Scope`
and its constructors are **not** opened at all. `extend.go` states this refusal explicitly ("Do not
move a `facts.Store` / `semantic.Store` into this file to 'make it pluggable'"), because a raw store
interface would let an embedder bypass the "every `Scope` is built by the runtime from `ctx`" invariant
(ADR-0011) that makes isolation hold by construction.

The facts/semantic backends are instead opened via the sealed-driver design (built and wired,
installed via `WithVectorBackend` / `WithFactsBackend`):

- **A tenant-UNAWARE driver** — `semantic.VectorBackend` (upsert/search/delete/scan over an opaque,
  already-partitioned vector namespace) and `facts.DocBackend` (the document/task-journal analogue).
  The driver knows storage, not tenants: it never sees a `tenant_id`, only opaque namespaces the runtime
  chose (a SHA-256 over the `ctx` `Scope`).
- **A runtime-owned SEALED scoping decorator** — `NewVectorBackendStore` / `NewDocBackendStore` wrap the
  driver, derive every namespace from the ctx `Scope` (ADR-0011), and are **not bypassable** by plugin
  code (the plugin holds only the driver handle, never the decorator). The read-authority never leaves
  the runtime. `facts.DocBackend.ExportAll` is the single audited cross-tenant path (the sleep cycle's
  export), reconstructing identity from stored fields.
- **The `internal/plugintest` conformance suite** — `RunVectorBackendSuite` / `RunDocBackendSuite`, a
  table of cross-tenant, cross-user, and durability probes (incl. the central isolation proof: a marker
  written through the decorator for tenant A is invisible to tenant B, while physically present in the
  raw backend under A's namespace) a candidate driver must pass, so "bring your own backend" is
  verifiable, not trust-me.

The slogan is the design: **bring your own backend, not your own isolation.**

### The differentiator, stated plainly

A typical framework's "pluggable store" hands you an interface that decides *what to read*, so a wrong
or hostile backend can read across tenants — the isolation guarantee degrades to the weakest plugin
ever installed. Aisotropy **seals the isolation**: the policy seams that are safe to open are opened
raw (Tier-1), and the backends that are not are opened — later — only as tenant-unaware drivers behind
a sealed, runtime-owned scoping decorator (Tier-2), so **a wrong backend still cannot leak across
tenants**. The read-authority is structurally retained by the runtime, by construction, on both tiers.

## Consequences

- **An extender (and a reviewer) has a rule, not a case-by-case judgment call.** Policy seam → safe to
  open raw; can-decide-what-data-is-read → sealed driver only. The rule is what keeps future "open
  seam X?" requests from silently regressing isolation.
- **Tier-1 is real and honestly reported.** All six seams are now proven invoked by the running
  pipeline (the former stored-not-wired exception, `Reranker`, is APPLIED on the semantic hydration
  path — streaming `/chat/stream`, `/ask`, and the bitrate hydrator — asserted by
  `TestWithRerankerInvoked` and `TestHydrateSystemPrompt_AppliesReranker`). No reader can mistake
  "aliased" for "wired," or "wired inside the module" for "implementable from an external module."
- **Tier-1 is structurally incapable of leaking isolation.** Because isolation is enforced downstream
  in the ctx-scoped stores, a community policy implementation — however wrong — cannot produce a
  cross-tenant read. This is the property that justifies opening these six raw.
- **Tier-2 is now opened as designed — still sealed.** The memory backends are exposed not as raw
  `Store` interfaces but as the specified sealed-driver design: tenant-unaware drivers
  (`semantic.VectorBackend` / `facts.DocBackend`) behind runtime-owned sealed scoping decorators
  (`semantic.NewVectorBackendStore` / `facts.NewDocBackendStore`, namespace derived from the
  ctx-scoped `Scope`), plus the `internal/plugintest` conformance + isolation-proof suite. They are
  installed via `WithVectorBackend` / `WithFactsBackend` (`aisotropy/extend.go`, wired through
  `startRuntime` and the shared `ensureSemantic` build path). A community backend supplies persistence
  but never sees, chooses, or can forge a tenant — the isolation guarantee does not degrade to the
  weakest backend.
- **The positioning is executable, not marketing.** "We seal the isolation competitors leak" is backed
  by the rule, the Tier-1 downstream-enforcement property, and the shipped Tier-2 sealed-driver +
  `plugintest` conformance suite — verifiable, not a claim.
- **Named follow-ons (honest gaps).** Re-exporting the Tier-1 payload types and the Tier-2 driver
  payload types (`BackendVector`/`BackendHit`, `BackendDoc`/`BackendDocHit`) so the seams are fully
  implementable from an EXTERNAL module (today implementable inside this module) — a distinct future
  change.

## References

- **Extends / operationalizes:** [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md)
  (sealed kernel L0 vs. composition surface — this ADR decides *which side of the seal* each seam is
  on) · [ADR-0013](0013-public-extension-surface-facade-vs-promote.md) (façade of type aliases — the
  Tier-1 aliases follow its contract; its explicit worry about making `facts.Scope`/`ExportAll` public
  is exactly the Tier-2 line)
- **Sibling:** [ADR-0032](0032-plugin-ecosystem.md) (the plugin ecosystem; a plugin's tool
  contributions ride the Tier-1 surface and pass the same scope gate)
- **Invariants held:** [ADR-0011](0011-tenant-scoped-statement-builder.md) (isolation by construction —
  the `Scope`-from-`ctx` invariant Tier-2's sealed decorator preserves and Tier-1's downstream
  enforcement relies on) · [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) (the tiered
  multi-tenancy the `TenantRoutingPolicy` seam assigns) · [ADR-0002](0002-anti-framework-magic-struct-as-contract.md)
  (the aliases are the genuine internal types, no shadow contract) · [ADR-0006](0006-reranker-cohere-vs-bge-local.md)
  (the reranking the `Reranker` seam hosts on the retrieval path)
- **Tier-1 seams + wiring:** [`aisotropy/extend.go`](../../aisotropy/extend.go) (`GuardrailCheck`,
  `Reranker`, `Summarizer`, `EvalJudge`, `ModelPolicy`, `TenantRoutingPolicy`; `WithGuardrailCheck`,
  `WithReranker`, `WithSummarizer`, `WithEvalJudge`, `WithModelPolicy`, `WithTenantRoutingPolicy`,
  `WithPlugin`, `WithPluginDir`; `appExt`) · [`aisotropy/app.go`](../../aisotropy/app.go)
  (`startRuntime` — the install points, incl. threading the applied `Reranker` onto `rt.reranker` for
  `RegisterStream`/`RegisterAsk`/the bitrate hydrator) ·
  [`aisotropy/extend_test.go`](../../aisotropy/extend_test.go) (the per-seam invocation spies:
  `TestWithGuardrailCheckInvoked`, `TestWithSummarizerInvoked`, `TestWithEvalJudgeInvoked`,
  `TestWithModelPolicyInvoked`, `TestWithTenantRoutingPolicyInvoked`, `TestWithRerankerInvoked`,
  `TestWithPluginDirContributesTool`, `TestWithPluginFileContributesTool`,
  `TestNoOptionsRuntimeUnchanged`)
- **Tier-2 sealed backends (opened as sealed drivers):**
  [`internal/semantic/backend.go`](../../internal/semantic/backend.go) (`VectorBackend` driver +
  `NewVectorBackendStore` sealed decorator) · [`internal/facts/backend.go`](../../internal/facts/backend.go)
  (`DocBackend` driver + `NewDocBackendStore` sealed decorator) ·
  [`internal/plugintest`](../../internal/plugintest) (the conformance + tenant-isolation-proof suites,
  `RunVectorBackendSuite` / `RunDocBackendSuite`) · installed via `WithVectorBackend` /
  `WithFactsBackend` (`aisotropy/extend.go`, wired in `startRuntime` + `ensureSemantic`) ·
  [`internal/tenancy`](../../internal/tenancy) (the `Scope`-from-`ctx` construction the sealed
  decorators derive the namespace from)
