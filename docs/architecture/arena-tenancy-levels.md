---
type: Process
title: "Arena Tenancy Levels — the L0-L3 ladder, and the corrected 'no concept of a tenant' claim"
description: A blanket claim shipped that the seven competitor frameworks have "no concept of a tenant" — false as stated. This document introduces the reusable L0 (no concept) to L3 (enforced by construction) ladder, applies it framework-by-framework with evidence, places aisotropy honestly on the same ladder per layer (including the edge/facade caveats a "by construction" claim would otherwise hide), records the correction already applied in commit 7743d51, and designs — but does not implement — a "forgetful integrator" negative dimension that would measure the gradation directly.
tags: [architecture, process, arena, multi-tenancy, isolation, tenancy, research, code-truth]
timestamp: 2026-07-22T00:00:00Z
---

# Arena Tenancy Levels — the L0-L3 ladder, and the corrected "no concept of a tenant" claim

> **The claim that was corrected, and why.** An earlier revision of
> `docs/architecture/arena-benchmark-methodology.md` described the seven competitor frameworks in the
> live arena as having "no concept of a tenant" — a blanket statement. It is **false as stated**. Four
> of the seven (AI SDK, Pydantic AI, Eino, Genkit-Go) genuinely have no framework-owned data plane to
> scope, so the claim holds for them. But LangGraph ships a hierarchical namespace-tuple `BaseStore`
> with semantic search; Mastra ships a `(resourceId, threadId)` scoping primitive with a documented,
> shipped-source request-context override; LlamaIndex documents a first-party multi-tenancy RAG
> pattern; Spring AI requires a caller-supplied `conversationId` and throws if it is omitted. Those are
> real scoping primitives, not "no concept." This document exists because the correction itself is the
> deliverable: it replaces the blanket claim with a reusable ladder, applies that ladder with evidence
> to every framework the arena covers (competitors and aisotropy alike, with the same rigor in both
> directions), and records exactly where and how the correction was made — following
> `.claude/rules/prune-evidence-gate.md`'s code-truth discipline applied to a *capability-absence*
> claim rather than a deletion claim.

## The L0–L3 ladder

A reusable four-point scale for "how does this system keep one tenant's data from another's":

| Level | Definition |
|---|---|
| **L0** | No concept. Nothing in the framework's own data plane represents a tenant or scope at all. |
| **L1** | Scoping parameter. Data is partitioned *if* the caller passes the scoping value correctly on every call. A wrong or omitted value silently reads another scope — the mistake is possible and invisible. |
| **L2** | Enforced at a boundary. Identity is bound once (e.g. at an HTTP middleware or server layer); downstream code cannot escape that binding without deliberate effort. |
| **L3** | Enforced by construction. Cross-scope access is unrepresentable — there is no code path that can even attempt to read another tenant's data without failing to compile, failing validation, or otherwise being structurally impossible. |

The ladder is deliberately about the **framework's own primitive**, not about what a careful
integrator can build on top of an L0 or L1 framework — an L0 framework can still be embedded safely
if the embedder writes their own scoping discipline; the ladder measures what the framework *gives
you for free*, not what is theoretically achievable with enough application code.

## Framework matrix (evidence)

| Framework | Level | Evidence |
|---|---|---|
| LangGraph OSS | **L1** | `thread_id` checkpointer; `BaseStore` namespace tuples — https://docs.langchain.com/langgraph-platform/auth ; https://docs.langchain.com/oss/python/langchain/long-term-memory ; the arena's own adapter concedes the same read: `arenas/live/langgraph/modes/scoped_store.py:5-24`. |
| LangGraph Platform | **L2** (commercially gated) | `@auth.authenticate` on every request; `@auth.on` handlers gate threads/assistants/crons/store; a handler rewrites the namespace per user — https://github.com/langchain-ai/langgraph/issues/5390. Conflicting signals on the exact plan tier this requires; **unverified**, see Could not establish. |
| LlamaIndex | **L1** | First-party documented multi-tenancy RAG pattern, per-user `MetadataFilters` — https://docs.llamaindex.ai/en/stable/examples/multi_tenancy/multi_tenancy_rag/ ; https://qdrant.tech/documentation/examples/llama-index-multitenancy/. LlamaCloud's org/project boundary is a platform-level concept; per-user scoping within a project is still application-written filters. |
| Mastra (library) | **L1** | `(resourceId, threadId)` — https://mastra.ai/docs/server/request-context ; the arena's own adapter passes both: `arenas/live/mastra/lib/modes/memory.mjs:53-56`. |
| Mastra (with server) | **L2** | Request-context override precedence **verified in shipped compiled source** (not docs-only): `requestContext?.get(MASTRA_RESOURCE_ID_KEY) \|\| options?.memory?.resource` at `@mastra/core/dist/chunk-34TKMNFP.js:6117`, plus documented ownership-check 403 semantics — https://mastra.ai/docs/server/middleware. **Caveat:** the 403 check itself lives in `@mastra/server`, which was not installed during this research — that specific claim is docs-only, not source-verified (see Could not establish). |
| Pydantic AI | **L0** | `message_history` is a plain parameter; persistence is entirely the caller's responsibility — https://ai.pydantic.dev/message-history/. |
| AI SDK (Vercel) | **L0** | `onFinish` — save it yourself, no framework data plane — https://ai-sdk.dev/docs/ai-sdk-ui/chatbot-message-persistence. |
| Eino | **L0/L1** | `CheckPointStore` is keyed by a single opaque `CheckPointID` with no tenant semantics — https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/checkpoint_interrupt/ ; the arena's adapter: `arenas/live/eino/mode_scoped_memory.go:16-31`. |
| Genkit Go | **L0** (data plane) / **L2** (identity, no data plane to apply it to) | No framework-owned data plane to scope — but flow-boundary identity IS real L2: `ContextProviders`/auth policies bind `context.auth` (e.g. a Firebase uid) at the flow boundary — https://genkit.dev/docs/auth/. The Go docs lag the JS docs, so this framework's L0-for-data-plane conclusion is an absence claim not independently source-verified beyond the docs read (see Could not establish). |
| Spring AI | **L1** | Required `ChatMemory.CONVERSATION_ID` — throws if omitted, so the failure mode is loud rather than silent, still single-axis — https://docs.spring.io/spring-ai/reference/api/chat-memory.html ; `FilterExpression` per-query for vectors; arena adapter: `arenas/live/spring-ai/src/main/java/arena/springai/RqliteChatMemoryRepository.java`. |
| Vector-store level (Qdrant/pgvector/Pinecone) | infra-level, not framework-level | Multi-tenancy support exists at the vector-store layer, equally available to every framework above regardless of its own ladder position. **No surveyed framework auto-plumbs the tenant filter** — every integration checked has the user construct the filter per query by hand. This is absence-of-evidence from docs plus spot checks, not an exhaustive source read of every framework's vector-store integration code. |

**What survives across every framework surveyed:** no OSS-core path reaches L3. Every OSS-core path is
either L1 (a parameter that partitions correctly if used correctly) or L0 (nothing at all). L2
enforcement appears only in commercial/server tiers layered on top of the OSS core. No surveyed
framework's own core reaches L3.

## aisotropy's honest placement — per layer, including the caveats

This section is the doc's credibility test: the same rigor applied to the seven competitors above is
applied here, including the parts that do not read as flattering. Softening this section would defeat
the entire point of writing the document.

- **Storage / downstream tiers: L3.** `internal/memory/key.go:88-118` defines a compound
  `(tenant, user, session)` key whose `Validate()` rejects empty components or reserved characters
  before any BadgerDB write happens — a key is never assembled by ad hoc string concatenation
  elsewhere. `internal/api/chat.go:193-199`'s `withIdentity` runs the identical `memory.Key.Validate()`
  check before writing tenant/user into `ctx`, so an invalid identity never reaches a handler body or a
  downstream storage key. `internal/semantic/isolation_test.go` is a **meta-test that fails the build**
  if Qdrant isolation regresses: `TestIsolationSemanticQdrantUsesMustFilterNotPostFilter`
  (`isolation_test.go:268-301`) asserts the `Search` method's source contains a `Must:`-filter over
  both tenant and user built with `qdrant.NewMatch`, and explicitly fails if it instead detects a
  post-scan `continue`-on-mismatch pattern — a post-filter would itself be a leak, not a fix, per
  `.claude/rules/constitutional-gates.md`. A second test (`isolation_test.go:360-370` region) fails the
  build if any point-returning `Store` method exists with no `Scope` argument.
- **HTTP edge: L2 when auth is configured; L1 in NoAuth mode.** Default posture is
  **default-deny**: `aisotropy/app.go:1153-1157` rejects every `/v1` request with 401 unless an
  authenticator was configured (`aisotropy.WithAuthenticator`) or NoAuth was explicitly requested
  (`aisotropy.NoAuth()`). `aisotropy/authenv.go` requires an explicit
  `AISOTROPY_ALLOW_NOAUTH=1` opt-in before NoAuth activates at all (never by mere omission — see the
  comment block starting `authenv.go:17`), and a **second**, deliberately separate guard,
  `AISOTROPY_ALLOW_NOAUTH_NONLOCAL`, before NoAuth is permitted on a non-loopback bind address
  (`authenv.go:29-37,199-227`). When a JWT authenticator is configured, the verified token's
  tenant/user MUST agree with the URL path's tenant/user or the request is rejected with 403
  (`aisotropy/auth.go:18`'s comment, `auth.go:112-113`) — a must-agree bind, not a trust-the-token
  pattern. **The caveat that keeps this honest:** in NoAuth mode, the edge degrades to L1 — the path
  is trusted, and identity provenance becomes whatever the caller puts in the URL, exactly the
  parameter-trust failure mode every L1 competitor framework above shares.
- **Library facade: mandatory-but-trusted scope, stronger than L1, not L3.** `App.Turn`
  (`aisotropy/turn.go:97`) and `App.SeedHistory` (`aisotropy/seed_history.go:87-99`) both **require** a
  `(tenant, user, session)` triple and validate it through the identical `memory.Key.Validate()` used
  everywhere else — an empty or reserved-character identity is a hard error, never silently widened
  into a cross-tenant prefix (`seed_history.go:94-97`). This is stronger than a typical L1 parameter,
  because the scope cannot be *omitted* — there is no code path where a blank identity reaches storage.
  But it is not L3 either: the facade accepts **whatever** tenant string the embedding application
  passes. Identity **provenance** — proving the caller actually is who they claim — is the embedder's
  job at this layer, the same way it is the embedder's job for every L1 framework above once you step
  outside that framework's own scoping parameter.
- **The accurate one-sentence self-description:** aisotropy has L3 storage discipline **downstream of**
  an identity that is L2-verified at the HTTP edge (when auth is configured) and caller-trusted at the
  library facade. "By construction" is correct about storage. It is **silent about the edge** unless
  stated explicitly — which an earlier revision of the benchmark-methodology doc did not do, and which
  this document (and the correction below) now does.

## Corrections already applied (2026-07-22)

- **`docs/architecture/arena-benchmark-methodology.md`**, the "Same checkmark, different guarantee"
  section — rewritten to replace the blanket "no concept of a tenant" framing with the L0–L3
  gradation above, a corrected lockers analogy (four frameworks had no lock of their own — the arena
  adapter bolted one on; three came with a latch the integrator must remember to fasten, and their
  vendors sell a doorman for the commercial tier), the aisotropy edge caveat verbatim, and a note on
  the forgetful-integrator design (§ below). Landed in commit **`7743d51`** — verified via
  `git show --stat 7743d51`, which touches `arenas/live/runner/results.go`,
  `arenas/live/runner/results_test.go`, and `docs/architecture/arena-benchmark-methodology.md` (44
  lines changed in the doc).
- **`arenas/live/runner/results.go`** — the isolation caveat rendered above the scoped-memory table in
  every future generated `RESULTS.md` was corrected the same way, in the same commit
  (`7743d51`), so the gradation is visible on every regenerated board going forward, not only in this
  standing document.

## The measurable design: "forgetful integrator" — **DESIGNED, NOT IMPLEMENTED**

Nothing in this section exists yet. No adapter code, runner flag, scorer, or `matrix.json` entry
described below has landed in `arenas/live/`.

The L0–L3 ladder above is prose and a table — accurate, but not a measured result. This design turns
the distinction into an actual arena dimension, `isolation_forgetful`, that exercises the **real-world
mistake an L1 scoping parameter makes possible and invisible**:

1. Write a fact under scope A through the framework's **native** scoping primitive (e.g. LangGraph's
   `BaseStore` namespace, Mastra's `resourceId`, Spring AI's `conversationId`).
2. Perform one read with the scope **omitted or defaulted** — the exact mistake an integrator makes by
   accident, not a contrived attack.
3. Score deterministically, three ways: **refuses/errors** = enforcement (pass); **returns scope-A
   data** = leak (fail); **returns empty because the omission silently created a *different* scope** =
   a third, honest category, "silent partition" — this is L1's real failure mode, because the mistake
   is invisible to whoever made it (no error, no leak either, just quietly wrong data going forward).
4. **aisotropy's own expected result:** omission is unrepresentable over HTTP in the first place —
   Huma returns 404/422 on a missing path parameter before a handler ever runs, and a blank identity
   independently fails `Key.Validate()` — so aisotropy lands in "refuses" by construction, not by a
   defensive check added for this test.
5. **Fairness constraint:** the omission must happen at the **framework's own API boundary**, not in
   adapter glue code written for the arena — testing "what happens if the *adapter* forgets to pass
   the scope" would measure the adapter author, not the framework, the same fairness principle
   `docs/architecture/arena-idiomaticity-and-dx.md` applies to idiomatic-usage claims generally.
6. **L0 frameworks are `infeasible` for this dimension**, per the honest-arena vocabulary
   (`.claude/rules/arena-full-matrix.md`): there is no native scope to forget in the first place, so
   exercising "omission" would only be exercising adapter-invented code, not the framework.

Scoring is a pure function — no LLM, no network call, no randomness — consistent with
`.claude/rules/arena-deterministic-judging.md`'s standing premise that the arena's primary quality
signal is always deterministic.

## Could not establish

- **Mastra's server-side 403 ownership check was not verified in source** — `@mastra/server` was not
  installed during this research, so that specific claim (§ framework matrix, Mastra-with-server row)
  rests on documentation only, not a source read.
- **Genkit-Go's "no framework-owned data plane" conclusion is not independently source-verified beyond
  the documentation** — the Go docs are known to lag the JS docs for this framework, and the absence
  of a data-plane concept was not confirmed by reading the Go SDK's source directly.
- **LangGraph Platform's custom-auth plan gating is unresolved** — public signals on which commercial
  tier `@auth.on` handlers require conflict with each other; the exact gating was not resolved from
  available sources.
- **`eino-ext` was not exhaustively enumerated** — the eino ecosystem's extension packages beyond the
  core and `eino-examples` were not fully surveyed for a tenancy-relevant addition.
- **"No framework auto-plumbs the vector-store tenant filter" is an absence-of-evidence claim** — based
  on documentation review plus spot checks of the arena's own adapters, not an exhaustive read of
  every framework's vector-store integration source.
- **WebFetch-sourced doc summaries used in this research are model-mediated**, not verbatim quotes —
  in particular, Mastra's documentation language was paraphrased during research rather than quoted
  character-for-character; the interpretation is believed accurate but was not independently
  re-verified against the raw doc HTML for this write-up.

## Cross-links

- `docs/architecture/arena-benchmark-methodology.md` — the "Same checkmark, different guarantee"
  section this document supersedes as the *standing* source for the tenancy gradation (the
  methodology doc keeps the corrected prose; this document is the fuller research writeup the
  correction was distilled from).
- `docs/architecture/multi-tenancy.md` — aisotropy's own multi-tenancy design in full (ctx propagation,
  the anti-leak composite key, per-layer filtering) that this document's "aisotropy's honest placement"
  section summarizes and grades against the L0–L3 ladder.
- `.claude/rules/constitutional-gates.md` — the hard gate (in-scan Qdrant filtering, never a
  post-filter) that `internal/semantic/isolation_test.go`'s meta-test enforces at build time.
- `.claude/rules/prune-evidence-gate.md` — the code-truth discipline ("code + tests are the truth, not
  a doc") this document's correction of the blanket "no concept of a tenant" claim is a direct
  application of, extended from deletion claims to absence claims.
- `.claude/rules/arena-full-matrix.md` — the infeasible-vs-run-and-fail vocabulary the
  `isolation_forgetful` design's L0 handling (§ above) follows.
- `.claude/rules/arena-deterministic-judging.md` — the standing premise (pure-function scoring, no LLM
  judge) the `isolation_forgetful` scorer design follows.
- `docs/architecture/arena-memory-extensions.md` — the companion capability-ladder document; both this
  document and that one exist because a claim about a competitor framework (uniqueness in one case,
  absence in this one) needed an ecosystem-level, evidence-cited check before shipping.
- `docs/architecture/arena-idiomaticity-and-dx.md` — the fairness principle (measure the framework, not
  the adapter author) this document's forgetful-integrator design explicitly inherits in its
  omission-must-happen-at-the-framework's-own-boundary constraint.
- `docs/architecture/arena-adapter-artifacts.md` — the Q0 discipline (core vs. adapter vs. doc) that
  applies equally here: an isolation-dimension result is about the *framework*, never about the
  *arena adapter* written to exercise it, unless proven otherwise.
