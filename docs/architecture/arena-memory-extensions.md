---
type: Process
title: "Arena Memory Extensions — the ecosystem test for cross-session, semantic, and consolidated memory"
description: An ecosystem-wide capability audit of the seven competitor frameworks' official memory extensions (langgraph/langmem, llamaindex, mastra, pydantic-ai-harness, ai-sdk, eino, genkit-go) plus the mem0/Zep/Letta third-party landscape, run before any "unique to us" claim about cross-session/semantic/consolidated memory. Verdict — the claim does not survive as stated; three frameworks have official paths to all or most of it. Includes the three-dimension arena design (memory_cross_session, memory_semantic_recall, memory_consolidation), which has since been IMPLEMENTED and MEASURED on the published board.
tags: [architecture, process, arena, memory, ecosystem, research, code-truth]
timestamp: 2026-07-22T00:00:00Z
---

# Arena Memory Extensions — the ecosystem test for cross-session, semantic, and consolidated memory

> **Why this research exists.** Aisotropy's memory tiers — working memory (BadgerDB), facts
> (rqlite/SQLite FTS5), semantic (Qdrant), and async sleep-cycle consolidation — are default-on and
> tenant-scoped by construction (see `docs/architecture/memory-tiers.md`,
> `docs/architecture/multi-tenancy.md`). It is tempting to describe that as "unique to us": no
> competitor framework in the live arena ships a comparable tiered-memory pipeline in its own
> adapter. But the arena's own discipline — `.claude/rules/prune-evidence-gate.md`'s "code + tests are
> the truth, not a doc read" and `arena-full-matrix.md`'s full-cartesian-coverage law — applies just as
> much to a positive uniqueness claim as to a deletion claim. **Fairness demands giving each
> competitor its best OFFICIAL extension before claiming something is unobtainable elsewhere.** A
> claim of "unique to aisotropy" that has not been checked against each framework's official
> memory-extension ecosystem (not just its bare core) is exactly the kind of code-truth-free assertion
> `docs/architecture/arena-adapter-artifacts.md` and `docs/architecture/arena-benchmark-methodology.md`
> exist to prevent. This document is that check, run before the claim shipped anywhere public.

## Verdict

**The claim "cross-session / semantic long-term / consolidation is unique to aisotropy" does not
survive as stated.** langgraph, llamaindex, and mastra each have an official path to all or most of
the three capabilities, via their own core or an in-tree first-party extension. The honest reframe:
aisotropy's differentiator is that the three tiers are **default-on and integrated** (every request
gets them with zero opt-in configuration, plus construction-enforced tenancy per
`docs/architecture/arena-tenancy-levels.md`) — not that the capabilities are **unobtainable**
elsewhere. Structurally, only langgraph, llamaindex, and mastra reach official coverage of all three;
pydantic-ai reaches one of three officially; genkit-go and eino offer session-ID-keyed transcript
persistence at best, which a different-session-ID test defeats by construction; ai-sdk has nothing
first-party.

## 1. Per-framework capability matrix

| Framework | (1) Cross-session | (2) Semantic long-term | (3) Consolidation | Key evidence |
|---|---|---|---|---|
| langgraph | NATIVE — checkpointer backends (thread-scoped) + `BaseStore` (user-namespaced, cross-thread) | NATIVE — `BaseStore` `index` config with embeddings; semantic `search()` | OFFICIAL-EXTENSION — langmem SDK (memory managers, async background manager); pre-1.0 (0.0.30, Oct 2025; active Jun 2026; ~746K monthly downloads) | https://docs.langchain.com/oss/python/langgraph/add-memory ; https://blog.langchain.com/semantic-search-for-langgraph-memory/ ; https://langchain-ai.github.io/langmem/concepts/conceptual_guide/ ; https://www.langchain.com/blog/langmem-sdk-launch |
| llamaindex | NATIVE — `Memory` (core), SQL-backed store, survives processes | NATIVE — `VectorMemoryBlock` (flushed chat → vector store, Qdrant supported) | NATIVE, synchronous-on-flush (not async) — `FactExtractionMemoryBlock` | https://developers.llamaindex.ai/python/framework/module_guides/deploying/agents/memory/ ; https://developers.llamaindex.ai/python/examples/memory/memory/ ; https://www.llamaindex.ai/blog/improved-long-and-short-term-memory-for-llamaindex-agents |
| mastra | NATIVE — `Memory` + storage adapters (LibSQL/Postgres/Mongo/Upstash); resource-scoped working memory across threads | NATIVE — `semanticRecall` (vector + embedder, `scope:'resource'`); OFF by default (compiled source: `arenas/live/mastra/node_modules/@mastra/core/dist/chunk-77JDY5O7.js:7526-7527`) | PARTIAL-NATIVE — LLM-maintained working-memory profile, in-hot-path; no async consolidator | https://mastra.ai/docs/memory/overview ; https://mastra.ai/docs/memory/working-memory ; https://mastra.ai/docs/memory/storage |
| pydantic-ai | OFFICIAL-EXTENSION — pydantic-ai-harness `Memory` capability (persistent namespaced notebook, bounded prompt injection, on-demand search) + Step Persistence | NONE official (search mechanism not established — see Open questions) | NONE official | https://pydantic.dev/docs/ai/harness/ ; https://pydantic.dev/docs/ai/core-concepts/capabilities/ ; https://github.com/pydantic/pydantic-ai-harness/issues/30 |
| ai-sdk (Vercel) | NONE first-party (community providers: mem0, Letta) | NONE first-party | NONE first-party | https://ai-sdk.dev/providers/community-providers/mem0 ; https://github.com/letta-ai/vercel-ai-sdk-provider |
| eino | NONE in core — adk v0.9.13 (checked 2026-07-22): in-process ctx KV + `CheckPointStore` (interrupt/resume only); `MemoryStore` exists only in `eino-examples`; open feature request #203 | NONE | NONE | https://pkg.go.dev/github.com/cloudwego/eino/adk ; https://github.com/cloudwego/eino/issues/203 |
| genkit-go | OFFICIAL-EXPERIMENTAL — `core/x/session` (`WithExperimental`): in-memory/file/Firestore, transcript-by-session-ID (server-managed replay, not user-scoped memory) | NONE | NONE | https://firebase.google.com/docs/genkit/chat ; https://www.infoq.com/news/2026/07/genkit-agents-api-preview/ |

**Structural read.** Only langgraph, llamaindex, and mastra have an official path to all three
capabilities. pydantic-ai has official cross-session support only. genkit-go and eino offer, at best,
session-ID-keyed transcript persistence — a test that varies the session ID while holding the user
constant defeats both by construction (see §3's cross-cutting protocol change). ai-sdk has nothing
first-party for any of the three.

### 1.1 2026-07 ecosystem sweep — the four weak adapters (post-measurement labels)

Re-swept after the full board landed (`arenas/live/RESULTS.md` @ `06945bf`), per
`.claude/rules/capability-absence-claims.md` (what exists + maturity + why it does not change the
user-scoped verdict — never "X has no memory"):

- **pydantic-ai** — native memory: **NONE** (open issue
  [pydantic/pydantic-ai-harness#30](https://github.com/pydantic/pydantic-ai-harness/issues/30), 2026-03,
  tracks it); THIRD-PARTY only (Hindsight, Neo4j Agent Memory, MemU). The board measured its
  app-level user-scoped store over native `message_history` serialization — its maximum official
  surface today.
- **ai-sdk** — core stays **L0** (no primitive); Vercel now publishes a **PRESCRIBED-PATTERN** doc,
  <https://ai-sdk.dev/docs/agents/memory> (user-scoped cross-session via external storage, a
  `userId`+`sessionId` example). `@mem0/vercel-ai-provider` exists but Vercel classifies it a
  "Community Provider" (<https://ai-sdk.dev/providers/community-providers/mem0>). The board measured
  the session-keyed core default; wiring the prescribed pattern is a filed follow-up.
- **eino** — **OFFICIAL-EXAMPLE** `MemoryStore`
  (<https://pkg.go.dev/github.com/cloudwego/eino-examples/flow/agent/react/memory_example/memory>,
  2026-03) — SESSION-keyed; a session-keyed store cannot pass a varies-session/holds-user dimension
  by construction.
- **genkit-go** — Genkit **Agents API PREVIEW** (2026-07, TS + Go): persistent sessions +
  `SessionStore` (File/Firestore/Custom) —
  <https://www.infoq.com/news/2026/07/genkit-agents-api-preview/> and <https://genkit.dev/docs/chat/>.
  Maturity **NATIVE-PREVIEW**, SESSION-scoped: no user-scoped cross-session recall, so the verdict
  stands — but this preview MUST be cited alongside it.

## 2. Third-party memory services — arena eligibility

| Service | License | Self-host | Provides | Integrations |
|---|---|---|---|---|
| mem0 | Apache-2.0 (https://github.com/mem0ai/mem0/blob/main/LICENSE) | YES — OSS SDK, local vector store incl. Qdrant; hosted platform has proprietary extras (https://docs.mem0.ai/open-source/overview) | (1) + (2) + (3) — an LLM fact-extraction pipeline is its core | llamaindex OFFICIAL IN-TREE (`llama-index-memory-mem0`); mastra OFFICIAL IN-TREE (`@mastra/mem0`, docs use platform key); langgraph documented on the mem0 side; ai-sdk community provider |
| Zep | CE discontinued (legacy Apache-2.0, unmaintained); current product is cloud-API-only | NO | (1) + (2) + (3), but only via the cloud API | **DISQUALIFIED for the arena** — cloud-only violates infra parity (`docs/architecture/arena-infrastructure-parity.md`); https://blog.getzep.com/announcing-a-new-direction-for-zeps-open-source-strategy/ |
| Letta | Apache-2.0 | YES | A full stateful-agent RUNTIME, not a memory add-on — it replaces the framework's own agent loop | **DISQUALIFIED as an equalizer** — a Letta-backed cell benchmarks Letta, not the framework under test |

**Arena-eligibility verdict:** mem0 OSS is the only eligible equalizer — Apache-2.0, self-hostable,
with more than one official in-tree integration. Zep is disqualified per the license-policy /
infrastructure-parity rules (cloud-only = a different infra tier than every other cell in the
matrix). Letta is disqualified because adding it to a framework's cell would measure Letta's own
agent runtime, not the framework being compared — the same "equalizer must not become the subject"
principle that governs mem0's inclusion elsewhere in the arena.

## 3. Three-dimension design — **IMPLEMENTED AND MEASURED**

> **STATUS UPDATE — IMPLEMENTED AND MEASURED (2026-07-27).** The text below was written as a
> design and is preserved as the specification the work was built against. It has since SHIPPED:
> the adapters, the runner modes, the deterministic scorers and the `matrix.json` cells all exist,
> and the dimensions RAN on the published board (`arenas/live/RESULTS.md`). Read what follows as
> "the spec we built to", never as "this does not exist yet" — the results are on the board.

The section below is the original design. Historical note: when written, no adapter, runner flag, scorer, or
`matrix.json` entry described here exists yet in `arenas/live/`. It is documented now so the
eventual implementation has a fairness-reviewed specification to build against, per
`.claude/rules/research-first-protocol.md`'s Phase 0/1 split (this is the Phase 0 research report;
operator triage on the open questions in §6 is the gating Phase 1 step before any adapter code is
written).

### 3.1 Cross-cutting protocol changes (apply to all three dimensions)

1. **`user_id` field.** `adapterInput` gains an optional `user_id` alongside the existing
   `session_id`. The new dimensions vary `session_id` while holding `user_id` constant across
   invocations — that single change defeats transcript replay by construction (an adapter that only
   knows how to replay a session-keyed transcript returns nothing for a new session ID, which is the
   point: a session-scoped store cannot pass a cross-session test by accident).
2. **Per-run data root for every adapter.** Aisotropy's own arena adapter previously pinned
   `AISOTROPY_DATA_DIR`, which caused cross-RUN contamination — see
   `docs/architecture/arena-memory-forensics.md` §3.4 for the diagnosis of that class of bug. Every
   adapter in the new dimensions gets a fresh per-run data root; cross-session behavior is tested
   **inside one run**, never inferred from residue left by a previous run.
3. **Per-framework Qdrant collections.** Memory dimensions that use Qdrant-backed storage use
   `arena_mem_<fw>` collections, the same collision-avoidance rule
   `.claude/rules/arena-deterministic-judging.md`'s "Collision fix" section already establishes for
   RAG collections.

### 3.2 `memory_cross_session`

Two invocations, **separate processes**. Invocation 1 persists a fact under session A, user U.
Invocation 2 recalls with session B (different session, same user U) — no wall-clock gap (forbidden
by the arena's timing-honesty rules). Scorer: the existing `ScoreMemory` answer-key containment
(`scoring.go:115`, threshold gate `passQAThreshold`, `scoring.go:57`) — no new scorer needed.

**aisotropy note:** working memory is session-keyed, so recall in this dimension must ride the
facts/semantic tiers — the dimension exercises consolidation indirectly. **Prerequisite:**
`resetRqliteFacts` must become per-RUN, not per-boot (`rqlite_reset.go:39-69`, called at
`main.go:317-327`), or aisotropy fails its own dimension by harness construction rather than by
capability.

**Fairness — best official primitive per framework:** langgraph `BaseStore` (this also closes
idiomaticity SHOULD-FIX #1, see `docs/architecture/arena-idiomaticity-and-dx.md`); llamaindex
`Memory` + blocks; mastra resource scope; pydantic-ai harness `Memory`. genkit-go and eino are
expected to **run and fail** (see §4's run-and-fail-vs-infeasible rule) rather than being marked
infeasible, since both are executable — they simply lack the capability under a genuinely
cross-session probe.

### 3.3 `memory_semantic_recall`

Invocation 1 persists roughly 30–40 turns with one "needle" fact stated semantically (never as a
verbatim quotable string). Invocation 2 — new session, same user — asks a **paraphrase** that shares
no content tokens with the needle statement. This defeats transcript replay two ways: (a)
structurally — a fresh session ID makes a session-keyed store return empty, so only user-scoped
semantic retrieval can find the needle; and (b) visibly, in cost — an adapter that instead persists
per-user raw transcript and replays all ~40 turns to compensate pays 10–20x the input tokens, which
renders directly in the arena's token table.

**Fixture guard:** a pure unit test in the scoring package asserts zero non-stopword token overlap
between the needle statement and the recall question — this pins "semantic, not lexical" as a
deterministic, permanently-enforced property of the fixture, not a one-time authoring choice.

**Fairness:** langgraph `BaseStore` semantic index; llamaindex `VectorMemoryBlock`; mastra
`semanticRecall:true, scope:'resource'` — the idiomaticity doc's justified-deviation entry for
`semanticRecall:false` applies **only** to whichever dimension it was written for, never to this one.
Embedder is the shared e5 path used elsewhere in the arena for a like-for-like comparison. Stub lane:
a hash-to-vector fake embedder over the real store machinery (offline, deterministic, zero network).

### 3.4 `memory_consolidation`

Three invocations. Invocation 1 persists a conversational fact (session A). Invocation 2 is a
`"consolidate"` directive — the adapter runs its framework's consolidation **to completion** before
process exit (process exit is the deterministic barrier; no sleeps, no polling for an async job to
finish). Invocation 3 recalls in session B, same user.

Per-framework trigger:

| Framework | Consolidation trigger for invocation 2 |
|---|---|
| langgraph | langmem manager invoked synchronously |
| llamaindex | force a flush so `FactExtractionMemoryBlock` runs |
| mastra | already in-hot-path; invocation 2 is declared a no-op — itself a finding: no async consolidator exists |
| aisotropy | outbox/sleep-cycle drain hook + wait-for-drain (adapter-side work, not core) |
| mem0 (exhibition, see §4) | extraction runs on `add()`; invocation 2 awaits it |

**Honesty caveat**, to be rendered directly in `RESULTS.md`: what is *scored* is the OUTCOME
(conversational fact → queryable durable fact across the session boundary); the MECHANISM (async
background worker vs. synchronous in-hot-path vs. none at all) is reported as a separate, labeled
column, never folded into the pass/fail score. Scorer: `ScoreMemory`; a stricter variant uses the
existing multi-token-key "list what you know" semantics (`scoring.go:83-114`).

## 4. Predicted infeasible cells, run-and-fail cells, and the ai-sdk decision

The arena's full-matrix law (`.claude/rules/arena-full-matrix.md`) requires every
`(framework × dimension × lane × form)` cell to be either a real result or an explicit
`infeasible: <reason>` — never silently blank. **Run-and-fail is the honest verdict for a framework
that is executable but lacks the capability; `infeasible` is reserved for cells that cannot execute
at all** (e.g. the wasm form when the framework's model client needs sockets WASI does not provide).
That distinction governs every row below.

- **All frameworks × all three dimensions × wasm:** `infeasible` — for a per-framework toolchain/dependency
  reason, NOT because "WASI has no sockets" (false since WASI 0.2, January 2024); aisotropy's
  own wasm form is its wazero tool-sandbox only, not a full networked runtime.
- **eino × `memory_semantic_recall` + `memory_consolidation`:** `infeasible` — no official memory
  component exists to exercise; hand-rolling one for the test would benchmark adapter authorship, the
  exact anti-pattern `docs/architecture/arena-idiomaticity-and-dx.md` names. eino ×
  `memory_cross_session`: **run-and-fail expected** (the CheckPointStore path is executable but not
  user-scoped).
- **genkit-go × `memory_semantic_recall` + `memory_consolidation`:** `infeasible` — the experimental
  `core/x/session` is transcript-by-session-ID only, with nothing semantic or consolidating to
  exercise. genkit-go × `memory_cross_session`: **run-and-fail expected**.
- **pydantic-ai × `memory_semantic_recall` + `memory_consolidation`:** **provisional `infeasible`**,
  pending confirmation of the harness `Memory` capability's search mechanism (open question #1 below)
  — if that search turns out to be embedding-backed, `memory_semantic_recall` flips from infeasible to
  runnable.
- **ai-sdk × all three dimensions:** **recommendation = honest `infeasible`** — "no official memory
  primitive; mem0/Letta providers are community-maintained and substitute an external service for the
  framework's own capability" — **plus** a clearly-labeled, **non-competing exhibition row** with mem0
  OSS self-hosted, rendered outside the primary table, following the same secondary/opt-in pattern
  `.claude/rules/arena-deterministic-judging.md` mandates for an LLM-judge signal. Putting ai-sdk+mem0
  in the *primary* table would measure mem0, not ai-sdk — the identical judge-poisoning failure mode
  that rule exists to prevent, just applied to a memory provider instead of an LLM judge. **Blocking
  unknown even for the exhibition row:** whether `@mem0/vercel-ai-provider` can target a self-hosted
  OSS mem0 instance at all — the provider's docs show only a platform API key (open question #2).

## 5. Per-adapter effort estimate

| Adapter | Effort | Notes |
|---|---|---|
| aisotropy | M | drain hook, per-run reset, `user_id` plumbing |
| langgraph | M | `BaseStore` swap (= idiomaticity doc's SHOULD-FIX #1) + langmem wiring |
| llamaindex | M | `Memory` blocks + forcing a flush |
| mastra | S–M | `semanticRecall:true` + `@mastra/qdrant` (= idiomaticity doc's SHOULD-FIX #2); consolidation declared a no-op |
| pydantic-ai | M–L | needs a v2/harness upgrade |
| genkit-go | S | run-and-fail lanes only |
| eino | S | run-and-fail lanes only |
| ai-sdk | S (infeasible route) / L (exhibition row, contingent on open question #2) | |
| Runner shared work | M | `user_id` field, `buildCrossSession`/`buildConsolidation` task builders, two task families with answer keys, determinism fixtures, matrix rows |

## 6. Open questions (gate implementation)

1. pydantic-ai-harness `Memory`'s search mechanism — lexical or embedding-backed. Source not read;
   gates whether `memory_semantic_recall` is provisional-infeasible or runnable for this framework.
2. `@mem0/vercel-ai-provider` and `@mastra/mem0` — whether either can target a self-hosted OSS mem0
   instance, or only the hosted platform (docs show platform API keys only). Gates the ai-sdk
   exhibition row.
3. langmem's synchronous-invocation API — highly likely to exist from the docs, but unverified
   against source.
4. eino's `.cn` documentation domain was unreachable (DNS) during this research; mitigated by reading
   `adk` v0.9.13 at pkg.go.dev, which shows no memory-store types — but the `.cn` docs were never
   directly read.
5. Release recency for `llama-index-memory-mem0`, `@mastra/mem0`, and the Letta ai-sdk provider was
   not individually pulled (version/last-publish dates unconfirmed).
6. Genkit's Agents API (July 2026 preview) memory semantics are not established — the preview
   announcement was read, not the API itself.
7. Zep's legacy CE runnability was not verified — irrelevant to the eligibility verdict, since
   "unmaintained" already disqualifies it regardless of whether it still runs.

**Maturity labels above are docs-grounded plus registry/repository-verified**; only eino was checked
at pkg.go.dev API depth. A source-level read of langmem, the pydantic-ai harness, and the two mem0
providers is the natural Phase 2 verification step before any adapter implementation is scoped —
consistent with `.claude/rules/research-first-protocol.md`'s Phase 0 (this document) → Phase 1
(operator triage of the open questions above) → Phase 2 (implementation) sequence.

## Could not establish

- Whether pydantic-ai-harness `Memory`'s on-demand search is lexical or embedding-backed (open
  question #1) — this single unknown gates one full matrix cell's feasibility classification.
- Whether the mem0 ai-sdk/mastra providers can target self-hosted OSS mem0 versus only the hosted
  platform (open question #2) — gates whether the ai-sdk exhibition row is even buildable.
- langmem's exact synchronous-invocation call shape (open question #3) — inferred from docs prose,
  not read from source.
- Genkit's July 2026 Agents API memory semantics (open question #6) — the feature is a preview
  announcement, not yet documented in enough depth to classify.
- eino's `.cn`-hosted documentation content (open question #4) — DNS-unreachable during this
  research; the pkg.go.dev API surface was the substitute evidence, not a full replacement for
  reading the doc site.

## Cross-links

- `docs/architecture/arena-benchmark-methodology.md` — how latency/token/RSS numbers behind any
  future memory-dimension benchmark cell are actually measured, and its own caveats about small-n
  statistics and provider-window variance that would apply to these new dimensions too.
- `docs/architecture/arena-deterministic-judging.md` — the standing premise (pure-function scoring,
  no LLM judge) this document's scorers (`ScoreMemory`) and the ai-sdk exhibition-row pattern both
  follow.
- `docs/architecture/arena-adapter-artifacts.md` — the Q0 discipline ("is the defect in the core, the
  adapter, or the doc?") that must be applied before any future `memory_cross_session` /
  `memory_semantic_recall` / `memory_consolidation` result is read as a capability claim about any
  framework, aisotropy included.
- `docs/architecture/arena-memory-forensics.md` — the prior aisotropy-adapter memory bug (§3.4's
  `AISOTROPY_DATA_DIR` cross-run contamination) that motivates this document's per-run-data-root
  cross-cutting protocol change.
- `docs/architecture/arena-idiomaticity-and-dx.md` — the SHOULD-FIX items (langgraph `BaseStore`,
  mastra `semanticRecall`) this design reuses as the "official, idiomatic primitive" fairness
  baseline.
- `docs/architecture/arena-full-matrix.md` — the full-cartesian-coverage law and the
  infeasible-vs-run-and-fail-vs-`candidate` vocabulary this document's §4 applies.
- `docs/architecture/arena-tenancy-levels.md` — the companion capability-ladder document; both this
  document and that one exist because a positive uniqueness/absence claim about a competitor
  framework needed an ecosystem-level check before shipping.
- `docs/architecture/memory-tiers.md` — aisotropy's own tiered-memory implementation these arena
  dimensions would measure against the competitors above.
