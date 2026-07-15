---
type: Research Report
title: "Competitive landscape — agent frameworks vs. aisotropy (mid-2026)"
description: An architect-level scan of LangChain/LangGraph, Mastra, Letta, Eino, ADK Go and Genkit Go, judging whether aisotropy's combination (Go-native, tenant-isolation-by-construction, temperature-tiered memory, pre-spend cost governance, 100% MIT/Apache) solves a genuinely unaddressed problem.
tags: [research, competitive, landscape, langchain, langgraph, mastra, letta, eino, adk, genkit, go, memory, multi-tenancy]
timestamp: 2026-07-09T00:00:00Z
---

# Competitive landscape — agent frameworks vs. aisotropy (mid-2026)

> **One line:** aisotropy's bet is defensible as a *combination*, not as any single
> feature. Every individual piece has at least one incumbent attacking it; no incumbent
> covers even three of aisotropy's five defining dimensions together.
> **As of:** 2026-07-09. Claims carry inline sources + dates; thin/conflicting evidence is
> flagged **UNCERTAIN** and should be re-verified before being load-bearing.

This report is the written form of a two-track investigation (a repo deep-read plus a
mid-2026 landscape scan). It exists so the positioning judgment lives in the repo, not only
in session memory. It is a snapshot: agent frameworks move weekly, so treat the dated facts
as perishable.

## The niche aisotropy competes for

aisotropy is not "another agent framework." Its thesis is a specific *combination* (see
[vision/thesis.md](../vision/thesis.md), [architecture/overview.md](../architecture/overview.md)):

1. **Tenant isolation by construction** across every memory tier — not a namespace
   convention a developer must remember.
2. **Temperature-tiered memory** with offline consolidation — working (BadgerDB) →
   facts/FTS5 (rqlite) → semantic vectors (Qdrant) → sleep-cycle batch (DataFusion).
3. **Pre-spend cost governance** — reject/degrade before the LLM call, not accounting after.
4. **Memory as governed, deletable data** — retention/deletion as a first-class concern.
5. **All of the above in Go, in-process latency class**, on a 100% MIT/Apache stack.

The test for "does aisotropy solve a real problem" is whether any incumbent covers these
five together. As of mid-2026, none covers even three.

## The LangChain suite (Python/JS)

- **Versions:** LangChain 1.0 + LangGraph 1.0 shipped late Oct 2025 after three years of v0.x
  churn ([langchain.com/blog/langchain-langgraph-1dot0](https://www.langchain.com/blog/langchain-langgraph-1dot0)).
  OSS core is MIT.
- **Long-term memory:** LangGraph checkpointers + Store + the **LangMem** SDK. LangMem looks
  stagnant as a package (latest PyPI 0.0.30, Oct 27 2025) despite active repo commits
  ([langmem docs](https://langchain-ai.github.io/langmem/)). Legacy
  `ConversationBufferMemory` classes were removed at 1.0.
- **Multi-tenancy:** namespacing convention only (`user_id` in the Store/LangMem namespace).
  No enforced isolation primitive — "don't forget the namespace," not context-propagated.
- **Token economy:** 1.0's answer is **middleware** (summarization, trimming, dynamic tool
  selection) — manual, not a runtime-enforced budget.
- **Self-hosting / pricing:** OSS runtime is MIT and self-hostable, but the **platform**
  (durable execution, task queues, assistants) is the canonical open-core squeeze:
  "Self-Hosted Lite" free to 1M node executions then paywalled; full self-host + self-hosted
  LangSmith are Enterprise/sales-gated ([ZenML analysis](https://www.zenml.io/blog/langgraph-pricing)).
- **Observability:** LangSmith is not OSS; trace-volume pricing; highest lock-in of the major
  tools per multiple 2026 comparisons ([SigNoz](https://signoz.io/comparisons/langsmith-alternatives/)).
  The market's escape hatch is **OTel gen_ai conventions + Langfuse** — exactly where
  aisotropy's ADR-0010 already points.
- **No Go story** (langchaingo is an unofficial community port, `tmc/langchaingo`, MIT).

## Mastra (TypeScript)

- **License:** core **Apache 2.0** — relicensed *from* Elastic License 2.0 after enterprise
  pushback ([mastra.ai/blog/apache-license](https://mastra.ai/blog/apache-license)). Enterprise
  features remain source-available (paid production). The ELv2→Apache reversal is empirical
  evidence that a restrictive license is untenable for a framework — a data point in favor of
  aisotropy's permissive-only stance.
- **Traction:** $22M Series A led by Spark Capital, announced Apr 9 2026 ($35M total), built by
  the Gatsby founders ([report](https://faq.com.tw/en/developer-tools/2026-04-10-mastra-22m-series-a-typescript-agents-en/)).
- **Memory:** the closest TS analog to aisotropy's working memory — working memory
  (resource- or thread-scoped), semantic recall (vector), and memory processors that trim
  retrieved messages before the context window ([memory docs](https://mastra.ai/docs/memory/overview)).
- **Multi-tenancy:** resource-scoping is per-user partitioning, not a runtime-enforced tenant
  boundary. TypeScript-only — no Go.

## Letta (MemGPT) — the closest conceptual neighbor

- **License:** Apache 2.0, self-hostable (Docker/Postgres).
- **Memory hierarchy** is explicitly OS-inspired, nearly isomorphic to aisotropy's framing:
  **Core Memory** (in-context editable blocks; RAM) → **Recall Memory** (out-of-context
  searchable history; disk) → **Archival Memory** (unbounded vector store; cold)
  ([letta.com](https://www.letta.com/)).
- **Sleep-time compute:** a paired background agent consolidates memory during idle time — the
  direct analog of aisotropy's DataFusion sleep cycle
  ([letta blog](https://www.letta.com/blog/sleep-time-compute/), 2025).
- **Differences vs aisotropy:** Python; agents are heavyweight durable server objects; memory
  is **self-managed by the LLM via tool calls** rather than runtime-hydrated (aisotropy's is
  deterministic and auditable); multi-tenancy is app-level (identities/orgs), not
  context-propagated with payload-filtered vector scans; no Go, no facts-tier FTS journal, no
  in-process µs working memory.
- **Takeaway:** Letta *validates the memory-OS thesis* while leaving aisotropy's exact
  combination (Go, isolation-by-construction, runtime-hydrated deterministic memory) open.

## The Go-native threat: Eino and ADK Go

These two are the only serious Go-native competitors. Both iterate fast with big-corp
resources; neither has a tiered-memory or multi-tenancy story today.

### Eino (ByteDance / CloudWeGo) — threat #1

- **Stack:** `github.com/cloudwego/eino`, Apache 2.0. Part of **CloudWeGo** alongside Kitex
  (RPC) and Hertz (HTTP) — it slots into ByteDance's Go microservices stack. Model providers
  via eino-ext (OpenAI, Claude, Gemini, Ark/Volcengine, DeepSeek, Qwen, Ollama).
- **Velocity:** ~35 tagged releases in the 7 weeks 2026-05-19 → 2026-07-07, across a stable
  `v0.9.x` line and a `v0.10.0-alpha` line — roughly one release every 1–2 days
  (api.github.com/repos/cloudwego/eino/releases, fetched 2026-07-08). No stated v1.0 timeline;
  0.x with occasional flagged breaking changes.
- **Team/backing:** small concentrated core (~4–7 active committers), ByteDance-affiliated
  (some `-bytedance`-suffixed contributors; security reported via ByteDance Security). 12.2k
  stars — more than Kitex/Hertz. **Coze Studio's runtime depends on Eino** (verified in its
  `go.mod`); CloudWeGo docs say Doubao/TikTok teams shaped its design.
- **Memory (more developed than assumed):** its ADK subsystem has mature checkpoint/resume,
  shipped session persistence (Jun 2026), and a just-landed **AutoMemory** middleware
  (v0.10.0-alpha.5, 2026-06-16). But it is **process-local / event-log**, not a
  temperature-tiered hierarchy. The vector/retrieval-backed memory middleware
  (issue #995, opened 2026-04-24) is **proposed, not implemented**.
- **Multi-tenancy: none.** A repo-wide search for "multi-tenant" returns **0 results, ever**
  (api.github.com/search/issues, fetched 2026-07-08).
- **Structured output:** provider JSON mode + a parser (`schema.NewMessageJSONParser`); **no
  documented fail-closed schema-violation reflection loop**.
- **Cost:** token-usage tracking via callbacks (Dec 2025), post-hoc; no pre-spend quota
  primitive.
- **Observability:** eino-ext callbacks target APMPlus (ByteDance/Volcengine), CozeLoop,
  Langfuse, LangSmith. **No native OTel gen_ai semconv** directory found. **UNCERTAIN** whether
  APMPlus emits spec-compliant `gen_ai.*` attributes or a proprietary schema over the OTel
  wire.

### ADK Go (Google) — threat #2

- **Stack:** `google.golang.org/adk` (v2 → `google.golang.org/adk/v2`, requires Go 1.25+),
  Apache 2.0. Not a web framework — an embeddable agent runtime. Models via
  `google.golang.org/genai` (Gemini-optimized) plus OpenAI-compatible providers.
- **ADK Go 2.0 (2026-06-30)** — **verified real**
  ([announcing-adk-go-20](https://developers.googleblog.com/announcing-adk-go-20/),
  release tag v2.0.0): a graph-based orchestration engine (concurrent scheduler, conditional
  routing, fan-out/fan-in, per-node retries/timeouts, input/output schema validation,
  human-in-the-loop pause/resume). "Same graph-first direction as Python ADK 2.0."
- **Cadence/parity:** ~monthly minors through v1.4.0, then v2.0.0. **Go trails Python by ~6
  weeks** (ADK Python 2.0 GA 2026-05-19; Python already at v2.4.0 on 2026-07-07 while Go sits
  at v2.0.0). ~56 contributors, several Google-identifiable.
- **Memory:** Session/State/Memory services; the rich backends (VertexAI Session Service,
  **VertexAI MemoryBank** added in ADK Go v1.3.0, RAG memory) are **Vertex-tethered**. Context
  compaction is "on our roadmap" with no committed date (maintainer reply, discussion #589,
  2026-05-21).
- **Multi-tenancy:** nothing beyond app+user+session scoping in the v0.2→v2.0 history
  (**UNCERTAIN**, absence-of-evidence).
- **Deployment:** standalone CLI (`adkgo`), Cloud Run, Agent Engine, or any container —
  genuinely self-hostable, but the whole feature gradient steers toward GCP ("works standalone,
  works best on Vertex").
- **Protocols/observability:** **A2A first-class** (Google donated A2A to the Linux Foundation
  2025-06-23; vendor-neutral by mid-2026), MCP (`mcptoolset`), and **OTel gen_ai semconv**
  (three releases cite semconv work). On observability, ADK *matches* aisotropy's ADR-0010 —
  aisotropy does not differentiate here against ADK, only against Eino/Genkit.

### Genkit Go (Google) — threat #3 (complement, not a direct competitor)

- **Stack:** `github.com/firebase/genkit/go`, Apache 2.0 (also TS/Python/Dart SDKs). Go SDK is
  1.0 GA (2025-09-10) with a Go-style stability promise; latest `go/v1.9.0` (2026-06-16).
- **Positioning:** Google itself frames Genkit as a general-purpose **application library** and
  steers "complex agent topologies" to ADK
  ([build-agentic-full-stack-apps-with-genkit](https://developers.googleblog.com/build-agentic-full-stack-apps-with-genkit/),
  2026-07-01). Its Agents API is in **preview**.
- **Durability:** not native — delivered via the third-party `genkitx-temporal` plugin.
- **Protocols:** **MCP native** (client + server, `go/plugins/mcp`); A2A only community-sample.
- **Observability:** OTel *transport* but a **proprietary attribute schema** (`genkit:metadata:*`,
  `genkit:init`), **not** the `gen_ai.*` semantic conventions (verified against
  pkg.go.dev/…/core/tracing). aisotropy's ADR-0010 is aligned with the market *and* ahead of
  Genkit here.

### Other players (brief)

| Framework | Language | License | Note |
|---|---|---|---|
| Vercel AI SDK 6 | TS | Apache 2.0 | 20M+ monthly downloads; memory is BYO |
| LlamaIndex | Python/TS | MIT | Composable memory blocks (static/fact-extraction/vector) under a token budget |
| Zep / Graphiti | Python | Apache 2.0 (Graphiti) | Temporal knowledge graph; facts with validity windows; Zep = commercial platform on top |
| Mem0 | Python | Apache 2.0 | `add()/search()` memory API; server mode for per-user isolation |
| CrewAI | Python | MIT | Large adoption; reliability-at-concurrency criticism (UNCERTAIN figures) |
| Microsoft Agent Framework | .NET/Python | MIT | 1.0 GA Apr 2026; AutoGen + Semantic Kernel converged (both now maintenance) |
| OpenAI Agents SDK / AgentKit | Python/TS | MIT (SDK) | Runtime-tethered to OpenAI; Agent Builder/Evals wound down (announced 2026-06-03) — platform-risk exemplar |
| Claude Agent SDK | Python/TS | MIT code | Memory tool + context editing; usage governed by Anthropic ToS |

## Time-to-threat by capability (the core judgment)

| Capability (aisotropy's niche) | Eino | ADK Go | Verdict |
|---|---|---|---|
| Tiered / long-term memory | ~6–12 months (AutoMemory shipped; #995 proposed) | Has it, but Vertex-tethered; self-hosted parity unlikely by business model | **The real clock is Eino's** |
| Enforced multi-tenant isolation | No signal (0 issues ever) | No signal | **Open lane — nobody is building it** |
| Pre-spend cost governance | Post-hoc usage only | Not found | **Open lane** |
| Fail-closed structured output | Parser only, no repair loop | Node retries in 2.0; no fail-closed evidence | Differentiator, weaker vs ADK 2.0 |
| OTel gen_ai semconv | No | **Yes** | Differentiates vs Eino/Genkit; ties ADK |

## What is genuinely unsolved (mid-2026)

Across the field, practitioners report the same gaps
([O'Reilly AI Agents Stack 2026](https://www.oreilly.com/radar/the-ai-agents-stack-2026-edition/),
[Fountain City survey](https://fountaincity.tech/resources/blog/how-to-build-and-operate-ai-agent-memory-in-2026/)):

- **Memory lifecycle** (consolidation, promotion/demotion, forgetting) is the least-solved
  dimension; only Letta productized sleep-time consolidation.
- **Multi-tenant isolation is a retrofit everywhere** — convention, not construction. This is
  aisotropy's strongest genuinely-unaddressed claim.
- **Pre-spend cost governance** as a framework primitive is essentially absent.
- **Memory as governed/deletable data** (retention, GDPR/HIPAA) is an open gap.
- Research is *converging on aisotropy's bets* (surprisal-gated storage à la Google Titans;
  sleep-inspired consolidation) but only Letta has productized any of it.

## Licensing / pricing pressure points

- **LangGraph/LangSmith:** OSS runtime MIT, but durable-execution platform + self-hosted
  LangSmith are Enterprise-gated — the canonical open-core squeeze.
- **Mastra:** core Apache 2.0 (relicensed *from* ELv2); enterprise features source-available.
- **Letta, Mem0, Graphiti, Eino, Genkit, ADK Go, Vercel AI SDK, CrewAI core, MS Agent
  Framework:** all Apache 2.0 or MIT, genuinely self-hostable.
- **OpenAI Agents SDK / Claude Agent SDK:** permissive code, but runtime-tethered to the
  vendor's API/ToS — not self-hosting in any meaningful sense.
- **Durable-execution substrates** (Temporal MIT server but ops-heavy; Inngest/Restate
  cloud-oriented; Restate server license **UNCERTAIN** — verify current terms).

**Bottom line:** aisotropy's 100% MIT/Apache stack is a real adoption advantage, not ideology —
Mastra's forced ELv2→Apache reversal is the market proof.

## Positioning conclusion for aisotropy

1. **The combination is the moat, not any single feature.** Tenant-isolation-by-construction +
   temperature-tiered memory + pre-spend quota + Go in-process + permissive licensing has
   exactly one conceptual neighbor (Letta: Python, LLM-self-managed memory, app-level tenancy)
   and two Go-native competitors (Eino, ADK Go) with **no** memory-hierarchy or tenancy story.
2. **The memory window is ~6–12 months and Eino sets the clock** — not Google. AutoMemory +
   issue #995 point straight at it. This argues for accelerating aisotropy's facts/semantic
   tiers (Phases 3–4), which is exactly the perishable part of the moat.
3. **Correction to an earlier internal claim:** it is wrong to say "neither Eino nor ADK has a
   memory story." Eino already has serious *session* memory (checkpoint/session/AutoMemory).
   What no one has is *hierarchical, multi-tenant* memory with offline consolidation — that is
   what aisotropy must market, not "we have memory."
4. **Observability differentiates vs Eino/Genkit, not vs ADK.** ADK is OTel gen_ai semconv
   compliant; aisotropy ties it there and only wins against Eino (proprietary APM) and Genkit
   (proprietary `genkit:*` schema).
5. **Sovereignty cuts both ways in aisotropy's favor:** Western enterprises are wary of
   ByteDance ownership (Eino), and ADK carries a GCP gradient. A Go-native, self-hosted,
   permissively-licensed memory-OS sits in the gap between them.

## Isolation deep-dive — adversarial re-verification (2026-07-14)

> **Added 2026-07-14.** A focused follow-up that stress-tested the four claimed
> differentiators (P1 token efficiency, P2 fail-closed structured output, P3 cost
> observability, P4 multi-tenant isolation) against primary sources, and re-examined the two
> Go-native competitors specifically on isolation. Net effect: it **downgrades** three of the
> four claims and **refines** — does not overturn — the fourth. The 2026-07-09 analysis above
> stands; this section corrects its sharper edges.

### The headline correction: "strongest / most uniform," not "only in kind"

The earlier framing risked implying aisotropy is *categorically the only* framework with any
structural tenant isolation. That is **wrong** — Google's **adk-python** is close-in-kind (see
below). The defensible claim is narrower and survives scrutiny:

- aisotropy is the **strongest and most uniform** on isolation — *every* memory tier is scoped
  by construction, with no tier left as convention.
- aisotropy is **uniquely alone in one specific lane**: **Go + self-hostable + permissively
  licensed (MIT/Apache) OSS** with isolation as an invariant. No competitor occupies that
  intersection.

What "by construction" means here — stated precisely so it is not oversold: **no code path
issues an unscoped read** (internal scoping is unbypassable), and Qdrant applies a `Must`
filter *during* the HNSW scan, not a post-filter. It is **not** authenticated-principal
binding: `tenant_id` is a trusted, validated Huma path param — structurally the *same* trust
ADK places in its caller-supplied `user_id`. The advantage is that the scoping is
unbypassable internally, not that the principal is cryptographically authenticated.

### Eino (correction): an orchestration library, not a runtime

The 2026-07-09 section credited Eino with "serious session memory." On isolation specifically,
the verdict is blunter: **Eino is an Apache-2.0 Go *orchestration* library (a LangGraph-for-Go),
not a multi-tenant runtime, and has zero tenant isolation by construction.**

- Its `CheckpointStore` is a bare `string -> []byte` keyed by a **developer-supplied**
  `WithCheckPointID` string, with **"no default implementation"** — the developer owns the key
  and therefore owns (or forgets) any scoping.
- The `adk-go` subpackage carries state only as an untyped `map[string]any` — **no typed
  scoping primitive at all**.
- Conclusion: a **same-language peer** (Go) but **no isolation model**. It competes on
  orchestration, not on the tenancy invariant.

### Google ADK (correction): the closest structural competitor — but by discipline, not construction

ADK is the one framework whose isolation is close-in-kind, and it deserves an honest, precise
treatment because the difference is subtle:

- **adk-python bakes `user_id` into the storage key.** `InMemorySessionService` uses a nested
  dict (`app_name -> user_id -> session_id`); `DatabaseSessionService` makes `user_id` part of
  a **composite primary key**; `InMemoryMemoryService` keys entries `{app_name}/{user_id}`. This
  is genuinely structural keying — closer to aisotropy than any convention-only competitor.
- **But it is discipline, not construction.** Bug
  [google/adk-python#1641](https://github.com/google/adk-python/issues/1641) was a **real
  cross-user event disclosure through v1.4.2**: the events sub-query **omitted the
  `user_id` filter**, leaking one user's events to another. That is proof isolation lived at the
  **query layer as filter discipline a maintainer could (and did) omit** — not as an invariant.
  aisotropy's construction makes that exact failure *unrepresentable*, because no unscoped read
  path exists. **Cite this as `google/adk-python#1641` — it is a Google ADK issue, NOT an
  aisotropy issue.**
- **Three further caveats blunt ADK as a competitor in aisotropy's lane:** (1) `user_id` is
  unauthenticated, dev-supplied — the same trust posture as aisotropy's `tenant_id`, so ADK's
  keying is not "more authenticated"; (2) `VertexAiSessionService` fetches by `session_id` then
  validates client-side; (3) all of this keying lives in **adk-python** — **adk-go has no
  scoping**, and the keyed backends are **GCP-managed (Vertex), not self-hostable OSS**.
- **Time-box:** the moat is real but perishable. Google maintains adk-go and could port the
  keyed backends into it; that would put a structural competitor into the Go lane.

### The four "differentiators," re-scored against primary sources

| Claim | Prior framing | 2026-07-14 verdict |
|---|---|---|
| **P1 — token efficiency** | Incumbents "sabotage" token budgets | **WEAK / strawman as a blanket accusation.** Modern frameworks ship context trimming/summarization; no evidence of systemic waste. Drop the accusation. |
| **P2 — fail-closed structured-output reflection loop** | Unique differentiator | **COLLAPSED as a differentiator** (cross-stack survey, 2026-07-14). Fail-closed self-repair is common across the field: Pydantic AI (`raise ModelRetry` → `UnexpectedModelBehavior`), **LangChain-Python** (`OutputFixingParser`/`RetryOutputParser` re-invoke an LLM with `error=repr(e)`, re-raise on exhaustion — opt-in, not in `with_structured_output`), Spring AI 2.0 (`StructuredOutputValidationAdvisor`, 3 attempts), and CrewAI (`guardrail_max_retries`) all have it. Only LangChain.js (langchainjs#9426), LangChain4j, and Vercel AI SDK throw *without* self-repair. aisotropy's only residual nuance: its loop is **default/runtime-wired**, not an opt-in wrapper the developer must remember to attach — a "default posture" argument, not an "only one who has it" claim. |
| **P3 — cost observability per token/tenant** | Differentiator | **Refine.** LangSmith computes cost automatically but is **observability-only** (no pre-spend gate) and **not per-tenant native** (metadata is DIY). Pydantic AI `UsageLimits` has a pre-spend `check_before_request()` but **per-run, not per-tenant**. aisotropy's real edge is the **pre-spend quota *gate* + per-tenant *by construction***, not "seeing" cost. |
| **P4 — multi-tenant isolation by construction** | Differentiator | **The one surviving differentiator** — with the "strongest/most-uniform, not only-in-kind" refinement above. None of LangChain/LangGraph (namespace convention; prefix search scans across tenants), Mastra (`resourceId`/`threadId` convention; `listMessages` threadId-only bug mastra#11856), Pydantic AI (no tenant memory model), Letta (LLM-self-managed memory), or Eino enforce isolation by construction. ADK-python is close-in-kind but query-discipline (see #1641). |

### Net takeaway (2026-07-14)

- **Only P4 is a genuine, standalone differentiator.** P1 should be dropped as framed; **P2 has
  collapsed** (fail-closed self-repair is common — see the re-scored table and the cross-stack
  survey below); P3 survives only in a *refined, narrowed* form (pre-spend *gate* + per-tenant,
  not cost *visibility*).
- **P4's moat is real but time-boxed and lane-specific:** the cross-stack survey below (~30
  frameworks across Python/TS/Java/.NET/Go/Rust/hosted) shows aisotropy is the **only
  self-hostable, permissive-OSS runtime that isolates by construction**. The proof point that
  isolation-by-construction matters is `google/adk-python#1641` — the closest structural
  competitor shipped a real cross-user leak precisely because it relied on per-query filter
  discipline.

## Cross-stack survey — the full isolation matrix (2026-07-14)

A comprehensive, adversarially-verified survey of ~30 agent/LLM frameworks across **Python,
TypeScript, Java, .NET, Go, Rust, and hosted APIs** — to test the isolation claim against the
*whole* field, not a hand-picked few. The single question per tool: **when two tenants share a
deployment, what stops tenant A's memory/vectors from being read for tenant B?**

The answer collapses to four categories. Only one of them is "by construction, in code you run."

| Framework (stack) | Isolation model | Failure mode of a wrong / omitted key |
|---|---|---|
| **aisotropy** (Go, MIT/Apache) | **By construction, self-hostable OSS** — BadgerDB key-prefix + facts SQL `WHERE tenant+user` + Qdrant `Must`-filter *during* the HNSW scan | A `Scope{}` cannot be built unscoped; no request-path unscoped read exists (one reviewed offline `ExportAll`) |
| LangChain core (Python) | None — no tenant construct | n/a (single-tenant library) |
| AutoGen / AG2 (Python) | None | n/a |
| Vercel AI SDK (TS) | None — stateless primitives | n/a |
| Rig (Rust) | None | n/a |
| langchaingo (Go) | None | n/a |
| Eino (Go) | None — `CheckpointStore` is a bare developer-keyed string | Wrong/reused checkpoint-id silently cross-reads |
| DSPy (Python) | None — LM programming model | n/a |
| Griptape (Python) | None | n/a |
| LlamaIndex (Py/TS) | Convention — `MetadataFilters` attached at query time | Omit the filter → scans the whole shared index |
| Haystack (Python) | Convention — filters at retrieval time | Omit → scans the whole DocumentStore |
| CrewAI (Python) | Convention — developer-keyed memory | Wrong key cross-reads |
| Agno (Python) | Convention — developer-passed keys | Wrong key cross-reads |
| Genkit (JS/Go/Py) | Convention — developer-passed session | Wrong key cross-reads |
| VoltAgent (TS) | Convention | Wrong key cross-reads |
| Mastra (TS) | Convention — `resourceId`/`threadId` | `listMessages` threadId-only (mastra#11856) |
| Spring AI (Java) | Convention — `conversationId` only | Wrong conversationId cross-reads |
| LangChain4j (Java) | Convention — `ChatMemoryStore` keyed by `memoryId` | Wrong `memoryId` cross-reads |
| Semantic Kernel (.NET/Py/Java) | Convention — developer-managed collections/keys | Omit → cross-reads |
| Microsoft Agent Framework | Convention — developer-passed thread/state | Cross-reads |
| LangGraph Store / Platform (Py) | Convention — "isolation not enforced by default"; `@auth.on` filters are developer-passed | Omit the auth handler → cross-reads |
| Letta OSS (Python) | Convention — `Identities`/tags developer-passed | Cross-reads |
| **Google Vertex Memory Bank** | **By construction — but hosted-managed (rented)** — "For each scope, Memory Bank maintains an isolated collection of memories" + IAM | Enforced *vendor-side* in closed-source GCP; not code you self-host |
| AWS Bedrock AgentCore | Hosted boundary — namespace convention + IAM backstop | AWS: "doesn't automatically prevent cross-user access" |
| OpenAI Assistants / Agents | Hosted boundary — coarse org/project only | Per-user isolation is your application code |
| Letta Cloud | Hosted boundary — per-user API keys (paid tier only) | The Apache-2.0 self-hosted core is single-user |
| **Dify** (self-hostable *platform*) | App-layer `WHERE tenant_id` (SQLAlchemy) — **and** license forbids multi-tenant SaaS without a paid license | License gate fails; a query omitting the filter cross-reads; UI app-builder, not a runtime |
| **Flowise** (self-hostable *platform*) | Multi-tenancy is commercial-license-gated (`packages/server/src/enterprise/`); Apache core is single-tenant | No workspace/org tenancy in the OSS core at all |
| **Langflow** (self-hostable *platform*) | MIT, but isolates by construction **nowhere** — delegates tenant isolation to infra; soft `WHERE user_id` only | Its own Security doc: "no isolation between users" in a process |
| **Cloudflare Agents** (*platform*) | **By construction — but hosted-only** — one Agent = one Durable Object + private SQLite (stronger than per-op filtering) | Durable Objects is Cloudflare-proprietary; the isolation is rented, not self-hostable |

**The moat is a four-way conjunction:** `{self-hostable} ∩ {permissive OSS for commercial SaaS} ∩
{isolation by construction} ∩ {embeddable runtime}`. Across the whole survey — ~30 frameworks plus
the four self-hostable multi-tenant platforms — that intersection is occupied by **aisotropy
alone**. Two competitors match aisotropy's isolation *strength* by construction — **Google Vertex
Memory Bank** and **Cloudflare Agents** (one Agent = one Durable Object + private SQLite, arguably
*stronger* than aisotropy's per-op filtering) — but both are **hosted/rented**, not self-hostable
OSS. Every self-hostable OSS competitor either isolates only by developer-passed convention, gates
multi-tenancy behind a commercial license (Dify, Flowise), or does not isolate at all (Langflow).
aisotropy wins the intersection on **delivery** (self-hostable + permissive), not on raw isolation
robustness — the honest correction to "the only one that isolates by construction," which is no
longer literally true.

**Two honest bounds on this claim:**

1. **The guarantee is conditional on a correct identity.** aisotropy's scoping is structural
   *given* a `(tenant_id, user_id)`; those arrive today as trusted-but-unauthenticated path
   params (no authn middleware wired). The by-construction guarantee begins one layer above an
   authn seam that is not yet built — a real, closeable gap, not a refutation. See
   [thesis → the authentication gap](../vision/thesis.md#the-authentication-gap-be-honest-about-it).
2. **The isolation is uniform call-site discipline, not DB-level RLS.** aisotropy applies the
   tenant filter on *every* storage op — BadgerDB key-prefix (no unscoped read *method* exists),
   facts SQL `WHERE tenant+user` on every statement, Qdrant `Must`-filter during the scan —
   backed by unforgeable value-object `Scope`s (unexported fields). That is stronger than a
   filter a query can *forget* on the current surface, but it is **not** a database guarantee
   like Postgres RLS or schema-per-tenant: a *new* storage op that omits the filter would leak.
   It is code-review/value-object-enforced, not DB-enforced (the one documented exception,
   `facts.ExportAll`, is offline-only). Same *class* as Dify/Flowise app-layer WHERE — but applied
   uniformly per-op rather than retrofitted, and behind unforgeable scopes.
3. **Self-hostable multi-tenant *platforms* — surveyed (2026-07-14), none in the lane.** **Dify** —
   self-hostable, but its Apache-2.0 + additional terms verbatim *"you may not use the Dify source
   code to operate a multi-tenant environment"* without a paid license (fails the permissive gate);
   isolation is app-layer `WHERE tenant_id`; UI app-builder. **Flowise** — Workspace/Org
   multi-tenancy is Commercial-License-gated (`packages/server/src/enterprise/` + `IdentityManager.ts`),
   so the Apache core ships no multi-tenancy; UI builder. **Langflow** — MIT, but its Security doc
   states it enforces no isolation between users in a process and delegates it to infra. **Cloudflare
   Agents** — MIT SDK, same embeddable category, genuinely by-construction per-object isolation, but
   **hosted-only** on Cloudflare's Durable Objects (rented, not self-hostable). Still uncovered:
   CN-origin frameworks (AgentScope, Qwen-Agent) and durable-execution runtimes
   (Temporal/Restack/Modal/Inngest).

## Sources & caveats

Every dated claim above cites a primary or clearly-labeled secondary source inline. Items
marked **UNCERTAIN** (Eino's Feb–Aug 2025 release-tag gap; APMPlus gen_ai semconv compliance;
ADK Go's Go-native VertexAI RAG memory equivalent; exact per-project engineering headcounts;
Restate's current server license; whether Genkit's Agents API preview covers Python/Dart) rest
on thin or conflicting evidence and must be re-verified before being used as load-bearing
facts. This landscape is a 2026-07-09 snapshot (isolation deep-dives 2026-07-14); agent
frameworks change weekly. The hosted-platform isolation verdicts (Vertex Memory Bank, Bedrock
AgentCore) were adversarially verified but their docs rendered as JS shells to the fetcher, so
quotes came from official search-index snippets + corroborating blogs rather than a live-HTML
diff; the OSS convention verdicts rest on docs, issue trackers, and source reads.
